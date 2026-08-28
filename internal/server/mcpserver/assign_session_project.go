package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/jssblck/akari/internal/server/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type assignSessionProjectInput struct {
	SessionID int64 `json:"session_id" jsonschema:"the orphaned session to re-home, from list_sessions or get_session"`
	ProjectID int64 `json:"project_id" jsonschema:"the project to pin the session onto, from list_projects"`
}

type assignSessionProjectDTO struct {
	SessionID int64 `json:"session_id"`
	ProjectID int64 `json:"project_id"`
	Pinned    bool  `json:"pinned"`
}

func registerAssignSessionProject(s *mcp.Server, st *store.Store, response responder) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "assign_session_project",
		Description: "Permanently pin an orphaned session onto a project. Use this when a session's worktree is gone so the client classified it orphaned, but the git project it belongs to is known. The pin survives a later reparse and a later client announce that still reports the session as orphaned. The caller must own the session or be an admin. A session that already resolved to a remote or standalone project is rejected. Re-calling with a different project_id re-homes a session that is already pinned.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, in assignSessionProjectInput) (*mcp.CallToolResult, assignSessionProjectDTO, error) {
		if in.SessionID < 1 || in.ProjectID < 1 {
			return nil, assignSessionProjectDTO{}, errors.New("session_id and project_id are required")
		}
		uid, err := callerID(req)
		if err != nil {
			return nil, assignSessionProjectDTO{}, err
		}
		detail, err := st.SessionDetailByID(ctx, in.SessionID)
		if err != nil {
			return nil, assignSessionProjectDTO{}, mapNotFound(err, "session")
		}
		if uid != detail.OwnerID {
			user, err := st.UserByID(ctx, uid)
			if err != nil || !user.IsAdmin {
				return nil, assignSessionProjectDTO{}, errors.New("cannot assign this session")
			}
		}
		got, err := st.AssignSessionProject(ctx, in.SessionID, in.ProjectID)
		if err != nil {
			return nil, assignSessionProjectDTO{}, mapAssignSessionProjectErr(err)
		}
		out := assignSessionProjectDTO{SessionID: got.SessionID, ProjectID: got.ProjectID, Pinned: got.Pinned}
		return jsonResult(response, fmt.Sprintf("assign_session_project: session %d pinned to project %d. Full data is in structuredContent.", out.SessionID, out.ProjectID), out, nil)
	})
}

func mapAssignSessionProjectErr(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return errors.New("project not found")
	case errors.Is(err, store.ErrNotOrphaned):
		return errors.New("session is not orphaned")
	default:
		return err
	}
}
