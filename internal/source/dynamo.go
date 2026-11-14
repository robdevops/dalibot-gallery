package source

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodbstreams"
)

// Every attribute name goes through a placeholder: ttl and current are DynamoDB reserved words.
const projection = "#id, #p, #c, #cur, #img, #ttl, #cr"

var projectionNames = map[string]string{
	"#id":  "id",
	"#p":   "prompt",
	"#c":   "command",
	"#cur": "current",
	"#img": "images",
	"#ttl": "ttl",
	"#cr":  "created",
}

// ScanAPI is the one DynamoDB operation the reader uses.
type ScanAPI = dynamodb.ScanAPIClient

// DynamoOptions configure the DynamoDB reader.
type DynamoOptions struct {
	Table  string // required
	Region string // required
	// Endpoint overrides the service endpoint, for moto or DynamoDB Local.
	// When empty the SDK's own rules apply, including AWS_ENDPOINT_URL_DYNAMODB and AWS_ENDPOINT_URL_DYNAMODB_STREAMS.
	Endpoint string
	// Logger receives a warning for items that cannot be decoded. May be nil.
	Logger *slog.Logger
}

// Dynamo reads the bot's DynamoDB table: all of it with Scan, then, if the table has a
// stream, only the changes from the stream (dynamo_stream.go). It never writes.
type Dynamo struct {
	api   ScanAPI
	table string
	log   *slog.Logger

	tableAPI TableAPI   // nil: no look for a stream
	streams  StreamsAPI // nil: no look for a stream
	now      func() time.Time

	mu        sync.Mutex // Records is not called twice at once, but a stream's state must never be shared
	items     map[string]Record
	stream    *stream   // non-nil: items is kept up to date from it
	scannedAt time.Time // when items was last read from the whole table
	triedAt   time.Time // when a stream was last looked for (zero: look at the next read)
	droppedAt time.Time // when a stream was last given up
	noStream  map[string]bool
}

// NewDynamo builds a reader that uses the standard AWS credential chain.
// It makes no request: the first Scan happens in Records.
func NewDynamo(ctx context.Context, opt DynamoOptions) (*Dynamo, error) {
	if opt.Table == "" || opt.Region == "" {
		return nil, fmt.Errorf("source: DynamoDB table and region are required")
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(opt.Region))
	if err != nil {
		return nil, fmt.Errorf("source: loading AWS configuration: %w", err)
	}
	client := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		if opt.Endpoint != "" {
			o.BaseEndpoint = aws.String(opt.Endpoint)
		}
	})
	streams := dynamodbstreams.NewFromConfig(cfg, func(o *dynamodbstreams.Options) {
		if opt.Endpoint != "" {
			o.BaseEndpoint = aws.String(opt.Endpoint)
		}
	})
	return NewDynamoFromClient(client, opt.Table, opt.Logger).WithStreams(client, streams), nil
}

// NewDynamoFromClient builds a reader on an existing client (or a fake, in tests). log may be nil.
func NewDynamoFromClient(api ScanAPI, table string, log *slog.Logger) *Dynamo {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Dynamo{api: api, table: table, log: log, now: time.Now, noStream: map[string]bool{}}
}

// item is the stored shape. "base" is deliberately not decoded.
type item struct {
	ID      string               `dynamodbav:"id"`
	Prompt  string               `dynamodbav:"prompt"`
	Command string               `dynamodbav:"command"`
	Current string               `dynamodbav:"current"`
	TTL     int64                `dynamodbav:"ttl"`
	Images  map[string]itemImage `dynamodbav:"images"`
}

type itemImage struct {
	FileID  string `dynamodbav:"file_id"`
	Caption string `dynamodbav:"caption"`
}

// Records returns every record of the table. While the table's stream is being followed
// that is the picture kept up to date from it; otherwise, and every resyncEvery, it is read
// from the whole table. The records are shared with the reader's picture: do not change them.
func (d *Dynamo) Records(ctx context.Context) ([]Record, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stream != nil && d.now().Sub(d.scannedAt) < resyncEvery {
		err := d.follow(ctx)
		if err == nil {
			d.stream.failures = 0
			return d.current(), nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		d.stream.failures++
		if !errors.Is(err, errFatal) && d.stream.failures < maxFailures {
			// May pass (throttling, the network): keep the picture and the places, and try at the next refresh.
			return nil, fmt.Errorf("source: following the stream of DynamoDB table %q: %w", d.table, err)
		}
		d.log.Warn("following the table's stream failed; reading the whole table again", "error", err)
		d.drop()
	} else if d.stream != nil {
		d.drop() // resyncEvery has passed: not a failure
		d.triedAt = time.Time{}
	}
	var st *stream
	if d.streams != nil && d.tableAPI != nil && d.now().Sub(d.triedAt) >= attachEvery {
		d.triedAt = d.now()
		st = d.attach(ctx)
	}
	records, err := d.scan(ctx, st != nil)
	if err != nil {
		d.triedAt = time.Time{} // the scan is what failed, not the stream
		return nil, err
	}
	d.scannedAt = d.now()
	if st != nil {
		d.stream, d.items = st, make(map[string]Record, len(records))
		for _, r := range records {
			d.items[r.ID] = r
		}
	}
	return records, nil
}

// drop throws the stream away after a failure. The next read scans the table and joins the stream
// again, unless the stream was dropped shortly before too: then it is left alone for attachEvery.
func (d *Dynamo) drop() {
	d.stream = nil
	if d.now().Sub(d.droppedAt) < flapping {
		d.triedAt = d.now()
	} else {
		d.triedAt = time.Time{}
	}
	d.droppedAt = d.now()
}

// scan reads the whole table. An item that does not have the bot's shape is
// skipped (and counted in one warning), so one bad item cannot empty the gallery.
// consistent asks for a strongly consistent read, which costs twice as much: used when a stream follows it.
func (d *Dynamo) scan(ctx context.Context, consistent bool) ([]Record, error) {
	p := dynamodb.NewScanPaginator(d.api, &dynamodb.ScanInput{
		ConsistentRead:           aws.Bool(consistent),
		TableName:                aws.String(d.table),
		ProjectionExpression:     aws.String(projection),
		ExpressionAttributeNames: projectionNames,
	})
	var records []Record
	skipped := 0
	for p.HasMorePages() {
		out, err := p.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("source: scanning DynamoDB table %q: %w", d.table, err)
		}
		for _, av := range out.Items {
			r, err := decodeItem(av)
			if err != nil {
				skipped++
				continue
			}
			records = append(records, r)
		}
	}
	if skipped > 0 {
		d.log.Warn("skipped items that do not have the bot's shape", "count", skipped)
	}
	return records, nil
}

// createdOf reads the optional "created" attribute. It is only a hint for ordering, so a
// value that is missing or not a whole number counts as absent instead of spoiling the item.
func createdOf(av map[string]types.AttributeValue) int64 {
	n, ok := av["created"].(*types.AttributeValueMemberN)
	if !ok {
		return 0
	}
	v, err := strconv.ParseInt(n.Value, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func decodeItem(av map[string]types.AttributeValue) (Record, error) {
	var it item
	if err := attributevalue.UnmarshalMap(av, &it); err != nil {
		return Record{}, err
	}
	if it.ID == "" {
		return Record{}, fmt.Errorf("item has no id")
	}
	r := Record{ID: it.ID, Prompt: it.Prompt, Command: it.Command, Current: it.Current, TTL: it.TTL, Created: createdOf(av)}
	if len(it.Images) > 0 {
		r.Images = make(map[string]RawImage, len(it.Images))
		for k, img := range it.Images {
			r.Images[k] = RawImage(img)
		}
	}
	return r, nil
}
