package httpapi

import (
	"errors"
	"net/http"

	"github.com/jssblck/akari/internal/server/store"
)

type sessionProjectRequest struct {
	ProjectID int64 `json:"project_id"`
}

type sessionProjectResponse struct {
	SessionID int64 `json:"session_id"`
	ProjectID int64 `json:"project_id"`
	Pinned    bool  `json:"pinned"`
}

func (s *Server) handleAPISessionProject(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id", "session")
	if !ok {
		return
	}
	var req sessionProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ProjectID < 1 {
		writeError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	p, _ := principalFrom(r.Context())
	detail, err := s.Store.SessionDetailByID(r.Context(), id)
	if writeStoreErr(w, err, "session not found", "load session") {
		return
	}
	if p.UserID != detail.OwnerID {
		user, err := s.Store.UserByID(r.Context(), p.UserID)
		if err != nil || !user.IsAdmin {
			writeError(w, http.StatusForbidden, "cannot assign this session")
			return
		}
	}
	got, err := s.Store.AssignSessionProject(r.Context(), id, req.ProjectID)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotOrphaned):
		writeError(w, http.StatusBadRequest, "session is not orphaned")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "project not found")
		return
	default:
		writeError(w, http.StatusInternalServerError, "assign session project")
		return
	}
	if got.PreviousProjectID != got.ProjectID {
		s.analyticsSnapshots.invalidate(analyticsScope{kind: analyticsProjectScope, id: got.PreviousProjectID})
		s.analyticsSnapshots.invalidate(analyticsScope{kind: analyticsProjectScope, id: got.ProjectID})
	}
	writeJSON(w, http.StatusOK, sessionProjectResponse{
		SessionID: got.SessionID, ProjectID: got.ProjectID, Pinned: got.Pinned,
	})
}
