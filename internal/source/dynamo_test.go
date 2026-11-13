package source

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const group = "-1001234567890"

func TestScanRequest(t *testing.T) {
	_, items := loadFixture(t)
	fake := &fakeScan{items: items, pageSize: 5}
	var logs bytes.Buffer
	d := NewDynamoFromClient(fake, "my-table", slog.New(slog.NewTextHandler(&logs, nil)))
	records, err := d.Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.inputs) != 4 { // 17 items in pages of 5
		t.Fatalf("made %d Scan calls, want 4", len(fake.inputs))
	}
	wantNames := map[string]string{"#id": "id", "#p": "prompt", "#c": "command", "#cur": "current", "#img": "images", "#ttl": "ttl", "#cr": "created"}
	for i, in := range fake.inputs {
		if *in.TableName != "my-table" {
			t.Errorf("call %d: table %q", i, *in.TableName)
		}
		if in.ProjectionExpression == nil || *in.ProjectionExpression != "#id, #p, #c, #cur, #img, #ttl, #cr" {
			t.Errorf("call %d: projection %v", i, in.ProjectionExpression)
		}
		if !reflect.DeepEqual(in.ExpressionAttributeNames, wantNames) {
			t.Errorf("call %d: names %v", i, in.ExpressionAttributeNames)
		}
		if in.FilterExpression != nil || in.IndexName != nil {
			t.Errorf("call %d: unexpected filter or index", i)
		}
		if (i == 0) != (in.ExclusiveStartKey == nil) {
			t.Errorf("call %d: ExclusiveStartKey = %v", i, in.ExclusiveStartKey)
		}
	}
	// The reserved words must never appear outside a placeholder.
	for _, word := range []string{"ttl", "current"} {
		if strings.Contains(strings.ReplaceAll(projection, "#"+word, ""), word) {
			t.Errorf("projection uses reserved word %q directly", word)
		}
	}
	if len(records) != 16 { // 17 items, one malformed
		t.Errorf("decoded %d records, want 16", len(records))
	}
	if !strings.Contains(logs.String(), "count=1") || strings.Contains(logs.String(), "FIXTURE") {
		t.Errorf("expected one warning counting 1 skipped item and no file ids, got: %s", logs.String())
	}
}

func TestDecodeFixtureShapes(t *testing.T) {
	f, records := fixtureRecords(t)

	// A normal item: NULL base and string base are both ignored.
	r, ok := recordByID(records, group+":101")
	if !ok {
		t.Fatal("record 101 missing")
	}
	if r.Prompt != "a red fox in snow" || r.Command != "dream" || r.Current != "monet" || r.TTL != f.Now-1000+90*86400 {
		t.Errorf("record 101 = %+v", r)
	}
	if got := sortedKeys(r.Images); !reflect.DeepEqual(got, []string{"_", "monet"}) {
		t.Errorf("record 101 images = %v", got)
	}
	if img := r.Images["monet"]; !strings.Contains(img.FileID, "fox1") || !strings.HasSuffix(img.Caption, "Claude Monet") {
		t.Errorf("record 101 monet = %+v", img)
	}

	// Created but not filled yet: decodes, with no images.
	if r, ok := recordByID(records, group+":102"); !ok || len(r.Images) != 0 {
		t.Errorf("record 102 = %+v, %v", r, ok)
	}
	// A current key that is not in images decodes as stored.
	if r, _ := recordByID(records, "42:7"); r.Current != "dali" || len(r.Images) != 2 {
		t.Errorf("record 42:7 = %+v", r)
	}
	// Unknown style keys are kept.
	if r, _ := recordByID(records, group+":103"); !reflect.DeepEqual(sortedKeys(r.Images), []string{"_", "bw", "neon_noir", "vaporwave"}) {
		t.Errorf("record 103 images = %v", sortedKeys(r.Images))
	}
	// Missing ttl decodes as 0; the probe and expired items decode like any other.
	if r, ok := recordByID(records, "42:2"); !ok || r.TTL != 0 {
		t.Errorf("record 42:2 = %+v", r)
	}
	if _, ok := recordByID(records, ProbeID); !ok {
		t.Error("the reader should not filter; Build drops the probe")
	}
	if r, ok := recordByID(records, "42:1"); !ok || r.TTL != f.Now-1 {
		t.Errorf("record 42:1 = %+v", r)
	}
	// images of the wrong type: the item is skipped, the rest survive.
	if _, ok := recordByID(records, "42:3"); ok {
		t.Error("malformed item 42:3 should be skipped")
	}
	// An image entry without a file_id decodes with an empty one.
	if r, _ := recordByID(records, "42:13"); r.Images["_"].FileID != "" || r.Images["ega"].FileID == "" {
		t.Errorf("record 42:13 = %+v", r)
	}
}

func TestDecodeItemEdgeCases(t *testing.T) {
	s := func(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
	null := &types.AttributeValueMemberNULL{Value: true}
	// No id.
	if _, err := decodeItem(map[string]types.AttributeValue{"prompt": s("x")}); err == nil {
		t.Error("item without id should fail")
	}
	// NULL caption, NULL images, numeric ttl as a string type.
	r, err := decodeItem(map[string]types.AttributeValue{
		"id": s("1:2"), "images": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
			"_": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"file_id": s("F"), "caption": null, "base": null}},
		}},
		"ttl": &types.AttributeValueMemberN{Value: "1800000000"},
	})
	if err != nil || r.Images["_"].FileID != "F" || r.Images["_"].Caption != "" || r.TTL != 1800000000 || r.Command != "" {
		t.Errorf("decode = %+v, %v", r, err)
	}
	if r.Created != 0 {
		t.Errorf("Created = %d for an item without one", r.Created)
	}
	// A picture the bot imported from a forwarded message: its id ends in the file's name, and it has a created date.
	r, err = decodeItem(map[string]types.AttributeValue{
		"id": s("111:fAQADabc_-9"), "ttl": &types.AttributeValueMemberN{Value: "1800000000"},
		"created": &types.AttributeValueMemberN{Value: "1700000000"},
	})
	if err != nil || r.ID != "111:fAQADabc_-9" || r.Created != 1700000000 || r.TTL != 1800000000 {
		t.Errorf("imported item = %+v, %v", r, err)
	}
	if r, err := decodeItem(map[string]types.AttributeValue{"id": s("1:2"), "images": null}); err != nil || r.Images != nil {
		t.Errorf("NULL images = %+v, %v", r, err)
	}
	if _, err := decodeItem(map[string]types.AttributeValue{"id": s("1:2"), "ttl": s("soon")}); err == nil {
		t.Error("string ttl should fail to decode")
	}
}

func TestScanErrorIsReturned(t *testing.T) {
	_, items := loadFixture(t)
	boom := errors.New("AccessDeniedException: not authorized to perform dynamodb:Scan")
	d := NewDynamoFromClient(&fakeScan{err: boom}, "dalibot", nil)
	if _, err := d.Records(context.Background()); !errors.Is(err, boom) || !strings.Contains(err.Error(), `"dalibot"`) {
		t.Errorf("err = %v", err)
	}
	// A failure on a later page must not return a partial listing.
	d = NewDynamoFromClient(&fakeScan{items: items, pageSize: 4, failAt: 3}, "dalibot", nil)
	if records, err := d.Records(context.Background()); err == nil || records != nil {
		t.Errorf("partial scan returned %d records, err %v", len(records), err)
	}
}

func TestNewDynamoValidation(t *testing.T) {
	if _, err := NewDynamo(context.Background(), DynamoOptions{Region: "us-east-2"}); err == nil {
		t.Error("missing table should fail")
	}
	if _, err := NewDynamo(context.Background(), DynamoOptions{Table: "dalibot"}); err == nil {
		t.Error("missing region should fail")
	}
}
