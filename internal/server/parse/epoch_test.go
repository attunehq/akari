package parse

import (
	"encoding/base64"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

// updateGolden rewrites the committed golden snapshots from the current parser
// output. Run `go test ./internal/server/parse -run TestGoldenProjection -update`
// after an intentional parser change, in the same commit that bumps parse.Epoch.
var updateGolden = flag.Bool("update", false, "rewrite parser golden snapshots")

// goldenFixtures are the representative raw sessions whose parsed projection is
// snapshotted. Each names a raw JSONL input under testdata/golden and the agent
// whose reducer parses it. The identities in the fixtures are women in computing
// history (Grace Hopper, Ada Lovelace), matching the rest of the suite.
var goldenFixtures = []struct {
	name  string
	agent string
}{
	{name: "claude", agent: "claude"},
	{name: "codex", agent: "codex"},
	{name: "cursor", agent: "cursor"},
	{name: "grok", agent: "grok"},
	{name: "opencode", agent: "opencode"},
}

// TestGoldenProjection is the guardrail that makes the parse.Epoch bump impossible
// to forget. It parses each fixture through the exact reducer + pricing path a
// rebuild uses (sessionReducer), snapshots the resulting projection delta, and
// compares it to a committed golden file. Any change to parser or reducer output,
// or to the pricing the projection carries, makes a snapshot differ and fails this
// test by name, so a developer cannot change what the parser produces without
// being told to bump parse.Epoch (the fleet-wide rebuild signal) and refresh the
// goldens. It needs no database: the reducer and pricing are pure.
func TestGoldenProjection(t *testing.T) {
	for _, f := range goldenFixtures {
		t.Run(f.name, func(t *testing.T) {
			rawPath := filepath.Join("testdata", "golden", f.name+".jsonl")
			raw, err := os.ReadFile(rawPath)
			if err != nil {
				t.Fatalf("read fixture %s: %v", rawPath, err)
			}

			// Drive the same seam the store calls: feed the whole session as one
			// region from offset 0, then finish and price. The returned delta is
			// exactly what a rebuild would write to the projection.
			r, err := newSessionReducer(f.agent)
			if err != nil {
				t.Fatalf("reducer for %s: %v", f.agent, err)
			}
			if err := r.Feed(raw, 0); err != nil {
				t.Fatalf("reduce %s fixture: %v", f.name, err)
			}
			delta := r.Finish()
			if len(delta.Messages) == 0 {
				t.Fatalf("%s fixture produced no messages; the fixture or parser is broken", f.name)
			}

			// An attachment body is raw decoded bytes (a PNG, say), not text, and
			// a JSON string cannot carry arbitrary bytes: encoding/json replaces
			// each invalid UTF-8 byte with U+FFFD. That loses the bytes, so a
			// parser change that corrupted an image would still match, and the
			// replacement is spelled differently across Go releases (the escape
			// sequence backslash-ufffd through Go 1.26, the literal character
			// once Go 1.27 rebuilt the package on json/v2). Snapshot bodies as
			// base64, which is exact and stable.
			for i := range delta.Attachments {
				delta.Attachments[i].Body = base64.StdEncoding.EncodeToString([]byte(delta.Attachments[i].Body))
			}
			// json/v2 rejects invalid UTF-8 where v1 silently replaces it, so
			// this fails if any other field starts carrying binary bytes that
			// would make the snapshot lossy in the same way.
			if _, err := jsonv2.Marshal(delta); err != nil {
				t.Fatalf("projection is not exactly representable as JSON; encode binary fields like attachment bodies above: %v", err)
			}

			got, err := json.MarshalIndent(delta, "", "  ")
			if err != nil {
				t.Fatalf("marshal delta: %v", err)
			}
			got = append(got, '\n')

			goldenPath := filepath.Join("testdata", "golden", f.name+".json")
			if *updateGolden {
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatalf("write golden %s: %v", goldenPath, err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s: %v (run with -update to create it)", goldenPath, err)
			}
			if string(got) != string(want) {
				t.Errorf(`parser output for the %q fixture changed.

If this change is intentional, bump the parse.Epoch constant in epoch.go (it is
the fleet-wide reparse signal; bumping it makes deployed servers rebuild the
stored projection) and refresh the golden snapshots:

    go test ./internal/server/parse -run TestGoldenProjection -update

If it is NOT intentional, your change altered the parsed projection by accident.

--- want (golden)
%s
--- got (current)
%s`, f.name, string(want), string(got))
			}
		})
	}
}
