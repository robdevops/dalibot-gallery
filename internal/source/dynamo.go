package source

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
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
	// When empty the SDK's own rules apply, including AWS_ENDPOINT_URL_DYNAMODB.
	Endpoint string
	// Logger receives a warning for items that cannot be decoded. May be nil.
	Logger *slog.Logger
}

// Dynamo reads the bot's DynamoDB table with Scan. It never writes.
type Dynamo struct {
	api   ScanAPI
	table string
	log   *slog.Logger
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
	return NewDynamoFromClient(client, opt.Table, opt.Logger), nil
}

// NewDynamoFromClient builds a reader on an existing client (or a fake, in tests). log may be nil.
func NewDynamoFromClient(api ScanAPI, table string, log *slog.Logger) *Dynamo {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Dynamo{api: api, table: table, log: log}
}

// item is the stored shape. "base" is deliberately not decoded.
type item struct {
	ID      string               `dynamodbav:"id"`
	Prompt  string               `dynamodbav:"prompt"`
	Command string               `dynamodbav:"command"`
	Current string               `dynamodbav:"current"`
	TTL     int64                `dynamodbav:"ttl"`
	Created int64                `dynamodbav:"created"`
	Images  map[string]itemImage `dynamodbav:"images"`
}

type itemImage struct {
	FileID  string `dynamodbav:"file_id"`
	Caption string `dynamodbav:"caption"`
}

// Records scans the whole table. An item that does not have the bot's shape is
// skipped (and counted in one warning), so one bad item cannot empty the gallery.
func (d *Dynamo) Records(ctx context.Context) ([]Record, error) {
	p := dynamodb.NewScanPaginator(d.api, &dynamodb.ScanInput{
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

func decodeItem(av map[string]types.AttributeValue) (Record, error) {
	var it item
	if err := attributevalue.UnmarshalMap(av, &it); err != nil {
		return Record{}, err
	}
	if it.ID == "" {
		return Record{}, fmt.Errorf("item has no id")
	}
	r := Record{ID: it.ID, Prompt: it.Prompt, Command: it.Command, Current: it.Current, TTL: it.TTL, Created: it.Created}
	if len(it.Images) > 0 {
		r.Images = make(map[string]RawImage, len(it.Images))
		for k, img := range it.Images {
			r.Images[k] = RawImage(img)
		}
	}
	return r, nil
}
