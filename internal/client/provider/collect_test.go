package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jssblck/akari/internal/client/upload"
	"github.com/jssblck/akari/internal/config"
)

// recorder stands in for the server. It records what a collection asked for and
// what it sent, so a test can assert on the resume point without a live akari.
type recorder struct {
	watermark    time.Time
	watermarkErr error
	sentProvider string
	sentAccount  string
	sent         []upload.ProviderUsageEvent
	calls        int
}

func (r *recorder) ProviderUsageWatermark(context.Context, string, string) (time.Time, error) {
	return r.watermark, r.watermarkErr
}

func (r *recorder) SendProviderUsage(_ context.Context, provider, account string, events []upload.ProviderUsageEvent) (int, error) {
	r.calls++
	r.sentProvider, r.sentAccount = provider, account
	r.sent = append(r.sent, events...)
	return len(events), nil
}

// A machine with no Cursor credential is the common case across a fleet, and it is
// a skip rather than a failure: the account's usage is collected by whichever
// machine can see it, and every other machine keeps syncing transcripts normally.
func TestCollectSkipsWithoutACredential(t *testing.T) {
	rec := &recorder{}
	results := Collect(context.Background(), config.Client{}, rec, t.TempDir(), func(string) string { return "" })

	if len(results) != 1 || results[0].Provider != "cursor" {
		t.Fatalf("results %+v, want one cursor result", results)
	}
	r := results[0]
	if !r.Skipped || r.Err != nil {
		t.Errorf("result skipped=%v err=%v, want a clean skip", r.Skipped, r.Err)
	}
	if rec.calls != 0 {
		t.Error("a machine with no credential still called the server")
	}
}

// Turning the collection off must stop it before it touches the local Cursor
// install at all, not merely discard what it read.
func TestCollectHonorsTheOffSwitch(t *testing.T) {
	cfg := config.Client{Cursor: config.CursorProvider{Disabled: true, Cookie: "WorkosCursorSessionToken=a%3A%3Ab"}}
	results := Collect(context.Background(), cfg, &recorder{}, t.TempDir(), func(string) string { return "" })
	if !results[0].Skipped {
		t.Errorf("collection ran with cursor disabled: %+v", results[0])
	}
}

// A watermark that cannot be read must stop the collection. Treating the failure as
// "no watermark" would silently re-page the account's whole history on every
// transient server error.
func TestCollectStopsWhenTheResumePointIsUnreadable(t *testing.T) {
	token := testToken(t)
	cfg := config.Client{Cursor: config.CursorProvider{Cookie: "WorkosCursorSessionToken=acct%3A%3A" + token}}
	rec := &recorder{watermarkErr: errors.New("server down")}

	results := Collect(context.Background(), cfg, rec, t.TempDir(), func(string) string { return "" })
	if results[0].Err == nil {
		t.Fatal("an unreadable watermark produced no error")
	}
	if rec.calls != 0 {
		t.Error("the collection sent events despite not knowing where to resume")
	}
}

// CursorProvider's switch is spelled negatively so an existing config file with no
// [cursor] table keeps collecting, which is what someone who installed akari to see
// their usage wants.
func TestCursorCollectionIsOnByDefault(t *testing.T) {
	if !(config.CursorProvider{}).Enabled() {
		t.Error("the zero CursorProvider is disabled; an existing config file would silently stop collecting")
	}
}

// testToken builds a JWT-shaped access token far enough from expiry to be used.
func testToken(t *testing.T) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"sub": "workos|acct", "exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatalf("encode claims: %v", err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

// A shutdown mid-collection is not a collection failure. Every other shutdown path
// in the client exits quietly, so reporting the context error would turn an
// ordinary Ctrl-C into a non-zero sync exit and a logged watch error. Nothing is
// lost either: the pass is idempotent and resumes from the server's watermark.
func TestCollectReportsAnInterruptAsASkip(t *testing.T) {
	token := testToken(t)
	cfg := config.Client{Cursor: config.CursorProvider{Cookie: "WorkosCursorSessionToken=acct%3A%3A" + token}}
	rec := &recorder{watermarkErr: context.Canceled}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := Collect(ctx, cfg, rec, t.TempDir(), func(string) string { return "" })

	r := results[0]
	if r.Err != nil {
		t.Errorf("an interrupted collection reported err=%v, want a quiet skip", r.Err)
	}
	if !r.Skipped {
		t.Error("an interrupted collection was not reported as skipped")
	}
}
