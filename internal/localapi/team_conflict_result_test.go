package localapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/team"
	"github.com/haochase/haowork/internal/teamsync"
)

type conflictResultFacade struct{ result team.PushResult }

func (f conflictResultFacade) Status(context.Context) (team.Status, error) { return team.Status{}, nil }
func (f conflictResultFacade) SyncNow(context.Context) (teamsync.SyncReport, error) {
	return teamsync.SyncReport{}, nil
}
func (f conflictResultFacade) Queue(context.Context) ([]teamsync.OutboxEntry, error) { return nil, nil }
func (f conflictResultFacade) Conflicts(context.Context) ([]model.Conflict, error)   { return nil, nil }
func (f conflictResultFacade) ResolveConflict(context.Context, string, string) (team.PushResult, error) {
	return f.result, nil
}
func (f conflictResultFacade) ResolveConflictRequest(context.Context, team.ConflictResolutionRequest) (team.PushResult, error) {
	return f.result, nil
}

func TestTeamConflictResolutionMapsPushResultsToHTTP(t *testing.T) {
	tests := []struct {
		name   string
		result team.PushResult
		want   int
	}{
		{"conflict", team.PushResult{Status: team.PushConflict}, http.StatusConflict},
		{"forbidden", team.PushResult{Status: team.PushRejected, Code: "unauthorized"}, http.StatusForbidden},
		{"rejected", team.PushResult{Status: team.PushRejected, Code: "invalid_resolution"}, http.StatusUnprocessableEntity},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{Sessions: NewSessionStore(), Team: conflictResultFacade{result: test.result}}
			response := jsonRequest(t, server, http.MethodPost, teamPath+"/conflicts/CNF-1/resolve", map[string]any{"action": team.AcceptTeam}, authenticatedCookie(t, server))
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}
}
