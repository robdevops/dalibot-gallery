package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodbstreams"
	stypes "github.com/aws/aws-sdk-go-v2/service/dynamodbstreams/types"
	"github.com/aws/smithy-go"
)

// fakeTable is a DynamoDB table with a stream, as far as the reader uses them.
type fakeTable struct {
	mu      sync.Mutex
	items   map[string]map[string]types.AttributeValue
	enabled bool
	view    types.StreamViewType
	status  stypes.StreamStatus
	shards  []*fakeShard
	seq     int

	scans, describes, gets int
	consistent             []bool                                     // ConsistentRead of every scan
	beforeScan             func()                                     // runs after the scan has read the table, before it answers
	staleScan              map[string]map[string]types.AttributeValue // when set, what every scan returns: a scan that is behind
	emptyPages             int                                        // empty pages returned before each page that has records
	fail                   map[string]error                           // by operation name
}

type fakeShard struct {
	id, parent string
	records    []stypes.Record
	closed     bool
	hidden     bool        // not listed by DescribeStream
	empty      map[int]int // empty pages still to give before the records at a position
}

func newFakeTable() *fakeTable {
	f := &fakeTable{items: map[string]map[string]types.AttributeValue{}, enabled: true, view: types.StreamViewTypeNewImage,
		status: stypes.StreamStatusEnabled, fail: map[string]error{}}
	f.shards = []*fakeShard{{id: "shard-0"}}
	return f
}

func (f *fakeTable) open() *fakeShard {
	for _, s := range slices.Backward(f.shards) {
		if !s.closed {
			return s
		}
	}
	panic("no open shard")
}

func rec(id, prompt string) map[string]types.AttributeValue {
	s := func(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
	return map[string]types.AttributeValue{
		"id": s(id), "prompt": s(prompt), "command": s("dream"), "current": s("_"),
		"ttl": &types.AttributeValueMemberN{Value: "2000000000"},
		"images": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
			"_": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"file_id": s("file-" + id), "caption": s("c")}},
		}},
	}
}

func toStream(v types.AttributeValue) stypes.AttributeValue {
	switch v := v.(type) {
	case *types.AttributeValueMemberS:
		return &stypes.AttributeValueMemberS{Value: v.Value}
	case *types.AttributeValueMemberN:
		return &stypes.AttributeValueMemberN{Value: v.Value}
	case *types.AttributeValueMemberM:
		m := map[string]stypes.AttributeValue{}
		for k, e := range v.Value {
			m[k] = toStream(e)
		}
		return &stypes.AttributeValueMemberM{Value: m}
	}
	panic("fake: unsupported value")
}

func (f *fakeTable) change(item map[string]types.AttributeValue, id string, remove bool) {
	f.seq++
	r := stypes.Record{EventName: stypes.OperationTypeModify, Dynamodb: &stypes.StreamRecord{
		SequenceNumber: aws.String(fmt.Sprintf("%08d", f.seq)),
		Keys:           map[string]stypes.AttributeValue{"id": &stypes.AttributeValueMemberS{Value: id}},
	}}
	if remove {
		r.EventName = stypes.OperationTypeRemove
		delete(f.items, id)
	} else {
		if _, ok := f.items[id]; !ok {
			r.EventName = stypes.OperationTypeInsert
		}
		f.items[id] = item
		r.Dynamodb.NewImage = map[string]stypes.AttributeValue{}
		for k, v := range item {
			r.Dynamodb.NewImage[k] = toStream(v)
		}
	}
	s := f.open()
	s.records = append(s.records, r)
}

func (f *fakeTable) put(id, prompt string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.change(rec(id, prompt), id, false)
}

func (f *fakeTable) del(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.change(nil, id, true)
}

// split closes the open shard and opens a child of it.
func (f *fakeTable) split(child string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parent := f.open()
	parent.closed = true
	f.shards = append(f.shards, &fakeShard{id: child, parent: parent.id})
}

func (f *fakeTable) failing(op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.fail, op)
	} else {
		f.fail[op] = err
	}
}

func (f *fakeTable) Scan(_ context.Context, in *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	f.mu.Lock()
	f.scans++
	f.consistent = append(f.consistent, aws.ToBool(in.ConsistentRead))
	if err := f.fail["Scan"]; err != nil {
		f.mu.Unlock()
		return nil, err
	}
	src := f.items
	if f.staleScan != nil {
		src = f.staleScan
	}
	out := &dynamodb.ScanOutput{}
	for _, it := range src {
		out.Items = append(out.Items, it)
	}
	hook := f.beforeScan
	f.mu.Unlock()
	if hook != nil {
		hook() // what happens while the scan is under way: after what it read
	}
	return out, nil
}

func (f *fakeTable) DescribeTable(context.Context, *dynamodb.DescribeTableInput, ...func(*dynamodb.Options)) (*dynamodb.DescribeTableOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.describes++
	if err := f.fail["DescribeTable"]; err != nil {
		return nil, err
	}
	t := &types.TableDescription{}
	if f.enabled {
		t.StreamSpecification = &types.StreamSpecification{StreamEnabled: aws.Bool(true), StreamViewType: f.view}
		t.LatestStreamArn = aws.String("arn:aws:dynamodb:x:1:table/dalibot/stream/t")
	}
	return &dynamodb.DescribeTableOutput{Table: t}, nil
}

func (f *fakeTable) DescribeStream(_ context.Context, in *dynamodbstreams.DescribeStreamInput, _ ...func(*dynamodbstreams.Options)) (*dynamodbstreams.DescribeStreamOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail["DescribeStream"]; err != nil {
		return nil, err
	}
	d := &stypes.StreamDescription{StreamStatus: f.status}
	// Two shards a page, to exercise the paging.
	skip := in.ExclusiveStartShardId != nil
	n := 0
	for _, s := range f.shards {
		if s.hidden {
			continue
		}
		if skip {
			if s.id == *in.ExclusiveStartShardId {
				skip = false
			}
			continue
		}
		if n == 2 {
			d.LastEvaluatedShardId = d.Shards[len(d.Shards)-1].ShardId
			break
		}
		sh := stypes.Shard{ShardId: aws.String(s.id), SequenceNumberRange: &stypes.SequenceNumberRange{StartingSequenceNumber: aws.String("0")}}
		if s.parent != "" {
			sh.ParentShardId = aws.String(s.parent)
		}
		if s.closed {
			sh.SequenceNumberRange.EndingSequenceNumber = aws.String("9")
		}
		d.Shards = append(d.Shards, sh)
		n++
	}
	return &dynamodbstreams.DescribeStreamOutput{StreamDescription: d}, nil
}

func (f *fakeTable) shard(id string) *fakeShard {
	for _, s := range f.shards {
		if s.id == id {
			return s
		}
	}
	return nil
}

// An iterator is "shard|index".
func (f *fakeTable) GetShardIterator(_ context.Context, in *dynamodbstreams.GetShardIteratorInput, _ ...func(*dynamodbstreams.Options)) (*dynamodbstreams.GetShardIteratorOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail["GetShardIterator"]; err != nil {
		return nil, err
	}
	s := f.shard(*in.ShardId)
	idx := 0
	switch in.ShardIteratorType {
	case stypes.ShardIteratorTypeTrimHorizon:
	case stypes.ShardIteratorTypeAfterSequenceNumber:
		for i, r := range s.records {
			if *r.Dynamodb.SequenceNumber == *in.SequenceNumber {
				idx = i + 1
			}
		}
	default:
		panic("fake: the gallery asked for an iterator type it should not use: " + string(in.ShardIteratorType))
	}
	return &dynamodbstreams.GetShardIteratorOutput{ShardIterator: aws.String(fmt.Sprintf("%s|%d", s.id, idx))}, nil
}

func (f *fakeTable) GetRecords(_ context.Context, in *dynamodbstreams.GetRecordsInput, _ ...func(*dynamodbstreams.Options)) (*dynamodbstreams.GetRecordsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	if err := f.fail["GetRecords"]; err != nil {
		return nil, err
	}
	p := strings.Split(*in.ShardIterator, "|")
	idx, _ := strconv.Atoi(p[1])
	s := f.shard(p[0])
	if idx < len(s.records) { // a page with nothing in it, though there are records: how many is remembered by position
		if s.empty == nil {
			s.empty = map[int]int{}
		}
		if _, ok := s.empty[idx]; !ok {
			s.empty[idx] = f.emptyPages
		}
		if s.empty[idx] > 0 {
			s.empty[idx]--
			return &dynamodbstreams.GetRecordsOutput{NextShardIterator: aws.String(fmt.Sprintf("%s|%d", s.id, idx))}, nil
		}
	}
	end := min(idx+int(aws.ToInt32(in.Limit)), len(s.records))
	out := &dynamodbstreams.GetRecordsOutput{Records: s.records[idx:end]}
	if !(s.closed && end >= len(s.records)) {
		out.NextShardIterator = aws.String(fmt.Sprintf("%s|%d", s.id, end))
	}
	return out, nil
}

type streamEnv struct {
	f    *fakeTable
	d    *Dynamo
	logs *bytes.Buffer
	now  time.Time
}

func newStreamEnv(t *testing.T) *streamEnv {
	t.Helper()
	e := &streamEnv{f: newFakeTable(), logs: &bytes.Buffer{}, now: time.Unix(1_800_000_000, 0)}
	e.d = NewDynamoFromClient(e.f, "dalibot", slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))).WithStreams(e.f, e.f)
	e.d.now = func() time.Time { return e.now }
	return e
}

func (e *streamEnv) ids(t *testing.T) []string {
	t.Helper()
	records, err := e.d.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range records {
		ids = append(ids, r.ID+"="+r.Prompt)
	}
	slices.Sort(ids)
	return ids
}

func (e *streamEnv) want(t *testing.T, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := e.ids(t); !slices.Equal(got, want) {
		t.Fatalf("records:\n got %v\nwant %v\nlog:\n%s", got, want, e.logs)
	}
}

func TestStreamFollowsChangesWithoutScanning(t *testing.T) {
	e := newStreamEnv(t)
	e.f.put("1:1", "one")
	e.f.put("1:2", "two")
	e.want(t, "1:1=one", "1:2=two")
	if e.f.scans != 1 || !e.f.consistent[0] {
		t.Fatalf("%d scans, consistent %v: the first read is one strongly consistent scan", e.f.scans, e.f.consistent)
	}
	e.f.put("1:3", "three")
	e.f.put("1:2", "two, edited")
	e.f.del("1:1")
	e.want(t, "1:2=two, edited", "1:3=three")
	e.want(t, "1:2=two, edited", "1:3=three") // nothing new
	if e.f.scans != 1 {
		t.Fatalf("the table was scanned %d times; the stream should have sufficed", e.f.scans)
	}
}

// The scan is eventually consistent when no stream is followed, and strongly consistent when one is.
func TestScanConsistencyFollowsTheStream(t *testing.T) {
	e := newStreamEnv(t)
	e.f.enabled = false
	e.f.put("1:1", "one")
	e.want(t, "1:1=one")
	if e.f.consistent[0] {
		t.Fatal("a table scanned at every refresh is read eventually consistent: half the cost")
	}
}

// What the stream holds from before the scan is replayed after it, and ends as the table is.
func TestStreamReplayConverges(t *testing.T) {
	e := newStreamEnv(t)
	e.f.put("1:1", "one")
	e.f.put("1:2", "two")
	e.f.del("1:2")
	e.f.put("1:2", "two again")
	e.f.put("1:3", "three")
	e.f.del("1:3")
	e.f.put("1:1", "one, edited")
	e.want(t, "1:1=one, edited", "1:2=two again")
	e.want(t, "1:1=one, edited", "1:2=two again") // after the replay
	if e.f.scans != 1 {
		t.Fatalf("%d scans", e.f.scans)
	}
}

// A scan that is behind (a replica that has not caught up) misses changes made before it. The stream holds them.
func TestStreamRepairsAScanThatIsBehind(t *testing.T) {
	e := newStreamEnv(t)
	e.f.put("1:1", "one")
	e.f.put("1:2", "two")
	e.f.mu.Lock()
	e.f.staleScan = map[string]map[string]types.AttributeValue{"1:1": rec("1:1", "one"), "1:2": rec("1:2", "two")}
	e.f.mu.Unlock()
	e.f.put("1:1", "one, edited")
	e.f.del("1:2")
	e.f.put("1:3", "three")
	e.want(t, "1:1=one", "1:2=two") // what the scan showed
	e.f.mu.Lock()
	e.f.staleScan = nil
	e.f.mu.Unlock()
	e.want(t, "1:1=one, edited", "1:3=three")
	if e.f.scans != 1 {
		t.Fatalf("%d scans", e.f.scans)
	}
}

func TestStreamKeepsWhatChangesDuringTheScan(t *testing.T) {
	e := newStreamEnv(t)
	e.f.put("1:1", "one")
	e.f.put("1:2", "two")
	// What follows happens after the scan has read the table, and before it answers.
	e.f.beforeScan = func() {
		e.f.mu.Lock()
		defer e.f.mu.Unlock()
		e.f.change(rec("1:3", "three"), "1:3", false)
		e.f.change(rec("1:1", "one, edited"), "1:1", false)
		e.f.change(nil, "1:2", true)
	}
	e.want(t, "1:1=one", "1:2=two") // the scan's picture
	e.f.beforeScan = nil
	e.want(t, "1:1=one, edited", "1:3=three")
	if e.f.scans != 1 {
		t.Fatalf("%d scans", e.f.scans)
	}
}

func TestStreamShardsAreReadParentFirst(t *testing.T) {
	e := newStreamEnv(t)
	// The child's name sorts before its parent's, so a read in name order would apply the changes backwards.
	e.f.shards[0].id = "shard-z"
	e.f.put("1:1", "one")
	e.want(t, "1:1=one")
	e.f.put("1:1", "old, in the parent")
	e.f.split("shard-a")
	e.f.put("1:1", "new, in the child")
	e.f.put("1:2", "two")
	e.want(t, "1:1=new, in the child", "1:2=two")
	e.f.split("shard-b")
	e.f.put("1:3", "three")
	e.want(t, "1:1=new, in the child", "1:2=two", "1:3=three")
	if e.f.scans != 1 {
		t.Fatalf("%d scans", e.f.scans)
	}
	// A finished shard that the stream has trimmed away is forgotten, not an error.
	e.f.shards[0].hidden = true
	e.f.put("1:4", "four")
	e.want(t, "1:1=new, in the child", "1:2=two", "1:3=three", "1:4=four")
	if e.f.scans != 1 || len(e.d.stream.shards) != 2 {
		t.Fatalf("%d scans, %d shards known", e.f.scans, len(e.d.stream.shards))
	}
}

func TestStreamJoinsLaterShards(t *testing.T) {
	e := newStreamEnv(t)
	e.f.put("1:1", "one")
	e.want(t, "1:1=one")
	e.f.split("shard-1") // splits, and a change lands in the child, before the gallery has looked
	e.f.put("1:2", "two")
	e.want(t, "1:1=one", "1:2=two")
	if e.f.scans != 1 {
		t.Fatalf("%d scans", e.f.scans)
	}
}

// Empty pages come before records, and a long backlog takes several pages.
func TestStreamReadsThroughEmptyPagesAndLongBacklogs(t *testing.T) {
	e := newStreamEnv(t)
	e.f.put("1:0", "start")
	e.want(t, "1:0=start")
	e.f.emptyPages = emptyPages - 1
	for i := range 2500 {
		e.f.put(fmt.Sprintf("2:%d", i), "x")
	}
	if got := len(e.ids(t)); got != 2501 {
		t.Fatalf("%d records after a backlog of 2500 changes over several pages", got)
	}
	// More empty pages than are read in one go: the rest is read at the next refresh, nothing is lost.
	e.f.emptyPages = emptyPages + 2
	e.f.put("3:1", "late")
	var seen bool
	for range 4 {
		if slices.Contains(e.ids(t), "3:1=late") {
			seen = true
			break
		}
	}
	if !seen || e.f.scans != 1 {
		t.Fatalf("seen %v after %d scans", seen, e.f.scans)
	}
}

func TestStreamNeedsAStream(t *testing.T) {
	denied := &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "no"}
	for name, set := range map[string]func(*fakeTable){
		"no stream":     func(f *fakeTable) { f.enabled = false },
		"wrong view":    func(f *fakeTable) { f.view = types.StreamViewTypeKeysOnly },
		"no describe":   func(f *fakeTable) { f.fail["DescribeTable"] = denied },
		"no streams":    func(f *fakeTable) { f.fail["DescribeStream"] = denied },
		"stream is off": func(f *fakeTable) { f.status = stypes.StreamStatusDisabled },
	} {
		t.Run(name, func(t *testing.T) {
			e := newStreamEnv(t)
			set(e.f)
			e.f.put("1:1", "one")
			e.want(t, "1:1=one")
			e.f.put("1:2", "two")
			e.want(t, "1:1=one", "1:2=two")
			if e.f.scans != 2 || e.d.stream != nil {
				t.Fatalf("%d scans, stream %v: the table must be scanned at every read", e.f.scans, e.d.stream)
			}
			if name != "stream is off" { // a stream that is off is looked at again at once: it may be a moment
				if e.f.describes != 1 {
					t.Fatalf("looked for a stream %d times in a row", e.f.describes)
				}
				if n := strings.Count(e.logs.String(), "reading the whole table at every refresh"); n != 1 {
					t.Fatalf("said %d times that the table is scanned:\n%s", n, e.logs)
				}
			}
		})
	}
}

// Not knowing yet (the network, throttling) is not the same as knowing there is none.
func TestStreamTransientLookupFailuresAreRetriedAtOnce(t *testing.T) {
	e := newStreamEnv(t)
	e.f.fail["DescribeTable"] = errors.New("connection reset, request id 123")
	e.f.put("1:1", "one")
	e.want(t, "1:1=one")
	e.want(t, "1:1=one")
	if e.f.describes != 2 || e.d.stream != nil {
		t.Fatalf("%d lookups", e.f.describes)
	}
	delete(e.f.fail, "DescribeTable")
	e.want(t, "1:1=one")
	if e.d.stream == nil {
		t.Fatalf("the stream was not found once it could be looked for:\n%s", e.logs)
	}
	if len(e.d.noStream) != 0 {
		t.Fatalf("remembers %v", e.d.noStream)
	}
}

func TestStreamIsFoundWhenItAppears(t *testing.T) {
	e := newStreamEnv(t)
	e.f.enabled = false
	e.f.put("1:1", "one")
	e.want(t, "1:1=one")
	e.f.enabled = true
	e.want(t, "1:1=one")
	if e.d.stream != nil {
		t.Fatal("looked for a stream again within the hour")
	}
	e.now = e.now.Add(attachEvery)
	e.want(t, "1:1=one")
	if e.d.stream == nil {
		t.Fatalf("the stream was not found:\n%s", e.logs)
	}
	e.f.put("1:2", "two")
	scans := e.f.scans
	e.want(t, "1:1=one", "1:2=two")
	if e.f.scans != scans {
		t.Fatal("scanned although the stream was being followed")
	}
}

// Throttling and the like: keep everything, say so, try again.
func TestStreamTransientErrorsKeepTheState(t *testing.T) {
	for _, op := range []string{"GetRecords", "DescribeStream", "GetShardIterator"} {
		t.Run(op, func(t *testing.T) {
			e := newStreamEnv(t)
			e.f.put("1:1", "one")
			e.want(t, "1:1=one")
			e.f.failing(op, errors.New("ThrottlingException"))
			e.f.put("1:2", "two")
			for range maxFailures - 1 {
				if _, err := e.d.Records(context.Background()); err == nil {
					t.Fatal("a failed read returned no error")
				}
			}
			if e.f.scans != 1 {
				t.Fatalf("%d scans: throttling is no reason to scan", e.f.scans)
			}
			e.f.failing(op, nil)
			e.want(t, "1:1=one", "1:2=two")
			if e.f.scans != 1 {
				t.Fatalf("%d scans after it passed:\n%s", e.f.scans, e.logs)
			}
			// The count starts again after a success.
			e.f.failing(op, errors.New("ThrottlingException"))
			for range maxFailures - 1 {
				_, _ = e.d.Records(context.Background())
			}
			e.f.failing(op, nil)
			e.want(t, "1:1=one", "1:2=two")
			if e.f.scans != 1 {
				t.Fatalf("%d scans", e.f.scans)
			}
		})
	}
}

func TestStreamFailingForGoodMeansScanningAgain(t *testing.T) {
	e := newStreamEnv(t)
	e.f.put("1:1", "one")
	e.want(t, "1:1=one")
	e.f.failing("GetRecords", errors.New("boom"))
	e.f.put("1:2", "two")
	for range maxFailures - 1 {
		_, _ = e.d.Records(context.Background())
	}
	e.want(t, "1:1=one", "1:2=two") // the third failure in a row
	if e.f.scans != 2 || !strings.Contains(e.logs.String(), "following the table's stream failed") {
		t.Fatalf("%d scans\n%s", e.f.scans, e.logs)
	}
	// It joined the stream again at once.
	e.f.failing("GetRecords", nil)
	e.f.put("1:3", "three")
	e.want(t, "1:1=one", "1:2=two", "1:3=three")
	if e.f.scans != 2 {
		t.Fatalf("%d scans", e.f.scans)
	}
}

func TestStreamFatalErrorsScanAtOnceAndFlappingIsLeftAlone(t *testing.T) {
	e := newStreamEnv(t)
	e.f.put("1:1", "one")
	e.want(t, "1:1=one")
	e.f.failing("GetShardIterator", &stypes.TrimmedDataAccessException{})
	e.f.put("1:2", "two")
	e.want(t, "1:1=one", "1:2=two")
	if e.f.scans != 2 || e.d.stream == nil {
		t.Fatalf("%d scans, stream %v: rescan and rejoin at once\n%s", e.f.scans, e.d.stream, e.logs)
	}
	// It fails again, soon after: the stream is not joined again for an hour, and the table is scanned each time.
	e.f.put("1:3", "three")
	e.want(t, "1:1=one", "1:2=two", "1:3=three")
	if e.f.scans != 3 || e.d.stream != nil {
		t.Fatalf("%d scans, stream %v", e.f.scans, e.d.stream)
	}
	e.want(t, "1:1=one", "1:2=two", "1:3=three")
	if e.f.scans != 4 {
		t.Fatalf("%d scans", e.f.scans)
	}
	e.f.failing("GetShardIterator", nil)
	e.now = e.now.Add(attachEvery)
	e.want(t, "1:1=one", "1:2=two", "1:3=three")
	if e.d.stream == nil {
		t.Fatalf("not joined again after an hour:\n%s", e.logs)
	}
}

func TestStreamRefreshingRarelyIsFine(t *testing.T) {
	e := newStreamEnv(t)
	e.f.put("1:1", "one")
	e.want(t, "1:1=one")
	for i := range 3 {
		e.now = e.now.Add(time.Hour) // far beyond any iterator's life
		e.f.put(fmt.Sprintf("1:%d", i+2), "x")
		e.want(t, append([]string{"1:1=one"}, func() []string {
			var w []string
			for j := range i + 1 {
				w = append(w, fmt.Sprintf("1:%d=x", j+2))
			}
			return w
		}()...)...)
	}
	if e.f.scans != 1 {
		t.Fatalf("%d scans", e.f.scans)
	}
}

func TestStreamLosingAShardMeansScanning(t *testing.T) {
	e := newStreamEnv(t)
	e.f.put("1:1", "one")
	e.want(t, "1:1=one")
	e.f.shards[0].hidden = true // gone before it was read to its end
	e.f.shards = append(e.f.shards, &fakeShard{id: "shard-new"})
	e.f.shards[0].closed = true
	e.f.put("1:2", "two")
	e.want(t, "1:1=one", "1:2=two")
	if e.f.scans != 2 {
		t.Fatalf("%d scans\n%s", e.f.scans, e.logs)
	}
}

func TestStreamIsScannedAgainEverySixHours(t *testing.T) {
	e := newStreamEnv(t)
	e.f.put("1:1", "one")
	e.want(t, "1:1=one")
	e.now = e.now.Add(resyncEvery - time.Minute)
	e.want(t, "1:1=one")
	if e.f.scans != 1 {
		t.Fatalf("%d scans", e.f.scans)
	}
	// A change the stream never carried (it should not happen) is found by the rescan.
	e.f.mu.Lock()
	e.f.items["1:9"] = rec("1:9", "silent")
	e.f.mu.Unlock()
	e.want(t, "1:1=one")
	e.now = e.now.Add(2 * time.Minute)
	e.want(t, "1:1=one", "1:9=silent")
	if e.f.scans != 2 || e.d.stream == nil {
		t.Fatalf("%d scans, stream %v", e.f.scans, e.d.stream)
	}
	if strings.Contains(e.logs.String(), "failed") {
		t.Fatalf("a planned rescan is not a failure:\n%s", e.logs)
	}
}

func TestStreamFailedScanRetriesTheStreamAtOnce(t *testing.T) {
	e := newStreamEnv(t)
	e.f.failing("Scan", errors.New("boom"))
	if _, err := e.d.Records(context.Background()); err == nil {
		t.Fatal("a failed scan returned no error")
	}
	if e.d.stream != nil {
		t.Fatal("a stream was kept although the table was never read")
	}
	e.f.failing("Scan", nil)
	e.f.put("1:1", "one")
	e.want(t, "1:1=one") // no hour's wait to look for the stream
	if e.d.stream == nil {
		t.Fatalf("no stream:\n%s", e.logs)
	}
}

func TestStreamCancelledReadKeepsTheState(t *testing.T) {
	e := newStreamEnv(t)
	e.f.put("1:1", "one")
	e.want(t, "1:1=one")
	e.f.failing("GetRecords", context.Canceled)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 2 * maxFailures {
		if _, err := e.d.Records(ctx); err == nil {
			t.Fatal("no error for a cancelled read")
		}
	}
	e.f.failing("GetRecords", nil)
	e.f.put("1:2", "two")
	e.want(t, "1:1=one", "1:2=two")
	if e.f.scans != 1 {
		t.Fatalf("%d scans: a cancelled read says nothing about the stream", e.f.scans)
	}
}

func TestStreamBadItemsAreNotShown(t *testing.T) {
	e := newStreamEnv(t)
	e.f.put("1:1", "one")
	e.want(t, "1:1=one")
	e.f.mu.Lock()
	e.f.change(map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "1:1"}, "ttl": &types.AttributeValueMemberS{Value: "not a number"}}, "1:1", false)
	e.f.mu.Unlock()
	e.want(t)
}

func TestFromStreamConvertsEveryKind(t *testing.T) {
	in := map[string]stypes.AttributeValue{
		"s": &stypes.AttributeValueMemberS{Value: "x"}, "n": &stypes.AttributeValueMemberN{Value: "1"},
		"b": &stypes.AttributeValueMemberB{Value: []byte{1}}, "t": &stypes.AttributeValueMemberBOOL{Value: true},
		"z": &stypes.AttributeValueMemberNULL{Value: true}, "ss": &stypes.AttributeValueMemberSS{Value: []string{"a"}},
		"ns": &stypes.AttributeValueMemberNS{Value: []string{"1"}}, "bs": &stypes.AttributeValueMemberBS{Value: [][]byte{{1}}},
		"l": &stypes.AttributeValueMemberL{Value: []stypes.AttributeValue{&stypes.AttributeValueMemberS{Value: "y"}}},
		"m": &stypes.AttributeValueMemberM{Value: map[string]stypes.AttributeValue{"k": &stypes.AttributeValueMemberN{Value: "2"}}},
	}
	out := fromStream(in)
	if len(out) != len(in) {
		t.Fatalf("%d of %d", len(out), len(in))
	}
	if m := out["m"].(*types.AttributeValueMemberM).Value["k"].(*types.AttributeValueMemberN); m.Value != "2" {
		t.Fatal("nested map")
	}
	if l := out["l"].(*types.AttributeValueMemberL).Value; len(l) != 1 || l[0].(*types.AttributeValueMemberS).Value != "y" {
		t.Fatal("list")
	}
}
