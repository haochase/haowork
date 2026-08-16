// haowork-mcp exposes the governed, canonical Haowork skill registry to
// AgentTeams through a deliberately narrow MCP endpoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/corebridge"
	"github.com/haochase/haowork/internal/idgen"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/skillapi"
	"github.com/haochase/haowork/internal/skillruntime"
	"github.com/haochase/haowork/internal/trace"
)

const (
	envListenAddress = "HAOWORK_MCP_LISTEN_ADDR"
	envProject       = "HAOWORK_MCP_PROJECT"
	envSkillsDir     = "HAOWORK_MCP_SKILLS_DIR"
	envBindingsFile  = "HAOWORK_MCP_BINDINGS_FILE"
	envBridgeState   = "HAOWORK_MCP_USE_CORE_BRIDGE_STATE"
	envRoutePath     = "HAOWORK_MCP_ROUTE_PATH"
)

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv); err != nil {
		fmt.Fprintln(os.Stderr, "haowork-mcp:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, getenv func(string) string) error {
	if getenv == nil {
		return errors.New("environment reader is required")
	}
	listenAddress, err := requiredEnvironment(getenv, envListenAddress)
	if err != nil {
		return err
	}
	projectPath, err := requiredEnvironment(getenv, envProject)
	if err != nil {
		return err
	}
	bindingsPath, err := requiredEnvironment(getenv, envBindingsFile)
	if err != nil {
		return err
	}
	project, err := core.Open(ctx, projectPath, core.Dependencies{IDs: idgen.New(), Clock: systemClock{}})
	if err != nil {
		return fmt.Errorf("open Haowork project: %w", err)
	}
	skillsDir := strings.TrimSpace(getenv(envSkillsDir))
	if skillsDir == "" {
		skillsDir = filepath.Join(project.Root, "skills")
	}
	registry, err := skillruntime.Load(skillsDir)
	if err != nil {
		return fmt.Errorf("load canonical skills: %w", err)
	}
	authenticator, err := skillapi.LoadRuntimeConsumerAuthenticator(bindingsPath)
	if err != nil {
		return err
	}
	var state skillruntime.StateReader = skillruntime.StateReaderFunc(project.Service.Snapshot)
	if strings.EqualFold(strings.TrimSpace(getenv(envBridgeState)), "true") {
		bridgeState, err := corebridge.OpenState(project.Root)
		if err != nil {
			return fmt.Errorf("open Core Bridge state: %w", err)
		}
		state = bridgeState
	}
	store := trace.New(project.Root)
	runtime := &skillruntime.Runtime{
		Policy:  skillruntime.Policy{Registry: registry, State: state, Clock: systemClock{}},
		Adapter: project.SkillAdapters,
		Audit:   traceAudit{store: store, state: state, clock: systemClock{}},
		Tracer:  trace.RuntimeTracer{Store: store, Candidates: unavailableCandidateSink{}, Clock: systemClock{}},
	}
	server := &skillapi.Server{
		Registry: registry, Runtime: runtime, BindingReader: coreStateBindingReader{state: state},
		Authenticator: authenticator, MaxBodyBytes: 1 << 20,
	}
	host, err := skillapi.NewHost(skillapi.HostConfig{ListenAddress: listenAddress, RoutePath: getenv(envRoutePath), Server: server})
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "haowork-mcp listening on", host.ListenAddress())
	return host.Serve(ctx)
}

func requiredEnvironment(getenv func(string) string, name string) (string, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

// coreStateBindingReader allows only the currently registered Core work item
// to reach the policy engine. Policy performs the subsequent lease, scope,
// skill, approval, and input-hash checks.
type coreStateBindingReader struct{ state skillruntime.StateReader }

func (reader coreStateBindingReader) ValidateInvocation(ctx context.Context, invocation skillruntime.Invocation) error {
	if reader.state == nil {
		return skillapi.ErrBindingNotFound
	}
	state, err := reader.state.State(ctx)
	if err != nil {
		return err
	}
	mission, exists := state.Missions[invocation.MissionID]
	if !exists || mission.GoalVersion != invocation.GoalVersion || mission.ContextID != invocation.ContextID || mission.ContextHash != invocation.ContextHash || mission.EnvironmentID != invocation.EnvironmentID {
		return skillapi.ErrBindingNotFound
	}
	if !containsTask(mission.GovernanceTaskIDs, invocation.TaskID) || invocation.WorkItemID != invocation.TaskID {
		return skillapi.ErrBindingNotFound
	}
	task, exists := state.Tasks[invocation.TaskID]
	if !exists || task.ID != invocation.TaskID || task.GoalVersion != invocation.GoalVersion {
		return skillapi.ErrBindingNotFound
	}
	run, exists := state.Runs[invocation.RunID]
	if !exists || run.ID != invocation.RunID || run.TaskID != invocation.TaskID || run.GoalVersion != invocation.GoalVersion || run.ContextID != invocation.ContextID || run.ContextHash != invocation.ContextHash || run.Status != model.StatusRunning {
		return skillapi.ErrBindingNotFound
	}
	agent, exists := state.Agents[invocation.LogicalActorID]
	if !exists || agent.ID != invocation.LogicalActorID || agent.SubjectKind != model.ActorAgent || agent.GovernanceRole != model.RoleAgent || agent.Status != "active" || mission.RoleAssignments[agent.Function] != agent.ID {
		return skillapi.ErrBindingNotFound
	}
	bindings := state.RuntimeBindings[agent.ID]
	matches := 0
	for _, binding := range bindings {
		if binding.Status == "active" && binding.LogicalActorID == agent.ID && binding.Revision == invocation.RuntimeBindingRevision && binding.EnvironmentID == invocation.EnvironmentID && binding.RuntimePrincipalID == invocation.RuntimePrincipalID && binding.AgentTeamsInstanceID == invocation.AgentTeamsInstanceID {
			matches++
		}
	}
	if matches != 1 {
		return skillapi.ErrBindingNotFound
	}
	return nil
}

func containsTask(tasks []string, taskID string) bool {
	for _, candidate := range tasks {
		if candidate == taskID {
			return true
		}
	}
	return false
}

// traceAudit creates a separate immutable observation for the audit sink.
// RuntimeTracer has already established the invocation root before this call.
type traceAudit struct {
	store trace.Store
	state skillruntime.StateReader
	clock skillruntime.Clock
}

func (audit traceAudit) RecordSkillCall(ctx context.Context, invocation skillruntime.Invocation, result skillruntime.Result) error {
	if audit.store == nil || audit.state == nil || audit.clock == nil {
		return errors.New("trace audit dependencies are required")
	}
	state, err := audit.state.State(ctx)
	if err != nil {
		return err
	}
	agent, exists := state.Agents[invocation.LogicalActorID]
	if !exists {
		return skillapi.ErrBindingNotFound
	}
	now := audit.clock.Now().UTC()
	_, err = audit.store.AppendIdempotent(ctx, trace.Envelope{
		ID: invocation.TraceID + ":" + invocation.ID + ":skill.audit.persisted", InvocationID: invocation.ID,
		MissionID: invocation.MissionID, GovernanceTaskID: invocation.TaskID, WorkItemID: invocation.WorkItemID, RunID: invocation.RunID,
		LogicalActorID: invocation.LogicalActorID, RuntimeBindingRevision: invocation.RuntimeBindingRevision, AgentFunction: agent.Function,
		EnvironmentID: invocation.EnvironmentID, AgentTeamsInstanceID: invocation.AgentTeamsInstanceID,
		SourceEventID: invocation.ID + ":skill.audit.persisted", SourceEventType: "skill.audit.persisted", ParentTraceID: invocation.TraceID,
		SkillName: invocation.SkillName, SkillVersion: invocation.SkillVersion, InputSHA256: invocation.InputSHA256, OutputSHA256: result.OutputSHA256,
		ArtifactRefs: append([]model.ArtifactRef(nil), result.Artifacts...), Status: result.Status, ErrorCode: result.ErrorCode, StartedAt: now, FinishedAt: now,
	})
	return err
}

// Candidate promotion is deliberately unavailable until Task 5 wires the
// authenticated Team Core authorization endpoint. Trace events are retained,
// but no skill call can promote an evidence or approval fact locally.
type unavailableCandidateSink struct{}

func (unavailableCandidateSink) SubmitCandidate(context.Context, trace.PromotionCandidate) error {
	return errors.New("Team Core promotion authorization is not configured")
}
