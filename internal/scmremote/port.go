package scmremote

import (
	"context"
	"time"

	"github.com/haochase/haowork/internal/model"
)

type PreparedConnect struct {
	Remote      model.SCMRemote
	CommitToken string
}

type PreparedSync struct {
	Remote       model.SCMRemote
	Refs         []model.SCMRemoteRefObservation
	PullRequests []model.SCMPullRequestObservation
	Reviews      []model.SCMReviewObservation
	Checks       []model.SCMCheckObservation
	Runtime      RuntimeStatus
	CommitToken  string
}

type RuntimeStatus struct {
	Connected          bool      `json:"connected"`
	Authenticated      bool      `json:"authenticated"`
	LastSuccessfulSync time.Time `json:"last_successful_sync,omitempty"`
	RetryAt            time.Time `json:"retry_at,omitempty"`
	RateLimitRemaining int       `json:"rate_limit_remaining"`
}

type Observer interface {
	PrepareConnect(context.Context, string, model.SCMRepository) (PreparedConnect, error)
	CommitConnect(context.Context, string) error
	PrepareSync(context.Context, string) (PreparedSync, error)
	CommitSync(context.Context, string) error
	Abort(context.Context, string)
	RuntimeStatus(context.Context) (RuntimeStatus, error)
}
