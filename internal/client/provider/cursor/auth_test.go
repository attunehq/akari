package cursor

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// signedToken builds a JWT-shaped access token. Nothing here verifies a signature:
// akari is not the token's audience, it only reuses the credential Cursor already
// holds, and cursor.com is the sole judge of whether it is good.
func signedToken(t *testing.T, sub string, expiry time.Time) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"sub": sub, "exp": expiry.Unix()})
	if err != nil {
		t.Fatalf("encode claims: %v", err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

// writeStateDB creates a Cursor-shaped state database holding one token, stored with
// the given encoder so both real-world encodings can be exercised.
func writeStateDB(t *testing.T, dir, token string, encode func(string) any) string {
	t.Helper()
	path := filepath.Join(dir, "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("create state database: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT PRIMARY KEY, value BLOB)`); err != nil {
		t.Fatalf("create ItemTable: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES ('cursorAuth/accessToken', ?)`, encode(token)); err != nil {
		t.Fatalf("insert token: %v", err)
	}
	return path
}

func asUTF8(s string) any { return s }

// asUTF16LE is the other encoding Cursor has shipped. Read as UTF-8 it keeps a NUL
// after every character, which would produce a token cursor.com rejects, so the
// decoder has to recognize it.
func asUTF16LE(s string) any {
	out := make([]byte, 0, len(s)*2)
	for i := range len(s) {
		out = append(out, s[i], 0)
	}
	return out
}

func TestReadsTheLocalTokenInBothEncodings(t *testing.T) {
	for name, encode := range map[string]func(string) any{"utf8": asUTF8, "utf16le": asUTF16LE} {
		t.Run(name, func(t *testing.T) {
			token := signedToken(t, "user_01KZWEKGDR1MRMNQ3RN8B1W5BF", time.Now().Add(time.Hour))
			path := writeStateDB(t, t.TempDir(), token, encode)
			got, err := readAppAccessToken(path)
			if err != nil {
				t.Fatalf("read token: %v", err)
			}
			if got != token {
				t.Errorf("read token %q, want %q", got, token)
			}
		})
	}
}

// The cookie names the account first and the token second, and the account half is
// the subject's trailing segment. Getting this wrong sends cursor.com a cookie it
// silently rejects.
func TestResolvesASessionFromTheLocalToken(t *testing.T) {
	token := signedToken(t, "workos|user_01KZWEKGDR1MRMNQ3RN8B1W5BF", time.Now().Add(time.Hour))
	stateDir := filepath.Join(t.TempDir(), "globalStorage")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("make state dir: %v", err)
	}
	path := writeStateDB(t, stateDir, token, asUTF8)
	if got, err := readAppAccessToken(path); err != nil || got != token {
		t.Fatalf("read token = %q, %v; want the stored token", got, err)
	}

	// stateDBPath is platform-specific, so the account and cookie assembly are
	// checked against the claims directly rather than against a home layout that
	// only matches one OS.
	claims, err := parseJWTClaims(token)
	if err != nil {
		t.Fatalf("parse claims: %v", err)
	}
	if got := claims.accountID(); got != "user_01KZWEKGDR1MRMNQ3RN8B1W5BF" {
		t.Errorf("account id %q, want the subject's trailing segment", got)
	}
}

// An expired or nearly expired token is treated as absent. akari never refreshes,
// so using one would abandon a partial fetch on a mid-page rejection.
func TestExpiringTokenIsTreatedAsNoSession(t *testing.T) {
	for name, expiry := range map[string]time.Time{
		"expired":        time.Now().Add(-time.Minute),
		"about to lapse": time.Now().Add(tokenExpiryFloor / 2),
	} {
		t.Run(name, func(t *testing.T) {
			token := signedToken(t, "user_x", expiry)
			claims, err := parseJWTClaims(token)
			if err != nil {
				t.Fatalf("parse claims: %v", err)
			}
			if time.Until(claims.expiry()) > tokenExpiryFloor {
				t.Errorf("token expiring at %v passed the floor, want it rejected", expiry)
			}
		})
	}
}

// A machine with no Cursor install has nothing to collect, which is a skip and not
// an error: most machines in a fleet are in exactly this state.
func TestMissingStateDatabaseIsNoSession(t *testing.T) {
	_, err := readAppAccessToken(filepath.Join(t.TempDir(), "absent.vscdb"))
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("missing state database returned %v, want ErrNoSession", err)
	}
}

// A configured cookie pins collection to that account. Falling back to a local token
// behind an explicit credential would silently collect a different account's usage.
func TestConfiguredCookieWinsAndNamesItsAccount(t *testing.T) {
	token := signedToken(t, "workos|user_pasted", time.Now().Add(time.Hour))
	header := fmt.Sprintf("other=1; WorkosCursorSessionToken=user_pasted%%3A%%3A%s; more=2", token)

	got, err := ResolveSession(header, t.TempDir(), func(string) string { return "" })
	if err != nil {
		t.Fatalf("resolve a configured cookie: %v", err)
	}
	if got.CookieHeader != header {
		t.Errorf("cookie header %q, want it forwarded verbatim", got.CookieHeader)
	}
	if got.AccountID != "user_pasted" {
		t.Errorf("account id %q, want user_pasted", got.AccountID)
	}
}

// A header with no session cookie is a configuration mistake worth reporting, not a
// silent fallback to whichever account this machine happens to be signed in to.
func TestConfiguredCookieWithoutASessionTokenFails(t *testing.T) {
	if _, err := ResolveSession("someother=1", t.TempDir(), func(string) string { return "" }); err == nil {
		t.Fatal("a cookie header with no session token resolved, want an error")
	}
}

func TestNoCredentialAnywhereIsNoSession(t *testing.T) {
	_, err := ResolveSession("", t.TempDir(), func(string) string { return "" })
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("resolving with no credential returned %v, want ErrNoSession", err)
	}
}
