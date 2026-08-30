package mcpserver

import "testing"

func TestSessionsSummaryNamesFollowUpIds(t *testing.T) {
	tests := []struct {
		name string
		in   sessionsDTO
		want string
	}{
		{
			name: "empty last page",
			in:   sessionsDTO{},
			want: "list_sessions: 0 sessions returned; no more pages. Full data is in structuredContent.",
		},
		{
			name: "each row names session_id and project_id",
			in: sessionsDTO{
				Sessions: []sessionDTO{
					{ID: 12, ProjectID: 3},
					{ID: 11, ProjectID: 3},
					{ID: 10, ProjectID: 2},
				},
			},
			want: "list_sessions: 3 sessions returned; session_id=12 project_id=3; session_id=11 project_id=3; session_id=10 project_id=2; no more pages. Full data is in structuredContent.",
		},
		{
			name: "orphaned row omits project_id",
			in: sessionsDTO{
				Sessions: []sessionDTO{{ID: 12}},
			},
			want: "list_sessions: 1 sessions returned; session_id=12; no more pages. Full data is in structuredContent.",
		},
		{
			name: "continuing page includes next_cursor",
			in: sessionsDTO{
				Sessions:   []sessionDTO{{ID: 12, ProjectID: 3}},
				NextCursor: "abc",
			},
			want: "list_sessions: 1 sessions returned; session_id=12 project_id=3; more available via next_cursor=abc. Full data is in structuredContent.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionsSummary(tt.in); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProjectsSummaryNamesProjectIds(t *testing.T) {
	tests := []struct {
		name string
		in   projectsDTO
		want string
	}{
		{
			name: "empty",
			in:   projectsDTO{},
			want: "list_projects: 0 projects returned. Full data is in structuredContent.",
		},
		{
			name: "each row names project_id",
			in: projectsDTO{
				Projects: []projectDTO{{ID: 3}, {ID: 7}},
			},
			want: "list_projects: 2 projects returned; project_id=3; project_id=7. Full data is in structuredContent.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectsSummary(tt.in); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOverviewSummaryNamesUserIds(t *testing.T) {
	tests := []struct {
		name string
		in   overviewDTO
		want string
	}{
		{
			name: "no users",
			in:   overviewDTO{},
			want: "overview: fleet usage loaded. Full data is in structuredContent.",
		},
		{
			name: "each account names user_id and username",
			in: overviewDTO{
				Users: []userRefDTO{{ID: 1, Username: "grace"}, {ID: 2, Username: "ada"}},
			},
			want: "overview: fleet usage loaded; user_id=1 grace; user_id=2 ada. Full data is in structuredContent.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := overviewSummary(tt.in); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
