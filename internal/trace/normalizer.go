package trace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/model"
)

type MatrixEvent struct {
	ID, MissionID, TaskID, WorkItemID, RunID    string
	LogicalActorID                              string
	RuntimeBindingRevision                      int
	AgentFunction                               model.AgentFunction
	EnvironmentID, AgentTeamsInstanceID, RoomID string
	EventID, EventType, Cursor, Body            string
	ObservationSequence                         uint64
	OccurredAt                                  time.Time
}

// Normalizer persists Matrix references and a body digest; it never stores Matrix message bodies.
type Normalizer struct {
	Store Store
}

func (normalizer Normalizer) NormalizeMatrix(ctx context.Context, event MatrixEvent) (Envelope, error) {
	if normalizer.Store == nil {
		return Envelope{}, errors.New("trace store is required")
	}
	if strings.TrimSpace(event.EventType) == "" || strings.TrimSpace(event.EventID) == "" {
		return Envelope{}, errors.New("Matrix event id and type are required")
	}
	digest := sha256.Sum256([]byte(event.Body))
	record := Envelope{
		ID: event.ID, MissionID: event.MissionID, GovernanceTaskID: event.TaskID, WorkItemID: event.WorkItemID, RunID: event.RunID,
		LogicalActorID: event.LogicalActorID, RuntimeBindingRevision: event.RuntimeBindingRevision, AgentFunction: event.AgentFunction,
		EnvironmentID: event.EnvironmentID, AgentTeamsInstanceID: event.AgentTeamsInstanceID, RoomID: event.RoomID,
		SourceEventID: event.EventID, SourceEventType: event.EventType, SourceSystem: "matrix", Cursor: event.Cursor, ObservationSequence: event.ObservationSequence,
		SummarySHA256: hex.EncodeToString(digest[:]), Status: "observed", StartedAt: event.OccurredAt,
	}
	store, ok := normalizer.Store.(interface {
		AppendMatrixIdempotent(context.Context, Envelope) (Envelope, error)
	})
	if !ok {
		return Envelope{}, ErrMatrixAppendUnsupported
	}
	return store.AppendMatrixIdempotent(ctx, record)
}
