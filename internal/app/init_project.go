package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/haochase/haowork/internal/capsule"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/model"
)

type InitializeProjectInput struct {
	Root               string
	Name               string
	ProjectID          string
	Goal               string
	Invariants         []string
	CompletionCriteria []string
	Actor              model.Actor
}

func InitializeProject(
	ctx context.Context,
	input InitializeProjectInput,
	ids IDGenerator,
	clock Clock,
) (capsule.Manifest, error) {
	return initializeProject(ctx, input, ids, clock, func(root string) EventRepository {
		return eventstore.New(root)
	})
}

func initializeProject(
	ctx context.Context,
	input InitializeProjectInput,
	ids IDGenerator,
	clock Clock,
	repositoryFor func(string) EventRepository,
) (capsule.Manifest, error) {
	if err := ctx.Err(); err != nil {
		return capsule.Manifest{}, wrapOperational(err)
	}
	if strings.TrimSpace(input.Root) == "" || strings.TrimSpace(input.Name) == "" {
		return capsule.Manifest{}, errors.New("project root and name are required")
	}
	if err := validateActor(input.Actor); err != nil {
		return capsule.Manifest{}, err
	}
	if input.Actor.Kind != model.ActorHuman || input.Actor.Role != model.RoleOwner {
		return capsule.Manifest{}, ErrApprovalRequired
	}
	if strings.TrimSpace(input.Goal) == "" {
		return capsule.Manifest{}, errors.New("goal is required")
	}
	if !hasNonBlank(input.CompletionCriteria) || hasBlank(input.CompletionCriteria) {
		return capsule.Manifest{}, errors.New("at least one non-empty completion criterion is required")
	}
	if hasBlank(input.Invariants) {
		return capsule.Manifest{}, errors.New("invariant values must not be empty")
	}

	projectID := input.ProjectID
	var err error
	if projectID == "" {
		projectID, err = ids.New("PRJ")
		if err != nil {
			return capsule.Manifest{}, wrapOperational(err)
		}
	}
	if strings.TrimSpace(projectID) == "" {
		return capsule.Manifest{}, errors.New("project id is required")
	}
	eventID, err := ids.New("EVT")
	if err != nil {
		return capsule.Manifest{}, wrapOperational(err)
	}
	now := clock.Now()
	if now.IsZero() {
		return capsule.Manifest{}, wrapOperational(errors.New("clock returned zero time"))
	}
	goal := model.GoalVersion{
		Version: 1, Statement: input.Goal,
		Invariants:         append([]string(nil), input.Invariants...),
		CompletionCriteria: append([]string(nil), input.CompletionCriteria...),
	}
	payload, err := json.Marshal(model.ProjectInitialized{Name: input.Name, Goal: goal})
	if err != nil {
		return capsule.Manifest{}, err
	}
	event := model.Event{
		ID: eventID, Type: "project.initialized", ProjectID: projectID, GoalVersion: 1,
		AggregateType: "project", AggregateID: projectID, Actor: input.Actor,
		OccurredAt: now.UTC(), Payload: payload,
	}

	manifest, err := capsule.Init(input.Root, capsule.InitInput{
		ProjectID: projectID, Name: input.Name, ActorID: input.Actor.ID, CreatedAt: now,
	})
	if err != nil {
		if errors.Is(err, capsule.ErrAlreadyExists) {
			return capsule.Manifest{}, err
		}
		return capsule.Manifest{}, wrapOperational(err)
	}
	if _, err := repositoryFor(input.Root).Append(ctx, event); err != nil {
		cleanupErr := os.RemoveAll(filepath.Join(input.Root, ".haowork"))
		if cleanupErr != nil {
			cleanupErr = fmt.Errorf("remove newly created capsule: %w", cleanupErr)
		}
		return capsule.Manifest{}, errors.Join(wrapOperational(err), wrapOperational(cleanupErr))
	}
	return manifest, nil
}
