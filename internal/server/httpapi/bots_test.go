package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/jssblck/akari/internal/server/auth"
	"github.com/jssblck/akari/internal/server/store"
)

func TestBotAccountLifecycle(t *testing.T) {
	t.Parallel()
	server, st := newTestServer(t)
	owner := registerAdmin(t, server.URL)

	response, created := doJSON(t, owner, http.MethodPost, server.URL+"/api/v1/app/account/bots", map[string]string{
		"username": "ci-review",
	})
	if response.StatusCode != http.StatusCreated || created["username"] != "ci-review" {
		t.Fatalf("create bot: status=%d body=%v", response.StatusCode, created)
	}
	botID := int64(created["id"].(float64))

	if status, body := postJSON(t, newClient(t), server.URL+"/api/v1/auth/login", `{"username":"ci-review","password":"anything"}`); status != http.StatusUnauthorized || body["error"] != "invalid credentials" {
		t.Fatalf("bot password login: status=%d body=%v", status, body)
	}

	response, token := doJSON(t, owner, http.MethodPost,
		server.URL+"/api/v1/app/account/bots/"+strconvFormat(botID)+"/tokens",
		map[string]string{"name": "review workflow", "scope": "full"})
	if response.StatusCode != http.StatusCreated || token["token"] == "" {
		t.Fatalf("create bot token: status=%d body=%v", response.StatusCode, token)
	}
	secret := token["token"].(string)
	tokenID := int64(token["id"].(float64))
	tokenUserID, scope, err := st.TokenAuth(t.Context(), auth.HashToken(secret))
	if err != nil || tokenUserID != botID || scope != scopeFull {
		t.Fatalf("bot token principal = (%d, %q, %v), want (%d, full, nil)", tokenUserID, scope, err, botID)
	}
	response, account := doJSON(t, owner, http.MethodGet, server.URL+"/api/v1/app/account", nil)
	bots, ok := account["bots"].([]any)
	if response.StatusCode != http.StatusOK || !ok || len(bots) != 1 {
		t.Fatalf("account bot list: status=%d body=%v", response.StatusCode, account)
	}
	listed := bots[0].(map[string]any)
	listedTokens, ok := listed["tokens"].([]any)
	if listed["username"] != "ci-review" || !ok || len(listedTokens) != 1 {
		t.Fatalf("listed bot = %v, want ci-review with one token", listed)
	}

	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/v1/app/overview", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("full bot token overview = %d, want 200", response.StatusCode)
	}

	botCreateBody, _ := json.Marshal(map[string]string{"username": "nested-bot"})
	request, err = http.NewRequest(http.MethodPost, server.URL+"/api/v1/app/account/bots", bytes.NewReader(botCreateBody))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("bot creating bot = %d, want 403", response.StatusCode)
	}

	request, err = http.NewRequest(http.MethodPost,
		server.URL+"/api/v1/app/account/bots/"+strconvFormat(botID)+"/tokens",
		bytes.NewReader([]byte(`{"name":"bot managed","scope":"ingest"}`)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("bot managing shared bot token = %d, want 403", response.StatusCode)
	}

	response, _ = doJSON(t, owner, http.MethodDelete,
		server.URL+"/api/v1/app/account/bots/"+strconvFormat(botID)+"/tokens/"+strconvFormat(tokenID), nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("revoke bot token = %d, want 200", response.StatusCode)
	}
	if _, _, err := st.TokenAuth(context.Background(), auth.HashToken(secret)); err == nil {
		t.Fatal("revoked bot token still authenticates")
	}

	response, replacement := doJSON(t, owner, http.MethodPost,
		server.URL+"/api/v1/app/account/bots/"+strconvFormat(botID)+"/tokens",
		map[string]string{"name": "replacement", "scope": "ingest"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create replacement token: status=%d body=%v", response.StatusCode, replacement)
	}
	replacementSecret := replacement["token"].(string)

	projectID, err := st.UpsertProject(t.Context(), "github.com/grace/review", "github.com", "grace", "review", "review", "remote")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Announce(t.Context(), store.AnnounceParams{
		UserID: botID, Agent: "codex", SourceSessionID: "ci-review-1",
		ProjectID: projectID, Cwd: "/ci/review", Machine: "actions",
	}); err != nil {
		t.Fatal(err)
	}

	response, _ = doJSON(t, owner, http.MethodDelete,
		server.URL+"/api/v1/app/account/bots/"+strconvFormat(botID), nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("delete bot = %d, want 200", response.StatusCode)
	}
	deletedBot, err := st.UserByID(t.Context(), botID)
	if err != nil || !deletedBot.IsBot() {
		t.Fatalf("soft-deleted bot = (%+v, %v), want retained bot identity", deletedBot, err)
	}
	if _, _, err := st.TokenAuth(t.Context(), auth.HashToken(replacementSecret)); err == nil {
		t.Fatal("deleted bot token still authenticates")
	}
	response, account = doJSON(t, owner, http.MethodGet, server.URL+"/api/v1/app/account", nil)
	bots, ok = account["bots"].([]any)
	if response.StatusCode != http.StatusOK || !ok || len(bots) != 0 {
		t.Fatalf("account after bot deletion: status=%d body=%v", response.StatusCode, account)
	}
	var sessions int
	if err := st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM sessions WHERE user_id = $1", botID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 {
		t.Fatalf("soft-deleted bot has %d sessions, want 1", sessions)
	}

	response, restored := doJSON(t, owner, http.MethodPost, server.URL+"/api/v1/app/account/bots", map[string]string{
		"username": "ci-review",
	})
	if response.StatusCode != http.StatusCreated || int64(restored["id"].(float64)) != botID {
		t.Fatalf("restore bot: status=%d body=%v", response.StatusCode, restored)
	}
	if _, _, err := st.TokenAuth(t.Context(), auth.HashToken(replacementSecret)); err == nil {
		t.Fatal("restored bot reactivated a revoked token")
	}
}

func TestBotManagementIsSharedAcrossUsers(t *testing.T) {
	t.Parallel()
	server, st := newTestServer(t)
	owner := registerAdmin(t, server.URL)
	response, created := doJSON(t, owner, http.MethodPost, server.URL+"/api/v1/app/account/bots", map[string]string{
		"username": "ci-owner-only",
	})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create bot: status=%d body=%v", response.StatusCode, created)
	}
	botID := int64(created["id"].(float64))

	other, err := st.UpsertProxyUser(t.Context(), "ada")
	if err != nil {
		t.Fatal(err)
	}
	sessionSecret, err := auth.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWebSession(t.Context(), auth.HashToken(sessionSecret), other.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	otherClient := newClient(t)
	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	otherClient.Jar.SetCookies(baseURL, []*http.Cookie{{Name: cookieName, Value: sessionSecret}})

	response, account := doJSON(t, otherClient, http.MethodGet, server.URL+"/api/v1/app/account", nil)
	bots, ok := account["bots"].([]any)
	if response.StatusCode != http.StatusOK || !ok || len(bots) != 1 {
		t.Fatalf("shared bot list: status=%d body=%v", response.StatusCode, account)
	}

	response, token := doJSON(t, otherClient, http.MethodPost,
		server.URL+"/api/v1/app/account/bots/"+strconvFormat(botID)+"/tokens",
		map[string]string{"name": "ada workflow", "scope": "ingest"})
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("other user creating bot token: status=%d body=%v", response.StatusCode, token)
	}
	tokenID := int64(token["id"].(float64))
	response, _ = doJSON(t, otherClient, http.MethodDelete,
		server.URL+"/api/v1/app/account/bots/"+strconvFormat(botID)+"/tokens/"+strconvFormat(tokenID), nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("other user revoking bot token = %d, want 200", response.StatusCode)
	}

	response, _ = doJSON(t, otherClient, http.MethodDelete,
		server.URL+"/api/v1/app/account/bots/"+strconvFormat(botID), nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("other user deleting bot = %d, want 200", response.StatusCode)
	}
	if _, err := st.UserByID(t.Context(), botID); err != nil {
		t.Fatalf("shared bot identity removed after another user deleted it: %v", err)
	}
	response, account = doJSON(t, otherClient, http.MethodGet, server.URL+"/api/v1/app/account", nil)
	bots, ok = account["bots"].([]any)
	if response.StatusCode != http.StatusOK || !ok || len(bots) != 0 {
		t.Fatalf("shared bot remains listed after deletion: status=%d body=%v", response.StatusCode, account)
	}
}
