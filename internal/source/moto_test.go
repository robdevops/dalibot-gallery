package source

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
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

	records, err := reader.Records(ctx)
	if err != nil {
		t.Fatalf("scan with the projection failed (reserved words must use placeholders): %v", err)
	}
	if len(records) != 16+extra {
		t.Fatalf("read %d records, want %d", len(records), 16+extra)
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
	if snap.Len() != 19+extra {
		t.Fatalf("%d tiles, want %d", snap.Len(), 19+extra)
	}
	all := pageAll(t, snap, "", 60)
	if len(all) != snap.Len() || all[0].ID != group+":101" || all[len(all)-1].ID != fmt.Sprintf("-1009000000001:%d", 1000+extra-1) {
		t.Errorf("paged %d tiles, first %s, last %s", len(all), all[0].ID, all[len(all)-1].ID)
	}

	// The allowlist, end to end.
	opt.Chats = []string{"777"}
	st = NewStore(reader, opt, nil)
	if err := st.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := st.Snapshot().Len(); got != extra/3 {
		t.Errorf("chat 777: %d tiles, want %d", got, extra/3)
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
