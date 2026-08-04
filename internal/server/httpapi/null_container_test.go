package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/jssblck/akari/internal/server/store"
)

// TestBrowserResponsesCarryNoNullContainers walks every browser GET endpoint against a
// live server and fails on a JSON null where openapi.json declares an array or an
// object. The frontend compiles against that contract and dropped its `?? []` guards on
// the strength of it, so a null here is a runtime crash in the browser rather than a
// cosmetic difference.
//
// It runs at the endpoint rather than at the store because the invariant is about the
// wire, and because a hand-maintained list of store reads is exactly what under-covered
// it before: the six reads that list happened to name were clean while the transcript,
// audit and session-feed reads were not. Driving it from browserContracts means
// a new endpoint is covered the moment it is added, which the contract test already
// requires of every endpoint.
//
// Both fixtures matter. The empty account catches a read that only fills a slice from
// rows; the seeded-but-bare session catches a read whose collection is empty for an
// ordinary session (no subagents, no tool calls, no fallbacks), which is the common case
// rather than an edge one.
func TestBrowserResponsesCarryNoNullContainers(t *testing.T) {
	t.Parallel()
	server, st := newTestServer(t)
	client := registerAdmin(t, server.URL)
	document := readContractDocument(t)
	ctx := context.Background()

	check := func(t *testing.T, path, schemaName string) {
		t.Helper()
		response := mustGet(t, client, server.URL+path)
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, response.StatusCode)
		}
		var body any
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		var nulls []string
		findNullContainers(t, document, body, schemaRef(schemaName), path, &nulls)
		if len(nulls) > 0 {
			sort.Strings(nulls)
			t.Errorf("GET %s returned null where the contract declares a container:\n\t%s",
				path, strings.Join(nulls, "\n\t"))
		}
	}

	// An account with nothing in it: every list is empty because no row exists.
	t.Run("empty account", func(t *testing.T) {
		for _, e := range emptyScopeEndpoints {
			t.Run(e.path, func(t *testing.T) { check(t, e.path, e.schema) })
		}
	})

	// Two fixtures beyond the empty account. An ordinary session: two turns, no
	// subagents, no tool calls, no attachments, no model fallbacks, no events, so every
	// collection hanging off it is legitimately empty. And a session whose projection
	// parsed to nothing at all, which takes the readers' empty-window early returns.
	user, err := st.UserByUsername(ctx, "grace")
	if err != nil {
		t.Fatalf("load registered user: %v", err)
	}
	projectID, err := st.UpsertProject(ctx, "github.com/hopper/subroutines", "github.com", "hopper", "subroutines", "subroutines", "remote")
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	announced, err := st.Announce(ctx, store.AnnounceParams{
		UserID: user.ID, Agent: "claude", SourceSessionID: "bare-session",
		ProjectID: projectID, Cwd: "/home/grace/subroutines", Machine: "hopper",
	})
	if err != nil {
		t.Fatalf("announce session: %v", err)
	}
	rebuildWith(t, st, announced.SessionID, store.ProjectionDelta{Messages: []store.MessageDelta{
		{Ordinal: 0, Role: "user", Content: "port the A-0 compiler notes into the readme"},
		{Ordinal: 1, Role: "assistant", Content: "Ported."},
	}})
	publicID, err := st.PublishSession(ctx, announced.SessionID, user.ID, "a0-compiler-notes")
	if err != nil {
		t.Fatalf("publish session: %v", err)
	}
	if err := st.PublishProjectOverview(ctx, projectID); err != nil {
		t.Fatalf("publish project: %v", err)
	}
	if err := st.PublishOverview(ctx, user.ID); err != nil {
		t.Fatalf("publish overview: %v", err)
	}

	// A session that parsed to no messages at all. Its transcript readers return
	// early before any fill runs, which is the path that leaves a page's collections
	// untouched.
	empty, err := st.Announce(ctx, store.AnnounceParams{
		UserID: user.ID, Agent: "codex", SourceSessionID: "unparsed-session",
		ProjectID: projectID, Cwd: "/home/grace/subroutines", Machine: "hopper",
	})
	if err != nil {
		t.Fatalf("announce empty session: %v", err)
	}
	rebuildWith(t, st, empty.SessionID, store.ProjectionDelta{})
	emptyPublicID, err := st.PublishSession(ctx, empty.SessionID, user.ID, "nothing-parsed")
	if err != nil {
		t.Fatalf("publish empty session: %v", err)
	}

	sessionID := fmt.Sprint(announced.SessionID)
	emptyID := fmt.Sprint(empty.SessionID)
	project := fmt.Sprint(projectID)
	t.Run("bare session", func(t *testing.T) {
		for _, e := range []struct{ path, schema string }{
			{"/api/v1/app/sessions/" + sessionID, "SessionResponse"},
			{"/api/v1/app/sessions/" + sessionID + "/append?after=0", "SessionResponse"},
			// A quiet tick: nothing follows the cursor, so the read returns an empty
			// window and skips the extras fill entirely.
			{"/api/v1/app/sessions/" + sessionID + "/append?after=1", "SessionResponse"},
			{"/api/v1/app/sessions/" + sessionID + "/transcript?before=1", "TranscriptResponse"},
			{"/api/v1/app/projects/" + project, "ProjectResponse"},
			{"/api/v1/app/public/projects/" + project, "PublicProjectResponse"},
			{"/api/v1/app/public/sessions/" + publicID, "PublicSessionResponse"},
			{"/api/v1/app/public/sessions/" + publicID + "/transcript?before=1", "PublicSessionResponse"},
			{"/api/v1/app/public/users/grace", "PublicOverviewResponse"},
			// The session that parsed to nothing, on every surface that renders one.
			{"/api/v1/app/sessions/" + emptyID, "SessionResponse"},
			{"/api/v1/app/sessions/" + emptyID + "/append?after=0", "SessionResponse"},
			{"/api/v1/app/sessions/" + emptyID + "/transcript?before=0", "TranscriptResponse"},
			{"/api/v1/app/public/sessions/" + emptyPublicID, "PublicSessionResponse"},
			{"/api/v1/app/public/sessions/" + emptyPublicID + "/transcript?before=0", "PublicSessionResponse"},
		} {
			t.Run(e.path, func(t *testing.T) { check(t, e.path, e.schema) })
		}
		// The lists that were empty above now have exactly one row, so their element
		// schemas get walked too rather than only their empty envelopes.
		for _, e := range emptyScopeEndpoints {
			t.Run("populated "+e.path, func(t *testing.T) { check(t, e.path, e.schema) })
		}
	})
}

// emptyScopeEndpoints are the browser GETs that need no path parameter, so they can be
// walked both before and after the fixture lands. Their names must match a
// browserContracts entry; TestEveryBrowserGETIsWalkedForNullContainers enforces that
// none is quietly dropped.
var emptyScopeEndpoints = []struct{ path, schema string }{
	{"/api/v1/app/bootstrap", "Viewer"},
	{"/api/v1/app/overview", "OverviewResponse"},
	{"/api/v1/app/insights", "InsightsResponse"},
	{"/api/v1/app/projects", "ProjectsResponse"},
	{"/api/v1/app/sessions", "SessionsResponse"},
	{"/api/v1/app/account", "AccountResponse"},
	{"/api/v1/tokens", "TokensResponse"},
	{"/api/v1/reparse/status", "ReparseStatusResponse"},
}

// TestEveryBrowserGETIsWalkedForNullContainers keeps the walk honest: a new browser GET
// added to browserContracts but not to the null walk would ship its arrays unchecked,
// which is precisely how the previous store-side check went stale.
func TestEveryBrowserGETIsWalkedForNullContainers(t *testing.T) {
	walked := map[string]bool{}
	for _, e := range emptyScopeEndpoints {
		walked[e.schema] = true
	}
	// The parameterized GETs the "bare session" subtest covers.
	for _, name := range []string{
		"SessionResponse", "TranscriptResponse", "ProjectResponse",
		"PublicProjectResponse", "PublicSessionResponse", "PublicOverviewResponse",
		"OAuthConsentResponse",
	} {
		walked[name] = true
	}
	for _, contract := range browserContracts {
		if contract.method != "get" || walked[contract.schemaName] {
			continue
		}
		t.Errorf("browser GET %s returns %s, which no null-container walk covers; add it to the walk",
			contract.path, contract.schemaName)
	}
}

func schemaRef(name string) map[string]any {
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

// findNullContainers walks a decoded response beside its schema and records the path of
// every null sitting where the schema declares a non-nullable array or object. A schema
// that permits null (an anyOf carrying {"type":"null"}, like an absent session shape)
// is respected: the point is to catch containers the contract promises are always
// present, not to outlaw nullable fields.
func findNullContainers(t *testing.T, document contractDocument, value any, schema map[string]any, path string, out *[]string) {
	t.Helper()
	schema = resolveSchema(t, document, schema)
	if schema == nil {
		return
	}
	if options, ok := schema["anyOf"].([]any); ok {
		// A nullable field: walk the non-null branch when a value is present, and
		// accept a null because the contract allows it.
		if value == nil {
			return
		}
		for _, option := range options {
			branch, _ := option.(map[string]any)
			if branch == nil || branch["type"] == "null" {
				continue
			}
			findNullContainers(t, document, value, branch, path, out)
		}
		return
	}

	switch schemaType(schema) {
	case "array":
		if value == nil {
			*out = append(*out, path+" (declared array)")
			return
		}
		items, _ := value.([]any)
		itemSchema, _ := schema["items"].(map[string]any)
		for i, element := range items {
			findNullContainers(t, document, element, itemSchema, fmt.Sprintf("%s[%d]", path, i), out)
		}
	case "object":
		if value == nil {
			*out = append(*out, path+" (declared object)")
			return
		}
		fields, _ := value.(map[string]any)
		if properties, ok := schema["properties"].(map[string]any); ok {
			for name, sub := range fields {
				propertySchema, _ := properties[name].(map[string]any)
				findNullContainers(t, document, sub, propertySchema, path+"."+name, out)
			}
			return
		}
		// A free-form map (additionalProperties): every value shares one schema.
		valueSchema, _ := schema["additionalProperties"].(map[string]any)
		for name, sub := range fields {
			findNullContainers(t, document, sub, valueSchema, path+"."+name, out)
		}
	}
}

// schemaType reads a schema's type, tolerating the ["string","null"] form OpenAPI uses
// for a nullable scalar.
func schemaType(schema map[string]any) string {
	switch declared := schema["type"].(type) {
	case string:
		return declared
	case []any:
		for _, option := range declared {
			if name, _ := option.(string); name != "" && name != "null" {
				return name
			}
		}
	}
	if _, ok := schema["properties"]; ok {
		return "object"
	}
	return ""
}

func resolveSchema(t *testing.T, document contractDocument, schema map[string]any) map[string]any {
	t.Helper()
	for depth := 0; schema != nil && depth < 32; depth++ {
		ref, _ := schema["$ref"].(string)
		if ref == "" {
			return schema
		}
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		resolved, ok := document.Components.Schemas[name].(map[string]any)
		if !ok {
			t.Fatalf("OpenAPI has no component schema %q", name)
		}
		schema = resolved
	}
	return schema
}
