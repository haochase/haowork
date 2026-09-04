package core

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/haochase/haowork/internal/agentteamsbridge"
	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/capsule"
	"github.com/haochase/haowork/internal/changes"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/executor"
	"github.com/haochase/haowork/internal/githubscm"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/scm"
	"github.com/haochase/haowork/internal/skillapi"
	"github.com/haochase/haowork/internal/team"
	"github.com/haochase/haowork/internal/teamapi"
	"github.com/haochase/haowork/internal/teamsync"
	"github.com/haochase/haowork/internal/trace"
	"github.com/haochase/haowork/internal/transfer"
)

type Dependencies struct {
	IDs        app.IDGenerator
	Clock      app.Clock
	TeamTokens teamapi.TokenSource
	CrossZone  *skillapi.CrossZoneConfig
	Transfer   *TransferConfig
	// AgentTeams configures the only production external executor path. A nil
	// value leaves the local project usable without an external deployment.
	AgentTeams   *agentteamsbridge.ProductionConfig
	GitHubTokens githubscm.TokenSource
	GitHubHTTP   *http.Client
}

// TransferConfig contains the environment-specific capabilities that core
// cannot safely infer from a portable transfer capsule.
type TransferConfig struct {
	TargetEnvironmentID    string
	PublicKeys             map[string]ed25519.PublicKey
	ExpectedGoalVersion    int
	ExpectedGitBaseline    string
	ExpectedContextHash    string
	ExpectedLeaseID        string
	ExpectedScope          []string
	RequiredSkills         map[string]string
	ReturnSigner           transfer.Signer
	RuntimeBindingResolver transfer.RuntimeBindingResolver
	ProvenanceVerifiers    transfer.ProvenanceVerifierSet
	NewEventID             func() string
}

type Project struct {
	Root     string
	Manifest capsule.Manifest
	Service  *app.Service
	Events   app.EventRepository
	Team     *Team
	Transfer *transfer.Service
	// SCMAvailable indicates that the project root is a local Git worktree.
	SCMAvailable bool
	// GitHubSCMAvailable indicates that this Git project can connect a read-only GitHub observer.
	GitHubSCMAvailable bool
	// SkillAdapters are project-owned runtime wiring; the MCP surface never opens event files.
	SkillAdapters skillapi.AdapterMap
}

// Team contains the runtime-only collaboration dependencies for a joined
// project. Credentials remain with the token source and are never persisted.
type Team struct {
	Root   string
	Config teamsync.ClientConfig
	Remote *teamapi.Client
	Sync   *teamsync.Engine
}

func (t *Team) Status(ctx context.Context) (team.Status, error) {
	if t == nil || t.Remote == nil {
		return team.Status{}, errors.New("Team collaboration is not configured")
	}
	return t.Remote.Status(ctx)
}
func (t *Team) SyncNow(ctx context.Context) (teamsync.SyncReport, error) {
	if t == nil || t.Sync == nil {
		return teamsync.SyncReport{}, errors.New("Team collaboration is not configured")
	}
	return t.Sync.Sync(ctx)
}
func (t *Team) Queue(ctx context.Context) ([]teamsync.OutboxEntry, error) {
	if t == nil {
		return nil, errors.New("Team collaboration is not configured")
	}
	return teamsync.NewOutbox(t.Root, t.Config.DeviceID).ReadAll(ctx)
}
func (t *Team) Conflicts(ctx context.Context) ([]model.Conflict, error) {
	if t == nil || t.Remote == nil {
		return nil, errors.New("Team collaboration is not configured")
	}
	return t.Remote.Conflicts(ctx)
}
func (t *Team) ResolveConflict(ctx context.Context, id, action string) (team.PushResult, error) {
	return t.ResolveConflictRequest(ctx, team.ConflictResolutionRequest{ConflictID: id, Action: action})
}
func (t *Team) ResolveConflictRequest(ctx context.Context, request team.ConflictResolutionRequest) (team.PushResult, error) {
	if t == nil || t.Remote == nil {
		return team.PushResult{}, errors.New("Team collaboration is not configured")
	}
	return t.Remote.ResolveConflictRequest(ctx, request.ConflictID, request)
}

func Open(ctx context.Context, start string, deps Dependencies) (Project, error) {
	if err := ctx.Err(); err != nil {
		return Project{}, err
	}
	root, err := capsule.Find(start)
	if err != nil {
		return Project{}, err
	}
	manifest, err := capsule.Load(root)
	if err != nil {
		return Project{}, err
	}
	events := app.EventRepository(eventstore.New(root))
	var joined *Team
	config, configured, err := findTeamConfig(ctx, root, manifest.ProjectID)
	if err != nil {
		return Project{}, err
	}
	if configured {
		remote, err := teamapi.NewClient(config.Endpoint, tokenSource(deps.TeamTokens), nil)
		if err != nil {
			return Project{}, err
		}
		accepted := eventstore.NewAt(
			filepath.Join(root, ".haowork", "team", "events.jsonl"),
			filepath.Join(root, ".haowork", "team", "events.lock"),
		)
		if err := ensureTeamAcceptedLog(root); err != nil {
			return Project{}, err
		}
		repository := teamsync.NewRepository(root, accepted, config, deps.Clock)
		events = repository
		joined = &Team{Root: root, Config: config, Remote: remote, Sync: teamsync.NewEngine(root, remote, accepted, config)}
	}
	service := app.NewWithWorkspaceScanner(
		manifest.ProjectID,
		manifest.CurrentGoalVersion,
		events,
		deps.IDs,
		deps.Clock,
		changes.Scanner{},
		root,
	)
	scmAvailable, err := configureSCM(root, service)
	if err != nil {
		return Project{}, err
	}
	githubSCMAvailable := false
	if scmAvailable {
		tokens := deps.GitHubTokens
		if tokens == nil {
			tokens = githubscm.EnvironmentTokenSource{}
		}
		clock := time.Now
		if deps.Clock != nil {
			clock = deps.Clock.Now
		}
		inspector := scm.NewInspector()
		observer := githubscm.NewObserver(githubscm.NewClient(tokens, deps.GitHubHTTP), githubscm.NewFileStore(root), inspector.Runner, root, clock)
		if err := service.ConfigureRemoteSCM(observer, root); err != nil {
			return Project{}, fmt.Errorf("configure GitHub SCM observation: %w", err)
		}
		githubSCMAvailable = true
	}
	adapters := skillapi.CoreAdapters(service)
	if deps.CrossZone != nil {
		adapters = skillapi.CoreAdaptersWithCrossZone(service, *deps.CrossZone)
	}
	if deps.AgentTeams != nil {
		production := *deps.AgentTeams
		production.RuntimeBindings = service
		production.Trace = trace.New(root)
		production.Mission = func(id string) (model.MissionEnvelope, error) {
			return service.Mission(context.Background(), id)
		}
		transport, err := agentteamsbridge.NewProductionTransport(production)
		if err != nil {
			return Project{}, fmt.Errorf("configure official AgentTeams bridge: %w", err)
		}
		service.ConfigureExecutorAdapter(executor.NewGovernedAgentTeamsAdapter(transport))
	}
	transferService, err := assembleTransfer(root, events, manifest.ProjectID, manifest.CurrentGoalVersion, deps, deps.Transfer)
	if err != nil {
		return Project{}, err
	}
	return Project{
		Root:               root,
		Manifest:           manifest,
		Service:            service,
		Events:             events,
		Team:               joined,
		Transfer:           transferService,
		SCMAvailable:       scmAvailable,
		GitHubSCMAvailable: githubSCMAvailable,
		SkillAdapters:      adapters,
	}, nil
}

func configureSCM(root string, service *app.Service) (bool, error) {
	_, err := os.Stat(filepath.Join(root, ".git"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect local Git metadata: %w", err)
	}
	if err := service.ConfigureSCM(scm.NewInspector(), root); err != nil {
		return false, fmt.Errorf("configure local Git inspection: %w", err)
	}
	return true, nil
}

type transferEventRepository interface {
	app.EventRepository
	AppendBatchIfUnchanged(context.Context, []model.Event, int) ([]model.Event, error)
}

type repositoryBatchSink struct{ events transferEventRepository }

func (sink repositoryBatchSink) AppendBatch(ctx context.Context, events []model.Event) error {
	history, err := sink.events.ReadAll(ctx)
	if err != nil {
		return err
	}
	_, err = sink.events.AppendBatchIfUnchanged(ctx, events, len(history))
	return err
}

func assembleTransfer(root string, events app.EventRepository, projectID string, goalVersion int, deps Dependencies, config *TransferConfig) (*transfer.Service, error) {
	if config == nil {
		return nil, nil
	}
	if config.TargetEnvironmentID == "" || len(config.PublicKeys) == 0 || config.ReturnSigner == nil || config.RuntimeBindingResolver == nil || config.ProvenanceVerifiers == nil || config.NewEventID == nil {
		return nil, errors.New("transfer configuration is incomplete")
	}
	repository, ok := events.(transferEventRepository)
	if !ok {
		return nil, errors.New("transfer event repository does not support atomic batches")
	}
	provenance := make(transfer.ProvenanceVerifierSet, len(config.ProvenanceVerifiers)+1)
	for source, verifier := range config.ProvenanceVerifiers {
		provenance[source] = verifier
	}
	provenance["trace-ledger"] = transfer.TraceStoreProvenanceVerifier{Store: trace.New(root)}
	writer := transfer.CoreTeamWriter{ProjectID: projectID, GoalVersion: goalVersion, Appender: repositoryBatchSink{events: repository}, NewEventID: config.NewEventID}
	if deps.Clock != nil {
		writer.Now = deps.Clock.Now
	}
	return &transfer.Service{
		TargetEnvironmentID:    config.TargetEnvironmentID,
		PublicKeys:             config.PublicKeys,
		ExpectedGoalVersion:    config.ExpectedGoalVersion,
		ExpectedGitBaseline:    config.ExpectedGitBaseline,
		ExpectedContextHash:    config.ExpectedContextHash,
		ExpectedLeaseID:        config.ExpectedLeaseID,
		ExpectedScope:          append([]string(nil), config.ExpectedScope...),
		RequiredSkills:         cloneRequiredSkills(config.RequiredSkills),
		Writer:                 writer,
		ReturnSigner:           config.ReturnSigner,
		RuntimeBindingResolver: config.RuntimeBindingResolver,
		ApprovalVerifier:       transfer.EventStoreApprovalVerifier{Events: events},
		ProvenanceVerifier:     provenance,
		Now:                    writer.Now,
	}, nil
}

func cloneRequiredSkills(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	for name, version := range values {
		result[name] = version
	}
	return result
}

func ensureTeamAcceptedLog(root string) error {
	path := filepath.Join(root, ".haowork", "team", "events.jsonl")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create Team accepted log directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create Team accepted log: %w", err)
	}
	if file != nil {
		if closeErr := file.Close(); closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func findTeamConfig(ctx context.Context, root, projectID string) (teamsync.ClientConfig, bool, error) {
	directory := filepath.Join(root, ".haowork", "local")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return teamsync.ClientConfig{}, false, nil
	}
	if err != nil {
		return teamsync.ClientConfig{}, false, err
	}

	var found teamsync.ClientConfig
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		config, err := teamsync.LoadConfig(ctx, root, entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return teamsync.ClientConfig{}, false, fmt.Errorf("load Team configuration for device %q: %w", entry.Name(), err)
		}
		if strings.TrimSpace(config.Endpoint) == "" || config.TeamProjectID != projectID {
			return teamsync.ClientConfig{}, false, fmt.Errorf("Team configuration for device %q does not match project %q", entry.Name(), projectID)
		}
		if found.DeviceID != "" {
			return teamsync.ClientConfig{}, false, errors.New("multiple Team device configurations require an explicit device selection")
		}
		found = config
	}
	return found, found.DeviceID != "", nil
}

type environmentTokenSource struct{}

func (environmentTokenSource) Token(context.Context) (string, error) {
	token := strings.TrimSpace(os.Getenv("HAOWORK_TEAM_TOKEN"))
	if token == "" {
		return "", errors.New("Team credential unavailable")
	}
	return token, nil
}

func tokenSource(source teamapi.TokenSource) teamapi.TokenSource {
	if source != nil {
		return source
	}
	return environmentTokenSource{}
}
