package localapi

import (
	"net/http"
	"sort"

	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/scmremote"
)

type githubSCMActionRequest struct {
	Actor model.Actor `json:"actor"`
}

type GitHubSCMStatusResponse struct {
	Remote       *model.SCMRemote                `json:"remote,omitempty"`
	Runtime      scmremote.RuntimeStatus         `json:"runtime"`
	Refs         []model.SCMRemoteRefObservation `json:"refs"`
	PullRequests []GitHubPullProjection          `json:"pull_requests"`
	Reviews      []model.SCMReviewObservation    `json:"reviews"`
	Checks       []model.SCMCheckObservation     `json:"checks"`
}

type GitHubPullProjection struct {
	Observation       model.SCMPullRequestObservation `json:"observation"`
	LocalCommitCount  int                             `json:"local_commit_count"`
	ConfirmedBindings int                             `json:"confirmed_bindings"`
}

func (server *Server) registerGitHubSCMRoutes(mux *http.ServeMux) {
	mux.HandleFunc(scmPath+"/github/connect", server.handleGitHubSCMConnect)
	mux.HandleFunc(scmPath+"/github/sync", server.handleGitHubSCMSync)
	mux.HandleFunc(scmPath+"/github/status", server.handleGitHubSCMStatus)
}

func (server *Server) githubSCMUnavailable(writer http.ResponseWriter) bool {
	if server.Project.GitHubSCMAvailable {
		return false
	}
	writeError(writer, http.StatusServiceUnavailable, "github_scm_unavailable", "GitHub SCM observation is not configured")
	return true
}

func (server *Server) handleGitHubSCMConnect(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if server.githubSCMUnavailable(writer) {
		return
	}
	var payload githubSCMActionRequest
	if !decodeJSON(writer, request, &payload) {
		return
	}
	remote, err := server.Project.Service.ConnectGitHubSCM(request.Context(), payload.Actor)
	if err != nil {
		writeDomainError(writer, err)
		return
	}
	if err := server.afterWrite(request.Context()); err != nil {
		writeDomainError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, remote)
}

func (server *Server) handleGitHubSCMSync(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if server.githubSCMUnavailable(writer) {
		return
	}
	var payload githubSCMActionRequest
	if !decodeJSON(writer, request, &payload) {
		return
	}
	report, err := server.Project.Service.SyncGitHubSCM(request.Context(), payload.Actor)
	if err != nil {
		writeDomainError(writer, err)
		return
	}
	if report.Appended > 0 {
		if err := server.afterWrite(request.Context()); err != nil {
			writeDomainError(writer, err)
			return
		}
	}
	writeJSON(writer, http.StatusOK, report)
}

func (server *Server) handleGitHubSCMStatus(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed")
		return
	}
	if server.githubSCMUnavailable(writer) {
		return
	}
	state, err := server.Project.Service.Status(request.Context())
	if err != nil {
		writeDomainError(writer, err)
		return
	}
	runtime, err := server.Project.Service.GitHubSCMRuntimeStatus(request.Context())
	if err != nil {
		writeDomainError(writer, err)
		return
	}
	writeJSON(writer, http.StatusOK, githubSCMProjection(state, runtime))
}

func githubSCMProjection(state model.ProjectState, runtime scmremote.RuntimeStatus) GitHubSCMStatusResponse {
	response := GitHubSCMStatusResponse{Runtime: runtime}
	remoteIDs := sortedMapKeys(state.SCMRemotes)
	if len(remoteIDs) == 1 {
		remote := state.SCMRemotes[remoteIDs[0]]
		response.Remote = &remote
	}
	for _, key := range sortedMapKeys(state.SCMRemoteRefs) {
		response.Refs = append(response.Refs, state.SCMRemoteRefs[key])
	}
	for _, key := range sortedMapKeys(state.SCMPullRequests) {
		pull := state.SCMPullRequests[key]
		response.PullRequests = append(response.PullRequests, GitHubPullProjection{
			Observation: pull, LocalCommitCount: localCommitMatches(state, pull), ConfirmedBindings: confirmedBindingMatches(state, pull),
		})
	}
	for _, key := range sortedMapKeys(state.SCMReviews) {
		response.Reviews = append(response.Reviews, state.SCMReviews[key])
	}
	for _, key := range sortedMapKeys(state.SCMChecks) {
		response.Checks = append(response.Checks, state.SCMChecks[key])
	}
	return response
}

func localCommitMatches(state model.ProjectState, pull model.SCMPullRequestObservation) int {
	remote, exists := state.SCMRemotes[pull.RemoteID]
	if !exists {
		return 0
	}
	count := 0
	for _, oid := range pullObservationOIDs(pull) {
		if _, exists := state.CommitObservations[model.SCMCommitKey(remote.RepositoryID, oid)]; exists {
			count++
		}
	}
	return count
}

func confirmedBindingMatches(state model.ProjectState, pull model.SCMPullRequestObservation) int {
	remote, exists := state.SCMRemotes[pull.RemoteID]
	if !exists {
		return 0
	}
	oids := make(map[string]struct{}, len(pull.CommitOIDs)+1)
	for _, oid := range pullObservationOIDs(pull) {
		oids[oid] = struct{}{}
	}
	count := 0
	for _, binding := range state.SCMBindings {
		if binding.RepositoryID == remote.RepositoryID && binding.Status == "confirmed" {
			if _, exists := oids[binding.CommitOID]; exists {
				count++
			}
		}
	}
	return count
}

func pullObservationOIDs(pull model.SCMPullRequestObservation) []string {
	oids := append([]string(nil), pull.CommitOIDs...)
	if pull.MergeCommitOID != "" {
		oids = append(oids, pull.MergeCommitOID)
	}
	sort.Strings(oids)
	return uniqueStrings(oids)
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	write := 1
	for read := 1; read < len(values); read++ {
		if values[read] == values[write-1] {
			continue
		}
		values[write] = values[read]
		write++
	}
	return values[:write]
}

func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
