package source

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// motoServer is where the moto server is installed in the build environment.
// MOTO_SERVER overrides it; the test is skipped when neither exists.
const motoServer = "/opt/moto/bin/moto_server"

// startMoto runs a moto server on a free local port and returns its URL.
// The seeding below is the only place anything is ever written, and it writes to moto, never to AWS.
func startMoto(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("MOTO_SERVER")
	if bin == "" {
		bin = motoServer
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("moto server not available (%s): skipping the DynamoDB reader test", bin)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.Command(bin, "-H", "127.0.0.1", "-p", fmt.Sprint(port))
	if err := cmd.Start(); err != nil {
		t.Skipf("moto server did not start: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(30 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			c.Close()
			return url
		}
		if time.Now().After(deadline) {
			t.Fatalf("moto server did not listen on %s", url)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestDynamoReaderAgainstMoto(t *testing.T) {
	endpoint := startMoto(t)
	// Fake static credentials and no shared config: nothing here can reach a real account.
	t.Setenv("AWS_ACCESS_KEY_ID", "testing")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "testing")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", os.DevNull)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", os.DevNull)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const table = "dalibot-test"
	reader, err := NewDynamo(ctx, DynamoOptions{Table: table, Region: "us-east-2", Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	client := reader.api.(*dynamodb.Client)

	// The table as doc/serverless.md creates it: hash key id (S), on demand, no indexes.
	if _, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(table),
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
		BillingMode:          types.BillingModePayPerRequest,
	}); err != nil {
		t.Fatalf("creating the table in moto: %v", err)
	}

	// An empty table scans to nothing.
	if records, err := reader.Records(ctx); err != nil || len(records) != 0 {
		t.Fatalf("empty table: %d records, %v", len(records), err)
	}

	f, items := loadFixture(t)
	for _, it := range items {
		if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(table), Item: it}); err != nil {
			t.Fatalf("seeding moto: %v", err)
		}
	}
	// Enough extra items, with long prompts, that the Scan spans several 1 MB pages.
	const extra = 700
	long := make([]byte, 4000)
	for i := range long {
		long[i] = 'a' + byte(i%26)
	}
	n := func(v int64) types.AttributeValue { return &types.AttributeValueMemberN{Value: fmt.Sprint(v)} }
	s := func(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
	for i := range extra {
		chat := []string{"-1009000000001", "-1009000000002", "777"}[i%3]
		item := map[string]types.AttributeValue{
			"id": s(fmt.Sprintf("%s:%d", chat, 1000+i)), "prompt": s(string(long)), "command": s("dream"), "current": s("_"),
			"ttl": n(f.Now - 20000 - int64(i) + 90*86400),
			"images": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
				"_": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
					"file_id": s(fmt.Sprintf("moto-file-%d", i)), "caption": s("extra"), "base": &types.AttributeValueMemberNULL{Value: true}}},
			}},
		}
		if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(table), Item: item}); err != nil {
			t.Fatalf("seeding moto: %v", err)
		}
	}

	// A picture the bot imported from a forwarded message: a "created" date and an id named after the file.
	imported := map[string]types.AttributeValue{
		"id": s("777:fAQADmoto_-1"), "prompt": s("an old picture"), "command": s("dream"), "current": s("_"),
		"ttl": n(f.Now + 90*86400), "created": n(f.Now - 500*86400),
		"images": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
			"_": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
				"file_id": s("moto-imported"), "caption": s("an old picture"), "base": &types.AttributeValueMemberNULL{Value: true}}},
		}},
	}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(table), Item: imported}); err != nil {
		t.Fatalf("seeding moto: %v", err)
	}

	records, err := reader.Records(ctx)
	if err != nil {
		t.Fatalf("scan with the projection failed (reserved words must use placeholders): %v", err)
	}
	if len(records) != 17+extra {
		t.Fatalf("read %d records, want %d", len(records), 17+extra)
	}
	if got, ok := recordByID(records, "777:fAQADmoto_-1"); !ok || got.Created != f.Now-500*86400 || got.TTL != f.Now+90*86400 {
		t.Errorf("imported record: %+v (found %v)", got, ok)
	}
	// Same result as the fake-backed decode, record for record.
	_, want := fixtureRecords(t)
	for _, w := range want {
		got, ok := recordByID(records, w.ID)
		if !ok || fmt.Sprint(got) != fmt.Sprint(w) {
			t.Errorf("record %s: got %+v, want %+v", w.ID, got, w)
		}
	}

	// Through the Store: the listing rules apply and paging covers everything once.
	opt := f.options()
	st := NewStore(reader, opt, nil)
	if err := st.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	snap := st.Snapshot()
	if snap.Len() != 20+extra {
		t.Fatalf("%d tiles, want %d", snap.Len(), 20+extra)
	}
	all := pageAll(t, snap, "", 60)
	if len(all) != snap.Len() || all[0].ID != group+":101" || all[len(all)-1].ID != "777:fAQADmoto_-1" { // the imported picture is the oldest, by its created date
		t.Errorf("paged %d tiles, first %s, last %s", len(all), all[0].ID, all[len(all)-1].ID)
	}

	// The allowlist, end to end.
	opt.Chats = []string{"777"}
	st = NewStore(reader, opt, nil)
	if err := st.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := st.Snapshot().Len(); got != extra/3+1 { // the extra items of that chat, and the imported one
		t.Errorf("chat 777: %d tiles, want %d", got, extra/3+1)
	}

	// A missing table is an error, and a Store keeps its snapshot through it.
	missing, err := NewDynamo(ctx, DynamoOptions{Table: "no-such-table", Region: "us-east-2", Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missing.Records(ctx); err == nil {
		t.Error("scanning a missing table should fail")
	}
}

// The reader follows a real stream (moto's) after one scan, and sees what is added, changed and removed.
func TestDynamoStreamAgainstMoto(t *testing.T) {
	endpoint := startMoto(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "testing")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "testing")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", os.DevNull)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", os.DevNull)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const table = "dalibot-stream"
	var logs bytes.Buffer
	reader, err := NewDynamo(ctx, DynamoOptions{Table: table, Region: "us-east-2", Endpoint: endpoint, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	client := reader.api.(*dynamodb.Client)
	if _, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(table),
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
		BillingMode:          types.BillingModePayPerRequest,
		StreamSpecification:  &types.StreamSpecification{StreamEnabled: aws.Bool(true), StreamViewType: types.StreamViewTypeNewImage},
	}); err != nil {
		t.Fatalf("creating the table in moto: %v", err)
	}
	put := func(id, prompt string) {
		t.Helper()
		if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(table), Item: rec(id, prompt)}); err != nil {
			t.Fatal(err)
		}
	}
	read := func() []string {
		t.Helper()
		records, err := reader.Records(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range records {
			out = append(out, r.ID+"="+r.Prompt)
		}
		slices.Sort(out)
		return out
	}
	expect := func(want ...string) {
		t.Helper()
		slices.Sort(want)
		if got := read(); !slices.Equal(got, want) {
			t.Fatalf("records %v, want %v", got, want)
		}
	}

	put("1:1", "one")
	put("1:2", "two")
	expect("1:1=one", "1:2=two")
	if reader.stream == nil {
		t.Fatal("the reader is not following moto's stream")
	}
	put("1:3", "three")
	put("1:1", "one, edited")
	if _, err := client.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(table), Key: map[string]types.AttributeValue{"id": &types.AttributeValueMemberS{Value: "1:2"}}}); err != nil {
		t.Fatal(err)
	}
	expect("1:1=one, edited", "1:3=three")
	expect("1:1=one, edited", "1:3=three")
	if reader.stream == nil || reader.stream.failures != 0 {
		t.Fatal("the reader stopped following the stream")
	}
	// Following, not scanning: the log has no mention of falling back, and the rows came from the stream.
	for _, bad := range []string{"failed", "whole table", "could not"} {
		if strings.Contains(logs.String(), bad) {
			t.Fatalf("the log says %q:\n%s", bad, &logs)
		}
	}
	if !strings.Contains(logs.String(), "following the table's stream") {
		t.Fatalf("the log does not say the stream is followed:\n%s", &logs)
	}
}
