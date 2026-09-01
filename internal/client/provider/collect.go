// Package provider collects account-wide usage from coding agent vendors whose
// transcripts do not record what a session cost.
//
// It sits beside transcript syncing rather than inside it. A transcript sync walks
// files on this machine and pushes their bytes; a provider collection asks one
// vendor account what it billed, which is neither per-file nor per-machine. Keeping
// them separate is what lets a machine with no Cursor transcripts still report the
// account's spend, and a machine with no Cursor credential still sync its
// transcripts.
package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jssblck/akari/internal/client/provider/cursor"
	"github.com/jssblck/akari/internal/client/upload"
	"github.com/jssblck/akari/internal/config"
)

// Result is the outcome of collecting one vendor account.
type Result struct {
	Provider string
	// Skipped with a reason covers the ordinary case of nothing to do: no Cursor
	// install, a signed-out app, an expired token, or collection turned off. None of
	// those is a failure, so callers report them as skips rather than errors.
	Skipped  bool
	Reason   string
	Fetched  int
	Inserted int
	Err      error
}

// uploader is the server-side half of a collection. *upload.Client implements it;
// the interface is the seam a test substitutes a recorder through.
type uploader interface {
	ProviderUsageWatermark(ctx context.Context, provider, accountID string) (time.Time, error)
	SendProviderUsage(ctx context.Context, provider, accountID string, events []upload.ProviderUsageEvent) (int, error)
}

// watermarkOverlap is how far before the server's newest stored event a collection
// restarts. Re-fetching already-stored events costs nothing (the server dedups on
// the event key) and losing one costs a permanently missing charge, so the window
// is deliberately far wider than the boundary instant it has to cover.
//
// The watermark is the newest occurred_at akari holds, but the feed is a billing
// pipeline: it can publish an event whose occurred_at is hours old. If a collection
// runs in between, the watermark has already advanced past that instant and a
// window that started at it would never see the late row again, losing the charge
// silently and permanently. Two days is the bound on how late a publication akari
// still catches, and it costs about one extra page on an ordinary account.
const watermarkOverlap = 48 * time.Hour

// Collect runs every enabled vendor collection for this client and returns one
// result per vendor.
//
// Vendors are collected independently: one vendor's missing credential or failed
// fetch never suppresses another's. Today Cursor is the only one.
func Collect(ctx context.Context, cfg config.Client, up uploader, home string, env func(string) string) []Result {
	return []Result{collectCursor(ctx, cfg, up, home, env)}
}

func collectCursor(ctx context.Context, cfg config.Client, up uploader, home string, env func(string) string) Result {
	r := collect(ctx, cfg, up, home, env)
	// A shutdown mid-collection is not a failure of the collection. Every other
	// shutdown path in the client exits quietly, and reporting "context canceled" here
	// would make an ordinary Ctrl-C a non-zero sync exit and a logged watch error.
	// Nothing is lost: the pass is idempotent and resumes from the server's watermark.
	if r.Err != nil && ctx.Err() != nil {
		return Result{Provider: r.Provider, Skipped: true, Reason: "interrupted", Fetched: r.Fetched}
	}
	return r
}

func collect(ctx context.Context, cfg config.Client, up uploader, home string, env func(string) string) Result {
	r := Result{Provider: "cursor"}
	if !cfg.Cursor.Enabled() {
		r.Skipped, r.Reason = true, "cursor usage collection is off"
		return r
	}

	session, err := cursor.ResolveSession(cfg.Cursor.Cookie, home, env)
	if errors.Is(err, cursor.ErrNoSession) {
		r.Skipped, r.Reason = true, "no signed-in cursor.com session on this machine"
		return r
	}
	if err != nil {
		r.Err = err
		return r
	}

	// Ask the server where to resume before fetching anything: a failed watermark
	// read must not silently become a full-history re-collection.
	watermark, err := up.ProviderUsageWatermark(ctx, "cursor", session.AccountID)
	if err != nil {
		r.Err = fmt.Errorf("read cursor usage watermark: %w", err)
		return r
	}
	var since time.Time
	if !watermark.IsZero() {
		since = watermark.Add(-watermarkOverlap)
	}

	events, err := (&cursor.Fetcher{}).Fetch(ctx, session, since)
	if errors.Is(err, cursor.ErrNoSession) {
		// The credential resolved locally but cursor.com rejected it, which is what a
		// revoked or superseded session looks like. Nothing to collect, nothing wrong
		// with this machine.
		r.Skipped, r.Reason = true, "cursor.com rejected the local session"
		return r
	}
	if err != nil {
		r.Err = err
		return r
	}
	r.Fetched = len(events)
	if len(events) == 0 {
		return r
	}

	out := make([]upload.ProviderUsageEvent, 0, len(events))
	for _, e := range events {
		out = append(out, upload.ProviderUsageEvent{
			EventKey:       e.EventKey,
			ConversationID: e.ConversationID,
			Model:          e.Model,
			Input:          e.Input,
			Output:         e.Output,
			CacheWrite:     e.CacheWrite,
			CacheRead:      e.CacheRead,
			CostUSD:        e.CostUSD,
			CostKnown:      e.CostKnown,
			OccurredAt:     e.OccurredAt,
		})
	}
	inserted, err := up.SendProviderUsage(ctx, "cursor", session.AccountID, out)
	r.Inserted = inserted
	r.Err = err
	return r
}
