package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jssblck/akari/internal/server/auth"
	"github.com/jssblck/akari/internal/server/store"
)

type createBotRequest struct {
	Username string `json:"username"`
}

func decodeCreateTokenRequest(w http.ResponseWriter, r *http.Request) (createTokenRequest, bool) {
	var req createTokenRequest
	if !decodeJSON(w, r, &req) {
		return createTokenRequest{}, false
	}
	if req.Scope == "" {
		req.Scope = scopeIngest
	}
	if !isValidScope(req.Scope) {
		writeError(w, http.StatusBadRequest, "scope must be 'ingest', 'read', or 'full'")
		return createTokenRequest{}, false
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "token name is required")
		return createTokenRequest{}, false
	}
	return req, true
}

func (s *Server) handleCreateBot(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	var req createBotRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	bot, err := s.Store.CreateBot(r.Context(), p.UserID, req.Username)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusForbidden, "bot accounts cannot create bots")
		return
	case isUniqueViolation(err):
		writeError(w, http.StatusConflict, "username already taken")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "create bot")
		return
	}
	writeJSON(w, http.StatusCreated, accountBotDTOFrom(bot))
}

func (s *Server) handleCreateBotToken(w http.ResponseWriter, r *http.Request) {
	botID, ok := pathInt64(w, r, "bot_id", "bot")
	if !ok {
		return
	}
	req, ok := decodeCreateTokenRequest(w, r)
	if !ok {
		return
	}
	secret, err := auth.NewToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "generate token")
		return
	}
	id, err := s.Store.CreateBotAPIToken(r.Context(), botID, req.Name, req.Scope, auth.HashToken(secret))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "bot not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "store bot token")
		return
	}
	writeJSON(w, http.StatusCreated, createdTokenResponse{
		ID: id, Name: req.Name, Scope: req.Scope, Token: secret,
	})
}

func (s *Server) handleRevokeBotToken(w http.ResponseWriter, r *http.Request) {
	botID, ok := pathInt64(w, r, "bot_id", "bot")
	if !ok {
		return
	}
	tokenID, ok := pathInt64(w, r, "token_id", "token")
	if !ok {
		return
	}
	err := s.Store.RevokeBotAPIToken(r.Context(), botID, tokenID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "bot not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "revoke bot token")
		return
	}
	writeJSON(w, http.StatusOK, revokedResponse{Revoked: true})
}

func (s *Server) handleDeleteBot(w http.ResponseWriter, r *http.Request) {
	botID, ok := pathInt64(w, r, "bot_id", "bot")
	if !ok {
		return
	}
	err := s.Store.DeleteBot(r.Context(), botID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "bot not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "delete bot")
		return
	}
	s.analyticsSnapshots.invalidateAll()
	s.insights.kickRefresh()
	writeJSON(w, http.StatusOK, deletedBotResponse{Deleted: true})
}
