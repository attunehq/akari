// Package cursor collects Cursor's account-wide usage from cursor.com and shapes
// it into the provider-usage events akari's server stores.
//
// It exists because a Cursor transcript is deliberately lossy: it records the
// conversation and the tool calls, but no model, no token counts, and no cost (see
// internal/parser/cursor.go). Cursor reports all three per billing request on its
// dashboard API, and stamps each event with the conversation it served. That
// conversation id is the same identifier the CLI names its transcript directory
// with, so a fetched event joins the session akari already ingested.
//
// The collection is account-wide rather than machine-scoped, which is the source of
// both its value and its main constraint. Its value: most Cursor spend never writes
// a local transcript at all (IDE composer chats, cloud agents, the Grok bot), so
// only this feed can report an account's real usage. Its constraint: every machine
// signed in to the account sees the same events, so the events must carry an
// identity stable enough for two machines to converge on one stored copy. The feed
// exposes no event id, so this package derives one; see EventKey.
package cursor

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Session is a resolved cursor.com credential: the Cookie header to present, and
// the account the credential belongs to.
//
// AccountID scopes stored events so one akari user may collect from two Cursor
// accounts without their derived event keys colliding. It is the stable subject
// from the token, not the email, so a renamed account keeps its history.
type Session struct {
	CookieHeader string
	AccountID    string
}

// tokenExpiryFloor is how much validity a token must have left to be used. Cursor
// signs short-lived access tokens and akari never refreshes them, so a token about
// to expire is treated as absent: the collection is skipped and retried on the next
// tick, which is strictly better than a mid-page 401 that abandons a partial fetch.
const tokenExpiryFloor = 60 * time.Second

// ErrNoSession reports that no usable cursor.com credential was found. It is not a
// failure: a machine with no Cursor install, or one signed out, simply has nothing
// to collect, and callers skip rather than log an error.
var ErrNoSession = errors.New("no usable cursor.com session")

// ResolveSession finds a credential to read the dashboard API with.
//
// A configured cookie header wins, and wins absolutely: an operator who pasted a
// header meant that account, so falling back to a local token would silently
// collect someone else's usage. Otherwise the local Cursor.app session is read from
// its own state database. akari never refreshes, rotates, or writes a token; it
// reads the one Cursor already holds and uses it while it is valid.
func ResolveSession(configuredCookie string, home string, env func(string) string) (Session, error) {
	if h := strings.TrimSpace(configuredCookie); h != "" {
		id, err := accountFromCookie(h)
		if err != nil {
			return Session{}, fmt.Errorf("configured cursor cookie: %w", err)
		}
		return Session{CookieHeader: h, AccountID: id}, nil
	}

	token, err := readAppAccessToken(stateDBPath(home, env))
	if err != nil {
		return Session{}, err
	}
	claims, err := parseJWTClaims(token)
	if err != nil {
		return Session{}, err
	}
	if time.Until(claims.expiry()) <= tokenExpiryFloor {
		return Session{}, ErrNoSession
	}
	id := claims.accountID()
	if id == "" {
		return Session{}, ErrNoSession
	}
	// Cursor's own web client presents the token as the session cookie's second
	// "::"-separated field, with the account id first. The separator is percent
	// encoded because the cookie value carries it literally.
	return Session{
		CookieHeader: "WorkosCursorSessionToken=" + id + "%3A%3A" + token,
		AccountID:    id,
	}, nil
}

// stateDBPath locates Cursor's VS Code-style global state database, which is where
// Cursor.app keeps the access token for the signed-in account.
func stateDBPath(home string, env func(string) string) string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")
	case "windows":
		base := env("APPDATA")
		if base == "" {
			base = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(base, "Cursor", "User", "globalStorage", "state.vscdb")
	default:
		base := env("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, "Cursor", "User", "globalStorage", "state.vscdb")
	}
}

// readAppAccessToken reads cursorAuth/accessToken out of Cursor's state database.
//
// The database is opened read-only, the same way OpenCode discovery opens its
// store: Cursor may be running and writing, and akari must never create or modify
// a file in another application's directory.
func readAppAccessToken(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", ErrNoSession
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve cursor state database %s: %w", path, err)
	}
	dsn := "file:" + filepath.ToSlash(abs) + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", fmt.Errorf("open cursor state database %s: %w", path, err)
	}
	defer db.Close()

	var raw []byte
	err = db.QueryRow(`SELECT value FROM ItemTable WHERE key = 'cursorAuth/accessToken'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoSession
	}
	if err != nil {
		return "", fmt.Errorf("read cursor access token: %w", err)
	}
	token := strings.TrimSpace(decodeStateValue(raw))
	if token == "" {
		return "", ErrNoSession
	}
	return token, nil
}

// decodeStateValue turns a state.vscdb value into text. The column is typed BLOB
// and Cursor has stored the token as both UTF-8 and BOM-less UTF-16LE across
// versions; read as UTF-8 the latter keeps an interleaved NUL after every
// character, which would produce a token no server accepts. A run of ASCII bytes
// alternating with NULs is unambiguously the UTF-16LE case.
func decodeStateValue(raw []byte) string {
	if len(raw) >= 2 && len(raw)%2 == 0 && isUTF16LEASCII(raw) {
		out := make([]byte, 0, len(raw)/2)
		for i := 0; i < len(raw); i += 2 {
			out = append(out, raw[i])
		}
		return string(out)
	}
	return string(raw)
}

func isUTF16LEASCII(raw []byte) bool {
	for i := 0; i+1 < len(raw); i += 2 {
		if raw[i] == 0 || raw[i] > 0x7f || raw[i+1] != 0 {
			return false
		}
	}
	return true
}

// jwtClaims is the subset of a Cursor access token akari reads: who it is for, and
// how long it is good for. The token is never verified here; akari is not the
// audience, it is only reusing the credential the vendor's own client holds, and
// cursor.com is the only party whose acceptance matters.
type jwtClaims struct {
	Sub string `json:"sub"`
	Exp int64  `json:"exp"`
}

func (c jwtClaims) expiry() time.Time {
	if c.Exp <= 0 {
		return time.Time{}
	}
	return time.Unix(c.Exp, 0)
}

// accountID normalizes the token subject to the stable account identifier the
// cookie carries: WorkOS issues subjects as "provider|id", and only the id half
// appears in the session cookie.
func (c jwtClaims) accountID() string {
	sub := strings.TrimSpace(c.Sub)
	if sub == "" {
		return ""
	}
	parts := strings.Split(sub, "|")
	return parts[len(parts)-1]
}

func parseJWTClaims(token string) (jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return jwtClaims{}, fmt.Errorf("cursor access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return jwtClaims{}, fmt.Errorf("decode cursor access token payload: %w", err)
	}
	var c jwtClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return jwtClaims{}, fmt.Errorf("parse cursor access token payload: %w", err)
	}
	return c, nil
}

// accountFromCookie recovers the account a pasted Cookie header belongs to, so
// configured and automatic credentials scope their stored events identically. The
// session cookie's value is "<account>::<jwt>", percent-encoded; the JWT is
// preferred because it carries the canonical subject, with the value's own first
// field as the fallback for a cookie whose token half is opaque.
func accountFromCookie(header string) (string, error) {
	for part := range strings.SplitSeq(header, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || strings.TrimSpace(name) != "WorkosCursorSessionToken" {
			continue
		}
		decoded, err := url.QueryUnescape(strings.TrimSpace(value))
		if err != nil {
			decoded = strings.TrimSpace(value)
		}
		id, token, ok := strings.Cut(decoded, "::")
		if !ok {
			return "", fmt.Errorf("WorkosCursorSessionToken is not <account>::<token>")
		}
		if claims, err := parseJWTClaims(token); err == nil {
			if sub := claims.accountID(); sub != "" {
				return sub, nil
			}
		}
		if id = strings.TrimSpace(id); id != "" {
			return id, nil
		}
		return "", fmt.Errorf("WorkosCursorSessionToken names no account")
	}
	return "", fmt.Errorf("no WorkosCursorSessionToken cookie in header")
}
