package source

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodbstreams"
	stypes "github.com/aws/aws-sdk-go-v2/service/dynamodbstreams/types"
	"github.com/aws/smithy-go"
)

// Reading the whole table every refresh is what costs money: a Scan is billed
// for every item it reads. So the gallery scans once, and then follows the
// table's DynamoDB stream, which carries only the changes. The stream has to be
// enabled on the table with the view type NEW_IMAGE (docs/aws-access.md). A
// table without one is scanned at every refresh, as before.
//
// The picture of the table the gallery holds (Dynamo.items) is made of one scan
// and the stream, under these rules:
//
//   - Every shard is read from the start of what the stream still holds (24 hours),
//     never from "the end". A change made while the scan runs, or that the scan
//     did not yet see (a Scan is only eventually consistent), is then in the
//     stream. Applying a change the scan already shows does no harm: a change
//     replaces the item as a whole, and the changes to one item are applied in
//     order, so the picture ends up as the table is. A rescan therefore replays
//     up to a day of changes, which is a few requests.
//   - A shard is read only after its parent shard has been read to its end.
//   - The place in each shard is kept as a sequence number, and an iterator is
//     asked for at every refresh, so nothing expires: the gallery may refresh as
//     rarely as it likes.
//   - A request that fails in a way that may pass (throttling, the network) keeps
//     everything and tries again at the next refresh. After maxFailures in a
//     row, or at once for anything that cannot pass (records trimmed away
//     before they were read, a shard gone, the stream turned off or replaced, a
//     permission removed), the picture is thrown away and the table is scanned
//     again, and the stream joined again. So it is every resyncEvery, whatever
//     happened. A stream that fails again soon after is left alone for a while.
const (
	// resyncEvery is how long a picture built from the stream is trusted before the table is scanned again.
	resyncEvery = 6 * time.Hour
	// attachEvery is how often a table whose stream cannot be used is looked at again.
	attachEvery = time.Hour
	// flapping: a stream dropped twice within this long is not joined again for attachEvery.
	flapping = 10 * time.Minute
	// maxFailures is how many refreshes in a row may fail before the picture is rebuilt.
	maxFailures = 3
	// callsPerShard bounds the GetRecords calls for one shard in one refresh; the rest is read at the next.
	callsPerShard = 100
	// emptyPages: an empty page can come before more records, so a shard that has not been read up to its end
	// before is read through this many in a row. Once it has, one empty page means there is nothing new.
	emptyPages = 3
	// rediscoverEvery is how often the list of shards is asked for again, besides whenever a shard has ended.
	rediscoverEvery = 5 * time.Minute
	// slowFollow: reading the stream that takes longer than this is logged.
	slowFollow = 2 * time.Second
)

// TableAPI is the DynamoDB operation, besides Scan, that finds the table's stream.
type TableAPI interface {
	DescribeTable(ctx context.Context, in *dynamodb.DescribeTableInput, opts ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error)
}

// StreamsAPI is the DynamoDB Streams operations the reader uses. All three only read.
type StreamsAPI interface {
	DescribeStream(ctx context.Context, in *dynamodbstreams.DescribeStreamInput, opts ...func(*dynamodbstreams.Options)) (*dynamodbstreams.DescribeStreamOutput, error)
	GetShardIterator(ctx context.Context, in *dynamodbstreams.GetShardIteratorInput, opts ...func(*dynamodbstreams.Options)) (*dynamodbstreams.GetShardIteratorOutput, error)
	GetRecords(ctx context.Context, in *dynamodbstreams.GetRecordsInput, opts ...func(*dynamodbstreams.Options)) (*dynamodbstreams.GetRecordsOutput, error)
}

// stream is the state of one followed stream.
type stream struct {
	arn          string
	shards       map[string]*shard
	failures     int       // refreshes in a row that failed in a way that may pass
	discoveredAt time.Time // when the list of shards was last asked for
	calls        int       // requests made by the current follow
	changes      int       // changes applied by the current follow
}

type shard struct {
	id, parent string
	seq        string // the last sequence number applied; empty: nothing yet, read from the start
	iter       string // the iterator to read on with, if it is still good; empty: ask for one
	tip        bool   // read up to its end before: one empty page is enough to stop
	done       bool   // read to its end
}

// Why a stream is not used. The text is stable, so that a warning is given once; the error says more.
const (
	whyNone     = "the table has no stream"
	whyView     = "the table's stream has a view type the gallery cannot use (it needs NEW_IMAGE)"
	whyDescribe = "the table cannot be described (the policy needs dynamodb:DescribeTable)"
	whyRead     = "the table's stream cannot be read (the policy needs dynamodb:DescribeStream, GetShardIterator and GetRecords)"
)

// attachError says why no stream is followed. definitive: asking again soon will not change the answer.
type attachError struct {
	why        string
	err        error
	definitive bool
}

func (e *attachError) Error() string { return e.why + ": " + e.err.Error() }
func (e *attachError) Unwrap() error { return e.err }

// WithStreams lets the reader follow the table's stream when it has one. table
// may be the Scan client itself if that also has DescribeTable.
func (d *Dynamo) WithStreams(table TableAPI, streams StreamsAPI) *Dynamo {
	d.tableAPI, d.streams = table, streams
	return d
}

// attach looks for a usable stream. It returns nil, after saying why (once, for
// a lasting reason), if there is none: the table is then scanned.
func (d *Dynamo) attach(ctx context.Context) *stream {
	st, err := d.findStream(ctx)
	if err == nil {
		clear(d.noStream)
		d.log.Info("following the table's stream", "table", d.table, "shards", len(st.shards))
		return st
	}
	if ctx.Err() != nil {
		return nil
	}
	ae, _ := errors.AsType[*attachError](err)
	if ae == nil || !ae.definitive {
		d.triedAt = time.Time{} // may pass: look again at the next refresh
		d.log.Warn("could not look for the table's stream; reading the whole table", "table", d.table, "error", err)
		return nil
	}
	if !d.noStream[ae.why] {
		d.noStream[ae.why] = true
		d.log.Warn("reading the whole table at every refresh, which costs more than following its stream: "+ae.why, "table", d.table, "error", ae.err)
	}
	return nil
}

func (d *Dynamo) findStream(ctx context.Context) (*stream, error) {
	out, err := d.tableAPI.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(d.table)})
	if err != nil {
		return nil, &attachError{whyDescribe, err, denied(err)}
	}
	t := out.Table
	switch {
	case t == nil || t.StreamSpecification == nil || !aws.ToBool(t.StreamSpecification.StreamEnabled) || t.LatestStreamArn == nil:
		return nil, &attachError{whyNone, errors.New("no stream is enabled"), true}
	case t.StreamSpecification.StreamViewType != types.StreamViewTypeNewImage && t.StreamSpecification.StreamViewType != types.StreamViewTypeNewAndOldImages:
		return nil, &attachError{whyView, fmt.Errorf("the view type is %s", t.StreamSpecification.StreamViewType), true}
	}
	st := &stream{arn: *t.LatestStreamArn, shards: map[string]*shard{}}
	if err := d.discover(ctx, st); err != nil {
		return nil, &attachError{whyRead, err, denied(err)}
	}
	return st, nil
}

// denied reports whether err is a refusal by AWS to allow the call, which asking again will not change.
func denied(err error) bool {
	ae, ok := errors.AsType[smithy.APIError](err)
	if !ok {
		return false
	}
	switch ae.ErrorCode() {
	case "AccessDeniedException", "AccessDenied", "UnauthorizedOperation", "ResourceNotFoundException":
		return true
	}
	return false
}

// errFatal marks what asking again will not mend.
var errFatal = errors.New("the stream cannot be followed")

// discover adds the shards of the stream that are not known yet, and forgets the finished ones the
// stream no longer lists (trimmed away after 24 hours).
func (d *Dynamo) discover(ctx context.Context, st *stream) error {
	var all []stypes.Shard
	var from *string
	for {
		out, err := d.streams.DescribeStream(ctx, &dynamodbstreams.DescribeStreamInput{StreamArn: &st.arn, ExclusiveStartShardId: from})
		if err != nil {
			return err
		}
		desc := out.StreamDescription
		if desc == nil || desc.StreamStatus != stypes.StreamStatusEnabled {
			return fmt.Errorf("%w: the stream is not enabled", errFatal)
		}
		all = append(all, desc.Shards...)
		if from = desc.LastEvaluatedShardId; from == nil {
			break
		}
	}
	st.discoveredAt = d.now()
	st.calls++
	present := map[string]bool{}
	for _, sh := range all {
		id := aws.ToString(sh.ShardId)
		present[id] = true
		if _, ok := st.shards[id]; !ok {
			st.shards[id] = &shard{id: id, parent: aws.ToString(sh.ParentShardId)}
		}
	}
	for id, s := range st.shards {
		if present[id] {
			continue
		}
		if !s.done {
			return fmt.Errorf("%w: shard %s left the stream before it was read to its end", errFatal, id)
		}
		delete(st.shards, id)
	}
	return nil
}

// follow reads the changes the stream holds and applies them to d.items. A refresh with nothing new
// is one GetRecords call for each open shard; the list of shards is asked for only when one has
// ended (it may have children) and every rediscoverEvery.
func (d *Dynamo) follow(ctx context.Context) error {
	st := d.stream
	st.calls, st.changes = 0, 0
	start := d.now()
	if d.now().Sub(st.discoveredAt) >= rediscoverEvery {
		if err := d.discover(ctx, st); err != nil {
			return err
		}
	}
	// A shard waits for its parent. Reading one to its end can free its children, so go round again.
	for progress := true; progress; {
		progress = false
		ids := make([]string, 0, len(st.shards))
		for id := range st.shards {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			s := st.shards[id]
			if s.done {
				continue
			}
			if p := st.shards[s.parent]; p != nil && !p.done {
				continue
			}
			if err := d.readShard(ctx, st, s); err != nil {
				return fmt.Errorf("shard %s: %w", id, err)
			}
			if s.done {
				progress = true
				if err := d.discover(ctx, st); err != nil { // its children
					return err
				}
				break // the shards changed
			}
		}
	}
	if took := d.now().Sub(start); took > slowFollow {
		d.log.Info("reading the table's stream was slow", "duration", took.Round(time.Millisecond), "requests", st.calls, "changes", st.changes, "shards", len(st.shards))
	} else {
		d.log.Debug("read the table's stream", "duration", took.Round(time.Millisecond), "requests", st.calls, "changes", st.changes, "shards", len(st.shards))
	}
	return nil
}

// iterator asks for a new iterator for s: after the last change applied, or from the start.
func (d *Dynamo) iterator(ctx context.Context, st *stream, s *shard) error {
	in := &dynamodbstreams.GetShardIteratorInput{StreamArn: &st.arn, ShardId: &s.id, ShardIteratorType: stypes.ShardIteratorTypeTrimHorizon}
	if s.seq != "" {
		in.ShardIteratorType, in.SequenceNumber = stypes.ShardIteratorTypeAfterSequenceNumber, &s.seq
	}
	st.calls++
	it, err := d.streams.GetShardIterator(ctx, in)
	if err != nil {
		if _, ok := errors.AsType[*stypes.TrimmedDataAccessException](err); ok {
			return fmt.Errorf("%w: %w", errFatal, err)
		}
		return err
	}
	s.iter = aws.ToString(it.ShardIterator)
	return nil
}

func (d *Dynamo) readShard(ctx context.Context, st *stream, s *shard) error {
	renewed, empty := false, 0
	for range callsPerShard {
		if s.iter == "" {
			if err := d.iterator(ctx, st, s); err != nil {
				return err
			}
		}
		st.calls++
		out, err := d.streams.GetRecords(ctx, &dynamodbstreams.GetRecordsInput{ShardIterator: &s.iter, Limit: aws.Int32(1000)})
		if err != nil {
			if _, ok := errors.AsType[*stypes.TrimmedDataAccessException](err); ok {
				return fmt.Errorf("%w: %w", errFatal, err)
			}
			// An iterator is good for 15 minutes. If this one is not, ask for another, once.
			if _, ok := errors.AsType[*stypes.ExpiredIteratorException](err); ok && !renewed {
				renewed, s.iter = true, ""
				continue
			}
			return err
		}
		for _, rec := range out.Records {
			if err := d.apply(rec); err != nil {
				return fmt.Errorf("%w: %w", errFatal, err)
			}
			s.seq = aws.ToString(rec.Dynamodb.SequenceNumber)
			st.changes++
		}
		if out.NextShardIterator == nil {
			s.done, s.iter = true, ""
			return nil
		}
		s.iter = *out.NextShardIterator
		if len(out.Records) > 0 {
			empty = 0
			continue
		}
		if empty++; empty >= emptyPages || s.tip {
			s.tip = true
			return nil // caught up; whatever comes is read at the next refresh
		}
	}
	return nil
}

// apply puts one change into the picture of the table.
func (d *Dynamo) apply(rec stypes.Record) error {
	if rec.Dynamodb == nil {
		return errors.New("a stream record has no data")
	}
	switch rec.EventName {
	case stypes.OperationTypeRemove:
		if id, ok := rec.Dynamodb.Keys["id"].(*stypes.AttributeValueMemberS); ok {
			delete(d.items, id.Value)
		}
	case stypes.OperationTypeInsert, stypes.OperationTypeModify:
		if len(rec.Dynamodb.NewImage) == 0 {
			return errors.New("a stream record carries no new image: the stream's view type must be NEW_IMAGE")
		}
		r, err := decodeItem(fromStream(rec.Dynamodb.NewImage))
		if err != nil { // not the bot's shape: not shown, as in a scan
			if id, ok := rec.Dynamodb.Keys["id"].(*stypes.AttributeValueMemberS); ok {
				delete(d.items, id.Value)
			}
			d.log.Debug("skipped a changed item that does not have the bot's shape", "error", err)
			return nil
		}
		d.items[r.ID] = r
	}
	return nil
}

// fromStream converts a stream's attribute values to the table API's, which is what decodeItem reads.
func fromStream(m map[string]stypes.AttributeValue) map[string]types.AttributeValue {
	out := make(map[string]types.AttributeValue, len(m))
	for k, v := range m {
		out[k] = fromStreamValue(v)
	}
	return out
}

func fromStreamValue(v stypes.AttributeValue) types.AttributeValue {
	switch v := v.(type) {
	case *stypes.AttributeValueMemberS:
		return &types.AttributeValueMemberS{Value: v.Value}
	case *stypes.AttributeValueMemberN:
		return &types.AttributeValueMemberN{Value: v.Value}
	case *stypes.AttributeValueMemberB:
		return &types.AttributeValueMemberB{Value: v.Value}
	case *stypes.AttributeValueMemberBOOL:
		return &types.AttributeValueMemberBOOL{Value: v.Value}
	case *stypes.AttributeValueMemberNULL:
		return &types.AttributeValueMemberNULL{Value: v.Value}
	case *stypes.AttributeValueMemberSS:
		return &types.AttributeValueMemberSS{Value: v.Value}
	case *stypes.AttributeValueMemberNS:
		return &types.AttributeValueMemberNS{Value: v.Value}
	case *stypes.AttributeValueMemberBS:
		return &types.AttributeValueMemberBS{Value: v.Value}
	case *stypes.AttributeValueMemberM:
		return &types.AttributeValueMemberM{Value: fromStream(v.Value)}
	case *stypes.AttributeValueMemberL:
		l := make([]types.AttributeValue, len(v.Value))
		for i, e := range v.Value {
			l[i] = fromStreamValue(e)
		}
		return &types.AttributeValueMemberL{Value: l}
	}
	return &types.AttributeValueMemberNULL{Value: true}
}

// current is the picture of the table as a list, in the order of the ids.
func (d *Dynamo) current() []Record {
	out := make([]Record, 0, len(d.items))
	for _, r := range d.items {
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b Record) int { return strings.Compare(a.ID, b.ID) })
	return out
}
