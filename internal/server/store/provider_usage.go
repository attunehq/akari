package store

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jssblck/akari/internal/pricing"
)

// sessionConversationID is the SQL expression for the vendor conversation a session
// belongs to: the last segment of its source id.
//
// A Cursor transcript records no id of its own, so the client derives the source id
// from the file's location (resolve.sourceIDFromName), and the conversation id is
// that path's trailing segment. The expression matches
// idx_sessions_cursor_conversation (migration 0060); keep the two in sync or the
// join falls back to a sequential scan of every session.
const sessionConversationID = `regexp_replace(source_session_id, '^.*/', '')`

// conversationIDOf is sessionConversationID in Go, for the announce path, which
// already holds the source id in memory.
func conversationIDOf(sourceID string) string {
	if i := strings.LastIndexByte(sourceID, '/'); i >= 0 {
		return sourceID[i+1:]
	}
	return sourceID
}

// ProviderUsageEvent is one billing event as a vendor's account API reported it.
// It is the ingest-side shape: the client fetches these, the server stores them
// verbatim, and the rebuild folds the ones that resolve to a session into that
// session's ledger.
//
// ConversationID is the vendor's own identifier for the conversation the event
// served. For Cursor it is the name of the transcript directory and file the CLI
// wrote, which is the last segment of the source id akari derives from that path,
// so resolution is an equality join rather than a heuristic (see
// sessionConversationID).
//
// EventKey is a client-derived stable identity. The feed exposes no event id, so
// idempotency (a re-fetch of an overlapping window, or a second machine collecting
// the same account-wide feed) rests on this key being a pure function of the
// event's own reported fields.
type ProviderUsageEvent struct {
	EventKey        string
	ConversationID  string
	Model           string
	Input           int
	Output          int
	CacheWrite      int
	CacheRead       int
	CostUSD         float64
	CostSource      CostSource
	ModelNamePublic bool
	OccurredAt      time.Time
}

// ProviderUsageResult reports what one RecordProviderUsage call changed: how many
// events were new, and how many sessions those new events made due for a rebuild.
// A collection that stores nothing marks nothing, so a re-fetched window costs no
// rebuild.
type ProviderUsageResult struct {
	Inserted       int
	SessionsMarked int
}

// RecordProviderUsage stores a batch of provider-reported usage events for one
// vendor account, resolves each to a session where one is known, and marks the
// sessions its newly stored events resolved to for a rebuild.
//
// provider names both the vendor and the akari agent whose sessions its events can
// attach to. The two are the same string by construction: the column's CHECK admits
// only vendors that are also parser.Agents entries, which is what lets a reported
// conversation id resolve to a session at all.
//
// Storing and resolving in one transaction is what keeps the ledger from double
// counting: usage_ledger's unattached arm reads exactly the rows with a NULL
// session_id, and the rebuild folds exactly the rows with a non-NULL one, so a row
// must never be visible in one state to the analytics read and the other to a
// concurrent rebuild.
//
// Re-ingesting an event already stored is a no-op rather than an error, so the
// client may re-fetch an overlapping window freely. It always does: the watermark it
// resumes from is an instant, and the feed is inclusive of it.
func (s *Store) RecordProviderUsage(ctx context.Context, userID int64, provider, accountID string, events []ProviderUsageEvent) (ProviderUsageResult, error) {
	var out ProviderUsageResult
	// Collapse repeats within the batch first. ON CONFLICT resolves a collision with
	// a committed row, but two rows sharing a key inside one INSERT still race the
	// same index entry, so the batch must be unique before it reaches the statement.
	cols := newProviderUsageColumns(len(events))
	seen := make(map[string]bool, len(events))
	for _, e := range events {
		key := sanitizeText(e.EventKey)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		cols.append(key, e)
	}
	if cols.len() == 0 {
		return out, nil
	}

	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// The conflict target is the account-scoped event identity. DO NOTHING rather
		// than DO UPDATE: an event's reported figures are immutable once billed, so a
		// second sighting carries no new information, and leaving the stored row
		// untouched keeps its session resolution and the rebuild it already triggered.
		//
		// The insert and the dirty mark are one statement so the mark can key off the
		// rows the insert actually wrote. Marking every session that merely holds
		// attached usage would re-flag sessions the rebuild already folded, and since
		// the rebuild clears the flag while the rows stay attached, every later
		// collection would set them again: a refold of the whole Cursor corpus once
		// per pass, forever. The flag has to mean "usage arrived that no rebuild has
		// seen", which is exactly the newly inserted rows.
		err := tx.QueryRow(ctx,
			`WITH inserted AS (
			   INSERT INTO provider_usage_events
			     (user_id, provider, account_id, event_key, conversation_id, session_id,
			      model, input_tokens, output_tokens, cache_write_tokens, cache_read_tokens,
			      cost_usd, cost_source, model_name_public, occurred_at)
			   SELECT $1, $2, $3, e.event_key, e.conversation_id,
			          -- One conversation can be ingested under two project slugs (the same
			          -- agent run observed from two worktrees), so this join is not unique.
			          -- The spend happened once, so it attaches to one session: the oldest,
			          -- which makes the choice deterministic across machines and repeat
			          -- collections rather than whichever row the planner happens to return.
			          (SELECT id FROM sessions
			            WHERE user_id = $1 AND agent = $2
			              AND `+sessionConversationID+` = e.conversation_id
			            ORDER BY id LIMIT 1),
			          e.model, e.input_tokens, e.output_tokens, e.cache_write_tokens,
			          e.cache_read_tokens, e.cost_usd, e.cost_source, e.model_name_public,
			          e.occurred_at
			     FROM unnest($4::text[], $5::text[], $6::text[], $7::int[], $8::int[],
			                 $9::int[], $10::int[], $11::double precision[], $12::text[],
			                 $13::boolean[], $14::timestamptz[])
			       AS e(event_key, conversation_id, model, input_tokens, output_tokens,
			            cache_write_tokens, cache_read_tokens, cost_usd, cost_source,
			            model_name_public, occurred_at)
			   ON CONFLICT (user_id, provider, account_id, event_key) DO NOTHING
			   RETURNING session_id
			 ), marked AS (
			   -- No NOT usage_dirty guard. A row that fails a predicate is not locked,
			   -- so guarding here would let this mark no-op against a rebuild that is
			   -- in flight and holding session_raw FOR UPDATE: the rebuild would clear
			   -- the flag without folding an event that committed after it read, and
			   -- nothing would re-mark it (the re-reported event hits ON CONFLICT DO
			   -- NOTHING and returns no row). Unguarded, the UPDATE always takes the
			   -- lock, so it serializes behind that rebuild and re-marks after it
			   -- commits. The set is already narrow: only sessions of rows this
			   -- statement actually inserted.
			   UPDATE session_raw sr
			      SET usage_dirty = true
			    WHERE sr.session_id IN (SELECT session_id FROM inserted WHERE session_id IS NOT NULL)
			   RETURNING 1
			 )
			 SELECT (SELECT count(*) FROM inserted), (SELECT count(*) FROM marked)`,
			userID, provider, accountID,
			cols.eventKey, cols.conversationID, cols.model, cols.input, cols.output,
			cols.cacheWrite, cols.cacheRead, cols.cost, cols.costSource,
			cols.modelPublic, cols.occurredAt).Scan(&out.Inserted, &out.SessionsMarked)
		if err != nil {
			return fmt.Errorf("insert provider usage events: %w", err)
		}

		// Then sweep any usage still unattached whose session is now visible, and
		// mark whatever that moves.
		//
		// This is a separate statement on purpose: READ COMMITTED gives it a fresh
		// snapshot, so it sees sessions the insert above could not. That closes the
		// interleaving where a collection and an announce miss each other, each
		// reading a snapshot that predates the other's commit: the insert resolves to
		// NULL because the session is not visible yet, the announce's claim of
		// still-unattached rows finds nothing because the event is not visible yet,
		// and neither rechecks. Without a sweep that spend would stay account-grain
		// forever, since a later collection re-reporting the event hits ON CONFLICT DO
		// NOTHING and never re-resolves it.
		//
		// Sweeping every unattached row rather than only this batch's makes the
		// attachment self-healing against any cause, not just that one race. It is a
		// hash join of one user's sessions against their unattached rows, once per
		// collection, and it normally moves nothing, so it marks nothing and costs no
		// rebuild.
		var sweptMarks int
		if err := tx.QueryRow(ctx,
			`WITH swept AS (
			   -- Same oldest-session rule as the insert. A join would take whichever
			   -- matching session the planner returned, so a conversation ingested under
			   -- two project slugs could land its swept rows on a different session than
			   -- its inserted ones and split one conversation's spend across both.
			   UPDATE provider_usage_events p
			      SET session_id = (SELECT s.id FROM sessions s
			                         WHERE s.user_id = $1 AND s.agent = $2
			                           AND `+sessionConversationID+` = p.conversation_id
			                         ORDER BY s.id LIMIT 1)
			    WHERE p.user_id = $1 AND p.provider = $2 AND p.session_id IS NULL
			      AND EXISTS (SELECT 1 FROM sessions s
			                   WHERE s.user_id = $1 AND s.agent = $2
			                     AND `+sessionConversationID+` = p.conversation_id)
			   RETURNING session_id
			 ), marked AS (
			   UPDATE session_raw sr
			      SET usage_dirty = true
			    WHERE sr.session_id IN (SELECT session_id FROM swept)
			   RETURNING 1
			 )
			 SELECT (SELECT count(*) FROM marked)`,
			userID, provider).Scan(&sweptMarks); err != nil {
			return fmt.Errorf("resolve unattached provider usage: %w", err)
		}
		out.SessionsMarked += sweptMarks
		return nil
	})
	if err != nil {
		return ProviderUsageResult{}, err
	}
	return out, nil
}

// providerUsageColumns is the batch transposed into one slice per column, which is
// what unnest takes. Building it explicitly keeps the whole batch to a single round
// trip; a per-event statement would be one round trip per billing request, and a
// first collection routinely carries thousands.
type providerUsageColumns struct {
	eventKey, conversationID, model, costSource []string
	input, output, cacheWrite, cacheRead        []int32
	cost                                        []float64
	modelPublic                                 []bool
	occurredAt                                  []time.Time
}

func newProviderUsageColumns(n int) *providerUsageColumns {
	return &providerUsageColumns{
		eventKey: make([]string, 0, n), conversationID: make([]string, 0, n),
		model: make([]string, 0, n), costSource: make([]string, 0, n),
		input: make([]int32, 0, n), output: make([]int32, 0, n),
		cacheWrite: make([]int32, 0, n), cacheRead: make([]int32, 0, n),
		cost: make([]float64, 0, n), modelPublic: make([]bool, 0, n),
		occurredAt: make([]time.Time, 0, n),
	}
}

func (c *providerUsageColumns) len() int { return len(c.eventKey) }

func (c *providerUsageColumns) append(key string, e ProviderUsageEvent) {
	c.eventKey = append(c.eventKey, key)
	c.conversationID = append(c.conversationID, sanitizeText(e.ConversationID))
	c.model = append(c.model, sanitizeText(e.Model))
	c.costSource = append(c.costSource, string(e.CostSource))
	c.input = append(c.input, tokenCount(e.Input))
	c.output = append(c.output, tokenCount(e.Output))
	c.cacheWrite = append(c.cacheWrite, tokenCount(e.CacheWrite))
	c.cacheRead = append(c.cacheRead, tokenCount(e.CacheRead))
	c.cost = append(c.cost, e.CostUSD)
	c.modelPublic = append(c.modelPublic, e.ModelNamePublic)
	c.occurredAt = append(c.occurredAt, e.OccurredAt)
}

// tokenCount narrows one reported token count to the INT column that stores it.
//
// The clamp is what makes the narrowing safe: a count above 2^31 would wrap to a
// negative and subtract from the session's totals and the fleet's, and nothing
// would ever repair it, because a stored event is immutable (ON CONFLICT DO
// NOTHING) and the watermark moves past its instant. Real feeds report millions at
// most, so this only ever fires on a vendor-side anomaly, which is exactly the case
// that must not corrupt a total silently.
func tokenCount(v int) int32 {
	return int32(min(max(v, 0), math.MaxInt32))
}

// ProviderUsageWatermark returns the newest event instant stored for one vendor
// account, or the zero time when none is stored.
//
// The client holds no cursor of its own; it asks for this and re-fetches from it,
// exactly as a transcript upload reconciles against the server's byte cursor. That
// keeps a fresh checkout, a second machine, and a reinstalled client all resuming
// from the same place rather than each re-paging the account's whole history.
func (s *Store) ProviderUsageWatermark(ctx context.Context, userID int64, provider, accountID string) (time.Time, error) {
	var at *time.Time
	if err := s.Pool.QueryRow(ctx,
		`SELECT max(occurred_at) FROM provider_usage_events
		  WHERE user_id = $1 AND provider = $2 AND account_id = $3`,
		userID, provider, accountID).Scan(&at); err != nil {
		return time.Time{}, fmt.Errorf("read provider usage watermark: %w", err)
	}
	if at == nil {
		return time.Time{}, nil
	}
	return *at, nil
}

// resolveProviderUsageForSessionTx attaches the provider usage already stored for a
// session's conversation id, and reports whether any row moved.
//
// It runs from the announce path, which is the other half of the ordering problem:
// events can land before their session exists, and a session can be announced long
// after the spend it did was billed. Whichever arrives second performs the join.
func resolveProviderUsageForSessionTx(ctx context.Context, tx pgx.Tx, sessionID int64, userID int64, agent, sourceID string) (bool, error) {
	// Only agents akari collects vendor usage for can have events waiting, and the
	// provider column admits exactly those names, so anything else short-circuits
	// rather than running a doomed UPDATE on every announce in the fleet.
	if agent != "cursor" {
		return false, nil
	}
	// Only rows that are still unattached move, so a conversation ingested a second
	// time under a different project slug never takes the events from the session
	// already holding them.
	//
	// The row goes to the oldest session for the conversation, not unconditionally to
	// the announcing one, which is the same rule the insert and the sweep apply. The
	// announcing session is usually that oldest one, but it is not when a conversation
	// is being announced a second time under another slug while some of its rows are
	// still stranded: attaching those to the announcer would split one conversation's
	// spend across two sessions depending on which path happened to rescue each row.
	tag, err := tx.Exec(ctx,
		`UPDATE provider_usage_events p
		    SET session_id = (SELECT s.id FROM sessions s
		                       WHERE s.user_id = $1 AND s.agent = $2
		                         AND `+sessionConversationID+` = p.conversation_id
		                       ORDER BY s.id LIMIT 1)
		  WHERE p.user_id = $1 AND p.provider = $2 AND p.conversation_id = $3
		    AND p.session_id IS NULL`,
		userID, agent, conversationIDOf(sourceID))
	if err != nil {
		return false, fmt.Errorf("resolve provider usage for session %d: %w", sessionID, err)
	}
	return tag.RowsAffected() > 0, nil
}

// providerUsageForSessionTx reads a session's provider usage as ledger rows the
// rebuild's fold can consume.
//
// The rows carry no message ordinal on purpose. A Cursor billing event covers a
// whole agent run rather than one model response (a 22-turn transcript is commonly
// one event), so there is no turn to attribute it to. usage_events already treats a
// NULL ordinal as session-level rather than per-message usage, so these land in
// sessions.total_* and the session's daily rollup without claiming a turn they
// cannot identify.
//
// SourceOffset is negative, which no transcript row can be, so the fold's
// (offset, index) dedup separates provider rows from parsed ones without a special
// case. The dedup key carries the vendor's event identity, so a row that somehow
// reached the batch twice still folds once.
func providerUsageForSessionTx(ctx context.Context, tx pgx.Tx, sessionID int64) ([]ProjUsage, error) {
	rows, err := tx.Query(ctx,
		`SELECT provider, event_key, model, input_tokens, output_tokens,
		        cache_write_tokens, cache_read_tokens, cost_usd, cost_source,
		        occurred_at
		   FROM provider_usage_events
		  WHERE session_id = $1
		  ORDER BY occurred_at, id`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("read provider usage for session %d: %w", sessionID, err)
	}
	defer rows.Close()
	var out []ProjUsage
	for rows.Next() {
		var u ProjUsage
		var provider, eventKey, costSource string
		if err := rows.Scan(&provider, &eventKey, &u.Model, &u.Input, &u.Output,
			&u.CacheWrite, &u.CacheRead, &u.CostUSD, &costSource,
			&u.OccurredAt); err != nil {
			return nil, fmt.Errorf("scan provider usage for session %d: %w", sessionID, err)
		}
		// Disclosure is decided here against the running catalog, not read from the
		// stored column, so it is rebuild-derived exactly as it is for a parsed row.
		// A stored event is immutable, so the ingest-time decision never gets another
		// look; if the catalog later withdrew a model, reading that column would keep
		// publishing a name akari no longer discloses. parse.Epoch already moves on a
		// pricing-table change, so the corpus re-folds and every attached row picks up
		// the current answer.
		u.ModelNamePublic = pricing.ModelNamePublic(u.Model)
		u.CostSource = CostSource(costSource)
		u.DedupKey = "provider:" + provider + ":" + eventKey
		u.SourceOffset = -1
		u.SourceIndex = len(out)
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate provider usage for session %d: %w", sessionID, err)
	}
	return out, nil
}
