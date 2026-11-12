// Package devstack builds a complete local stand-in for the gallery's two
// upstreams, for the integration test, the browser tests and local
// development: a moto DynamoDB server seeded with items in the bot's real
// shapes, and a fake Telegram Bot API that serves sample JPEGs.
//
// Nothing here can reach AWS or Telegram: the seeder refuses any endpoint that
// is not on the loopback interface, and the fake Telegram server implements
// only getFile and the file download. The gallery binary does not import this
// package.
package devstack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Fixed values of the local stack. None of them is a real credential.
const (
	Table        = "dalibot"
	Region       = "us-east-2"
	StoreDays    = 90
	GalleryToken = "dev-gallery-token-0123456789"
	BotToken     = "123456:DEV-fake_bot_token_abcdef"
	// MotoServer is where the moto server is installed in the build environment; $MOTO_SERVER overrides it.
	MotoServer = "/opt/moto/bin/moto_server"
	// FileIDPrefix starts every seeded file_id, so a leak of any of them is easy to detect.
	FileIDPrefix = "AgACAgUAAxkBAAIDEVSTACK"
	// deadMark inside a file_id makes the fake Telegram answer 400 for it.
	deadMark = "DEADFILE"
)

// Chats are the three chats the seeded items belong to: two groups and one private chat.
var Chats = []string{"-1001111111111", "-1002222222222", "424242"}

// ErrNoMoto means the moto server binary is not installed.
var ErrNoMoto = errors.New("moto server is not installed")

// Moto is a running moto server.
type Moto struct {
	URL string
	cmd *exec.Cmd
}

// FreePort returns a TCP port that was free on 127.0.0.1 a moment ago.
func FreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// WaitListening waits until something accepts connections on addr.
func WaitListening(ctx context.Context, addr string) error {
	for {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("nothing is listening on %s: %w", addr, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// StartMoto runs a moto server on a free loopback port. It returns ErrNoMoto
// (wrapped) when the binary is missing.
func StartMoto(ctx context.Context) (*Moto, error) {
	bin := os.Getenv("MOTO_SERVER")
	if bin == "" {
		bin = MotoServer
	}
	if _, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("%w (%s)", ErrNoMoto, bin)
	}
	port, err := FreePort()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(bin, "-H", "127.0.0.1", "-p", fmt.Sprint(port))
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting moto: %w", err)
	}
	m := &Moto{URL: fmt.Sprintf("http://127.0.0.1:%d", port), cmd: cmd}
	wctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := WaitListening(wctx, fmt.Sprintf("127.0.0.1:%d", port)); err != nil {
		m.Stop()
		return nil, err
	}
	return m, nil
}

// Stop kills the moto server.
func (m *Moto) Stop() {
	if m != nil && m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		_, _ = m.cmd.Process.Wait()
	}
}

// AWSEnv is the environment that points an AWS SDK at the local endpoint with
// fake static credentials and no shared configuration.
func AWSEnv(endpoint string) []string {
	return []string{
		"AWS_ENDPOINT_URL_DYNAMODB=" + endpoint,
		"AWS_ACCESS_KEY_ID=testing",
		"AWS_SECRET_ACCESS_KEY=testing",
		"AWS_SESSION_TOKEN=",
		"AWS_PROFILE=",
		"AWS_REGION=" + Region,
		"AWS_CONFIG_FILE=" + os.DevNull,
		"AWS_SHARED_CREDENTIALS_FILE=" + os.DevNull,
		"AWS_EC2_METADATA_DISABLED=true",
	}
}

// client returns a DynamoDB client for a loopback endpoint only.
func client(endpoint string) (*dynamodb.Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(u.Hostname()); u.Scheme != "http" || ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("devstack writes only to a local moto server, not to %q", u.Host)
	}
	return dynamodb.New(dynamodb.Options{
		Region:       Region,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider("testing", "testing", ""),
	}), nil
}

// Seeded describes what Seed wrote.
type Seeded struct {
	Items   int      // items written, including the ones the gallery must not show
	Records int      // records the gallery should list
	Tiles   int      // tiles the gallery should list
	FileIDs []string // every file_id written, shown or not: none may ever reach a browser
	Dead    []string // file_ids for which the fake Telegram answers 400 (their tiles show the placeholder)
	Hidden  []string // file_ids of items that must not be listed (expired, probe, malformed id, no ttl)
}

type seeder struct {
	n     int
	out   Seeded
	items []map[string]types.AttributeValue
}

func s(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
func n(v int64) types.AttributeValue  { return &types.AttributeValueMemberN{Value: fmt.Sprint(v)} }

// fileID returns a new file_id shaped like Telegram's.
func (sd *seeder) fileID(mark string) string {
	sd.n++
	id := fmt.Sprintf("%s%s%04d-Xy_%032x", FileIDPrefix, mark, sd.n, uint64(sd.n)*0x9E3779B97F4A7C15)
	sd.out.FileIDs = append(sd.out.FileIDs, id)
	return id
}

// image is one entry of an item's images map. base: "null", "missing" or a style key.
func image(fileID, caption, base string) types.AttributeValue {
	m := map[string]types.AttributeValue{"file_id": s(fileID), "caption": s(caption)}
	switch base {
	case "missing":
	case "null":
		m["base"] = &types.AttributeValueMemberNULL{Value: true}
	default:
		m["base"] = s(base)
	}
	return &types.AttributeValueMemberM{Value: m}
}

func (sd *seeder) add(item map[string]types.AttributeValue, records, tiles int) {
	sd.items = append(sd.items, item)
	sd.out.Items++
	sd.out.Records += records
	sd.out.Tiles += tiles
}

var prompts = []string{
	"a red fox in snow", "lighthouse in a storm", "tiny robot watering a bonsai", "a city made of books",
	"an owl wearing a scarf", "tea party on the moon", "a whale above the clouds", "a neon-lit ramen stall at night",
	"mountain cabin, first light", "a dragon asleep on a pile of socks", "a paper boat on a wide river", "still life with lemons",
}

// build makes about 150 items across the three chats, in the shapes the bot
// really writes, including the awkward ones.
func build(now time.Time, storeDays int) *seeder {
	sd := &seeder{}
	span := int64(storeDays) * 86400
	ttl := func(age int64) types.AttributeValue { return n(now.Unix() - age + span) }
	images := func(m map[string]types.AttributeValue) types.AttributeValue {
		return &types.AttributeValueMemberM{Value: m}
	}

	// 140 ordinary records, each a few seconds older than the one before.
	var carried map[string]types.AttributeValue
	for i := 0; i < 140; i++ {
		prompt := prompts[i%len(prompts)]
		base := []string{"null", "missing", "null"}[i%3]
		imgs := map[string]types.AttributeValue{
			"_": image(sd.fileID(""), fmt.Sprintf("%s, highly detailed (%d)", prompt, i), base),
		}
		current, command := "_", "dream"
		if i%4 == 1 {
			imgs["monet"] = image(sd.fileID(""), prompt+", in the style of Claude Monet", "_")
			current = "monet"
		}
		if i%10 == 3 {
			for _, k := range []string{"dali", "8bit", "bw"} {
				imgs[k] = image(sd.fileID(""), prompt+" as "+k, "_")
			}
			command = "imagine"
		}
		item := map[string]types.AttributeValue{
			"id":     s(fmt.Sprintf("%s:%d", Chats[i%3], 1000+i)),
			"prompt": s(prompt), "command": s(command), "current": s(current),
			"images": images(imgs), "ttl": ttl(600 + int64(i)*37),
		}
		if i == 5 {
			// Carried over below: the copy is newer, so this record loses every image and is not listed.
			carried = imgs
			sd.add(item, 0, 0)
			continue
		}
		sd.add(item, 1, len(imgs))
	}

	chat := Chats[0]
	// Created but not filled yet: an empty images map.
	sd.add(map[string]types.AttributeValue{
		"id": s(chat + ":5001"), "prompt": s("still drawing"), "command": s("dream"), "current": s("_"),
		"images": images(map[string]types.AttributeValue{}), "ttl": ttl(5),
	}, 0, 0)
	// current names a style that is not there: falls back to "_".
	sd.add(map[string]types.AttributeValue{
		"id": s(chat + ":5002"), "prompt": s("a harbour at dusk"), "command": s("dream"), "current": s("vangogh"),
		"images": images(map[string]types.AttributeValue{
			"_":       image(sd.fileID(""), "a harbour at dusk, soft light", "null"),
			"picasso": image(sd.fileID(""), "a harbour at dusk, cubist", "_"),
		}), "ttl": ttl(50),
	}, 1, 2)
	// current missing and no "_": falls back to the first image in display order.
	sd.add(map[string]types.AttributeValue{
		"id": s(Chats[1] + ":5003"), "prompt": s("a cat on a windowsill"), "command": s("dream"),
		"images": images(map[string]types.AttributeValue{
			"sketch": image(sd.fileID(""), "a cat on a windowsill, pencil sketch", "missing"),
			"anime":  image(sd.fileID(""), "a cat on a windowsill, anime", "missing"),
		}), "ttl": ttl(90),
	}, 1, 2)
	// A style key the gallery does not know: shown with a title-cased label.
	sd.add(map[string]types.AttributeValue{
		"id": s(Chats[2] + ":5004"), "prompt": s("a street in the rain"), "command": s("dream"), "current": s("neon_noir"),
		"images": images(map[string]types.AttributeValue{
			"_":         image(sd.fileID(""), "a street in the rain", "null"),
			"neon_noir": image(sd.fileID(""), "a street in the rain, neon noir", "_"),
		}), "ttl": ttl(130),
	}, 1, 2)
	// carry_over: the images of record 5 copied to a new message, plus one new style.
	over := map[string]types.AttributeValue{"tattoo": image(sd.fileID(""), "as a tattoo", "_")}
	for k, v := range carried {
		over[k] = v
	}
	sd.add(map[string]types.AttributeValue{
		"id": s(Chats[2] + ":5005"), "prompt": s(prompts[5]), "command": s("dream"), "current": s("tattoo"),
		"images": images(over), "ttl": ttl(170),
	}, 1, len(over))
	// Expired but not yet deleted by DynamoDB.
	hidden := func(mark string) string {
		id := sd.fileID(mark)
		sd.out.Hidden = append(sd.out.Hidden, id)
		return id
	}
	sd.add(map[string]types.AttributeValue{
		"id": s(chat + ":5006"), "prompt": s("expired"), "command": s("dream"), "current": s("_"),
		"images": images(map[string]types.AttributeValue{"_": image(hidden("EXPIRED"), "expired", "null")}),
		"ttl":    n(now.Unix() - 3600),
	}, 0, 0)
	// The bot's read probe.
	sd.add(map[string]types.AttributeValue{
		"id": s("check:check"), "prompt": s("probe"), "command": s("dream"), "current": s("_"),
		"images": images(map[string]types.AttributeValue{"_": image(hidden("PROBE"), "probe", "null")}),
		"ttl":    ttl(1),
	}, 0, 0)
	// No ttl at all, and an id that is not "<chat>:<message>".
	sd.add(map[string]types.AttributeValue{
		"id": s(chat + ":5007"), "prompt": s("no ttl"), "command": s("dream"), "current": s("_"),
		"images": images(map[string]types.AttributeValue{"_": image(hidden("NOTTL"), "no ttl", "null")}),
	}, 0, 0)
	sd.add(map[string]types.AttributeValue{
		"id": s("malformed"), "prompt": s("bad id"), "command": s("dream"), "current": s("_"),
		"images": images(map[string]types.AttributeValue{"_": image(hidden("BADID"), "bad id", "null")}),
		"ttl":    ttl(2),
	}, 0, 0)
	// No prompt, no command, an empty caption.
	sd.add(map[string]types.AttributeValue{
		"id":     s(Chats[1] + ":5008"),
		"images": images(map[string]types.AttributeValue{"_": image(sd.fileID(""), "", "null")}),
		"ttl":    ttl(210),
	}, 1, 1)
	// Text that must be escaped everywhere it is shown.
	sd.add(map[string]types.AttributeValue{
		"id": s(Chats[1] + ":5009"), "prompt": s(`<script>alert("x")</script> & 'quotes' — ünïcödé 🦊`), "command": s("dream"), "current": s("_"),
		"images": images(map[string]types.AttributeValue{
			"_": image(sd.fileID(""), `<img src=x onerror=alert(1)> "caption" & more`, "null"),
		}), "ttl": ttl(250),
	}, 1, 1)
	// A file Telegram no longer serves (the bot left the group): its tile is the placeholder.
	// It is the oldest record, so it is the last tile.
	dead := sd.fileID(deadMark)
	sd.out.Dead = append(sd.out.Dead, dead)
	sd.add(map[string]types.AttributeValue{
		"id": s(Chats[1] + ":5010"), "prompt": s("a picture that is gone"), "command": s("dream"), "current": s("_"),
		"images": images(map[string]types.AttributeValue{"_": image(dead, "a picture that is gone", "null")}),
		"ttl":    ttl(100000),
	}, 1, 1)
	return sd
}

// Seed creates the table as the bot's documentation does (hash key "id",
// on demand, no indexes) and fills it. endpoint must be a loopback moto server.
func Seed(ctx context.Context, endpoint, table string, now time.Time, storeDays int) (*Seeded, error) {
	c, err := client(endpoint)
	if err != nil {
		return nil, err
	}
	if _, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(table),
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
		BillingMode:          types.BillingModePayPerRequest,
	}); err != nil {
		return nil, fmt.Errorf("creating the table in moto: %w", err)
	}
	sd := build(now, storeDays)
	for _, item := range sd.items {
		if _, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(table), Item: item}); err != nil {
			return nil, fmt.Errorf("seeding moto: %w", err)
		}
	}
	return &sd.out, nil
}

var (
	addMu  sync.Mutex
	addSeq int
)

// AddNew writes count new single-image records dated now, as if the bot had
// just drawn them, and returns their file_ids. It is how the tests make the
// "N new" pill appear.
func AddNew(ctx context.Context, endpoint, table string, now time.Time, storeDays, count int) ([]string, error) {
	c, err := client(endpoint)
	if err != nil {
		return nil, err
	}
	var ids []string
	for i := 0; i < count; i++ {
		addMu.Lock()
		addSeq++
		seq := addSeq
		addMu.Unlock()
		fileID := fmt.Sprintf("%sNEW%04d-Qz_%032x", FileIDPrefix, seq, uint64(seq)*0xC2B2AE3D27D4EB4F)
		prompt := prompts[seq%len(prompts)]
		item := map[string]types.AttributeValue{
			"id":     s(fmt.Sprintf("%s:%d", Chats[seq%3], 900000+seq)),
			"prompt": s(prompt), "command": s("dream"), "current": s("_"),
			"images": &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{
				"_": image(fileID, prompt+", freshly drawn", "null"),
			}},
			"ttl": n(now.Unix() + int64(seq) + int64(storeDays)*86400),
		}
		if _, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(table), Item: item}); err != nil {
			return ids, fmt.Errorf("adding an item to moto: %w", err)
		}
		ids = append(ids, fileID)
	}
	return ids, nil
}

// LoadImages reads every *.jpg in dir, in name order.
func LoadImages(dir string) ([][]byte, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.jpg"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out [][]byte
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		out = append(out, data)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no .jpg files in %s", dir)
	}
	return out, nil
}

// Telegram is a fake Bot API server. It answers exactly two requests,
//
//	POST /bot<token>/getFile
//	GET  /file/bot<token>/<file_path>
//
// and records every other request as forbidden: the gallery must never make one.
type Telegram struct {
	token  string
	images [][]byte
	// Delay is added to every answer, to imitate the network. Set it before serving.
	Delay time.Duration

	mu        sync.Mutex
	forbidden []string
	getFile   int
	downloads int
	paths     map[string]int // file_path -> image index
}

// NewTelegram returns a fake that serves images (chosen by a hash of the file_id).
func NewTelegram(token string, images [][]byte) *Telegram {
	return &Telegram{token: token, images: images, paths: map[string]int{}}
}

// Forbidden lists the requests that were neither getFile nor a file download ("METHOD path").
func (t *Telegram) Forbidden() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.forbidden...)
}

// Calls returns how many getFile requests and downloads were answered.
func (t *Telegram) Calls() (getFile, downloads int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.getFile, t.downloads
}

func (t *Telegram) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if t.Delay > 0 {
		time.Sleep(t.Delay)
	}
	filePrefix := "/file/bot" + t.token + "/"
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/bot"+t.token+"/getFile" && r.URL.RawQuery == "":
		var body struct {
			FileID string `json:"file_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body.FileID == "" {
			apiError(w, 400, "Bad Request: file_id is missing")
			return
		}
		t.mu.Lock()
		t.getFile++
		t.mu.Unlock()
		if strings.Contains(body.FileID, deadMark) {
			apiError(w, 400, "Bad Request: wrong file_id or the file is temporarily unavailable")
			return
		}
		h := fnv.New32a()
		h.Write([]byte(body.FileID))
		sum := h.Sum32()
		idx := int(sum % uint32(len(t.images)))
		path := fmt.Sprintf("photos/file_%d.jpg", sum)
		t.mu.Lock()
		t.paths[path] = idx
		t.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{
			"file_id": body.FileID, "file_unique_id": fmt.Sprintf("AQAD%d", sum),
			"file_size": len(t.images[idx]), "file_path": path,
		}})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, filePrefix) && r.URL.RawQuery == "":
		t.mu.Lock()
		idx, ok := t.paths[strings.TrimPrefix(r.URL.Path, filePrefix)]
		if ok {
			t.downloads++
		}
		t.mu.Unlock()
		if !ok {
			apiError(w, 404, "Not Found")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprint(len(t.images[idx])))
		_, _ = w.Write(t.images[idx])
	default:
		t.mu.Lock()
		// The path may hold the token; record the method and what follows the token only.
		t.forbidden = append(t.forbidden, r.Method+" "+strings.ReplaceAll(r.URL.Path, t.token, "<token>"))
		t.mu.Unlock()
		apiError(w, 404, "Not Found")
	}
}

func apiError(w http.ResponseWriter, code int, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": code, "description": description})
}
