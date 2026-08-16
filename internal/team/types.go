package team

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/model"
)

const (
	PushAccepted = "Accepted"
	PushRejected = "Rejected"
	PushConflict = "Conflict"

	CodeStaleBaseline = "stale_baseline"
	CodeBatchConflict = "batch_conflict"
)

var (
	ErrUnauthorized      = errors.New("team authorization denied")
	ErrLeaseRequired     = errors.New("active assigned lease is required")
	ErrInvalidBatch      = errors.New("invalid team push batch")
	ErrBatchConflict     = errors.New("team batch id conflicts with accepted history")
	ErrNotWritable       = errors.New("team service is not writable")
	ErrHistoryDivergent  = errors.New("accepted and canonical histories diverge")
	ErrMaterialization   = errors.New("accepted facts could not be materialized")
	ErrPrincipalMismatch = errors.New("event sync claims do not match principal")
)

// Principal is the authenticated caller identity used to verify every event
// in a push batch before it can enter the accepted log.
type Principal struct {
	AuthenticatedPrincipal string
	Actor                  model.Actor
	DeviceID               string
	EnvironmentID          string
	FunctionalIdentity     string
	AllowedSkills          []string
}

type PushBatch struct {
	BatchID     string        `json:"batch_id"`
	BaseTeamSeq uint64        `json:"base_team_seq"`
	Events      []model.Event `json:"events"`
}

type PushResult struct {
	Status       string        `json:"status"`
	TeamSeqFrom  uint64        `json:"team_seq_from"`
	TeamSeqTo    uint64        `json:"team_seq_to"`
	Materialized bool          `json:"materialized"`
	ConflictID   string        `json:"conflict_id,omitempty"`
	Code         string        `json:"code,omitempty"`
	Message      string        `json:"message,omitempty"`
	Events       []model.Event `json:"events,omitempty"`
}

type Status struct {
	ProjectID           string           `json:"project_id"`
	TeamSeq             uint64           `json:"team_seq"`
	Writable            bool             `json:"writable"`
	MaterializedThrough uint64           `json:"materialized_through"`
	GoalVersion         int              `json:"goal_version"`
	Principal           Principal        `json:"principal"`
	ActiveLeases        []model.Lease    `json:"active_leases"`
	OpenConflicts       []model.Conflict `json:"open_conflicts"`
}

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	New(prefix string) (string, error)
}

// IndexRebuilder is intentionally narrow because the local index is a
// rebuildable runtime cache, not a second source of truth.
type IndexRebuilder interface {
	Rebuild(context.Context, []model.Event) error
}

// Materializer applies the accepted log to the portable canonical log and its
// derived runtime index. Implementations must be idempotent over a full
// accepted history.
type Materializer interface {
	Materialize(context.Context, []model.Event) error
	Recover(context.Context, []model.Event) error
	MaterializedThrough(context.Context) (uint64, error)
}

// DetectedConflict is a non-operational conflict result. Task 7 can inject a
// richer detector while preserving the result contract used by Push.
type DetectedConflict struct {
	ID      string
	Code    string
	Message string
}

type ConflictDetector interface {
	Detect(context.Context, model.ProjectState, PushBatch, uint64) (*DetectedConflict, error)
}

type Dependencies struct {
	AcceptedStore     eventstore.Store
	MaterializedStore eventstore.Store
	Clock             Clock
	IDs               IDGenerator
	Index             IndexRebuilder
	Materializer      Materializer
	ConflictDetector  ConflictDetector
}

type Service struct {
	root             string
	accepted         eventstore.Store
	materialized     eventstore.Store
	materializer     Materializer
	clock            Clock
	ids              IDGenerator
	conflictDetector ConflictDetector

	mutationMu sync.Mutex
	writable   bool
}
