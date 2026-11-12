package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const fixturePath = "../../testdata/dynamodb/items.json"

// fixture is testdata/dynamodb/items.json: items in DynamoDB's own JSON form.
type fixture struct {
	Now       int64                        `json:"now"`
	StoreDays int                          `json:"store_days"`
	Items     []map[string]json.RawMessage `json:"items"`
}

func (f fixture) options() Options {
	return Options{StoreDays: f.StoreDays, DefaultCommand: "dream", Now: func() time.Time { return time.Unix(f.Now, 0) }}
}

func loadFixture(t testing.TB) (fixture, []map[string]types.AttributeValue) {
	t.Helper()
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	items := make([]map[string]types.AttributeValue, len(f.Items))
	for i, it := range f.Items {
		m, err := avMap(it)
		if err != nil {
			t.Fatalf("fixture item %d: %v", i, err)
		}
		items[i] = m
	}
	return f, items
}

func avMap(m map[string]json.RawMessage) (map[string]types.AttributeValue, error) {
	out := make(map[string]types.AttributeValue, len(m))
	for k, raw := range m {
		v, err := av(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = v
	}
	return out, nil
}

// av converts DynamoDB JSON ({"S": "x"}, {"N": "1"}, {"M": {...}}, {"NULL": true}, ...) to an AttributeValue.
func av(raw json.RawMessage) (types.AttributeValue, error) {
	var one map[string]json.RawMessage
	if err := json.Unmarshal(raw, &one); err != nil || len(one) != 1 {
		return nil, fmt.Errorf("not a DynamoDB attribute: %s", raw)
	}
	for typ, v := range one {
		switch typ {
		case "S":
			var s string
			err := json.Unmarshal(v, &s)
			return &types.AttributeValueMemberS{Value: s}, err
		case "N":
			var s string
			err := json.Unmarshal(v, &s)
			return &types.AttributeValueMemberN{Value: s}, err
		case "BOOL":
			var b bool
			err := json.Unmarshal(v, &b)
			return &types.AttributeValueMemberBOOL{Value: b}, err
		case "NULL":
			return &types.AttributeValueMemberNULL{Value: true}, nil
		case "M":
			var m map[string]json.RawMessage
			if err := json.Unmarshal(v, &m); err != nil {
				return nil, err
			}
			out, err := avMap(m)
			return &types.AttributeValueMemberM{Value: out}, err
		case "L":
			var l []json.RawMessage
			if err := json.Unmarshal(v, &l); err != nil {
				return nil, err
			}
			out := make([]types.AttributeValue, len(l))
			for i, e := range l {
				x, err := av(e)
				if err != nil {
					return nil, err
				}
				out[i] = x
			}
			return &types.AttributeValueMemberL{Value: out}, nil
		}
	}
	return nil, fmt.Errorf("unsupported attribute type: %s", raw)
}

// fakeScan serves items in pages, like DynamoDB, and records every request.
type fakeScan struct {
	mu       sync.Mutex
	items    []map[string]types.AttributeValue
	pageSize int
	err      error // returned by every call when set
	failAt   int   // when > 0, the call with this number (1-based) fails
	inputs   []*dynamodb.ScanInput
}

func (f *fakeScan) Scan(_ context.Context, in *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, in)
	if f.err != nil {
		return nil, f.err
	}
	if f.failAt > 0 && len(f.inputs) == f.failAt {
		return nil, fmt.Errorf("fake scan failure on call %d", f.failAt)
	}
	start := 0
	if k, ok := in.ExclusiveStartKey["n"].(*types.AttributeValueMemberN); ok {
		start, _ = strconv.Atoi(k.Value)
	}
	size := f.pageSize
	if size <= 0 {
		size = len(f.items)
	}
	end := min(start+size, len(f.items))
	out := &dynamodb.ScanOutput{Items: f.items[start:end], Count: int32(end - start)}
	if end < len(f.items) {
		out.LastEvaluatedKey = map[string]types.AttributeValue{"n": &types.AttributeValueMemberN{Value: strconv.Itoa(end)}}
	}
	return out, nil
}

// fixtureRecords decodes the fixture through the real reader over a fake Scan.
func fixtureRecords(t testing.TB) (fixture, []Record) {
	t.Helper()
	f, items := loadFixture(t)
	d := NewDynamoFromClient(&fakeScan{items: items, pageSize: 4}, "dalibot", nil)
	records, err := d.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return f, records
}

func recordByID(records []Record, id string) (Record, bool) {
	for _, r := range records {
		if r.ID == id {
			return r, true
		}
	}
	return Record{}, false
}

func sortedKeys(m map[string]RawImage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
