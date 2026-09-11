package higressbinding

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/haochase/haowork/internal/agentteamsbinding"
)

const FixedMCPServerName = "haowork-mcp"

var (
	ErrInvalidConfig      = errors.New("invalid Higress binding publisher configuration")
	ErrInvalidBindingPath = errors.New("invalid runtime binding path")
	ErrInvalidBinding     = errors.New("invalid runtime binding")
)

type Config struct {
	ConsoleURL           string
	Username             string
	Password             string
	MCPServerName        string
	BindingsPath         string
	StatePath            string
	PollInterval         time.Duration
	RetryInterval        time.Duration
	MaxAttempts          int
	MaxBodyBytes         int64
	EnvironmentID        string
	AgentTeamsInstanceID string
	MatrixServer         string
	AllowedConsoleHosts  []string
	AllowLoopback        bool
}

type Console interface {
	Consumers(context.Context, string) ([]string, error)
	AddConsumers(context.Context, string, []string) error
	DeleteConsumers(context.Context, string, []string) error
}

type ManagedState interface {
	Load(context.Context) ([]string, error)
	Save(context.Context, []string) error
}

type ManagedJournal struct {
	Confirmed     []string
	PendingAdd    []string
	PendingDelete []string
}

type ManagedStateJournal interface {
	LoadJournal(context.Context) (ManagedJournal, error)
	SaveJournal(context.Context, ManagedJournal) error
}

type ManagedStateLocker interface {
	Lock(context.Context) (func() error, error)
}

type Publisher struct {
	Config  Config
	Console Console
	State   ManagedState
	Now     func() time.Time
}

func (publisher Publisher) Reconcile(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := publisher.validate(); err != nil {
		return err
	}
	if locker, ok := publisher.State.(ManagedStateLocker); ok {
		unlock, err := locker.Lock(ctx)
		if err != nil {
			return fmt.Errorf("lock managed Higress state: %w", err)
		}
		defer unlock()
	}
	document, err := publisher.readBindingDocument()
	if err != nil {
		return err
	}
	bound := make([]string, 0, len(document.Bindings))
	for _, entry := range document.Bindings {
		if !validDeliveryLeaderBinding(entry) {
			return fmt.Errorf("%w: consumer does not match the authenticated runtime principal", ErrInvalidBinding)
		}
		if publisher.Config.EnvironmentID != "" && entry.Principal.EnvironmentID != publisher.Config.EnvironmentID {
			return fmt.Errorf("%w: binding environment does not match publisher zone", ErrInvalidBinding)
		}
		if publisher.Config.AgentTeamsInstanceID != "" && entry.Principal.AgentTeamsInstanceID != publisher.Config.AgentTeamsInstanceID {
			return fmt.Errorf("%w: binding AgentTeams instance does not match publisher zone", ErrInvalidBinding)
		}
		if publisher.Config.MatrixServer != "" && !strings.HasSuffix(entry.Principal.RuntimePrincipalID, ":"+publisher.Config.MatrixServer) {
			return fmt.Errorf("%w: binding Matrix server does not match publisher", ErrInvalidBinding)
		}
		bound = append(bound, entry.ConsumerName)
	}
	journal, err := publisher.loadJournal(ctx)
	if err != nil {
		return fmt.Errorf("load managed Higress consumers: %w", err)
	}
	managed, err := normalizeManagedConsumers(journal.Confirmed)
	if err != nil {
		return err
	}
	desired, err := normalizeManagedConsumers(bound)
	if err != nil {
		return err
	}
	current, err := publisher.readConsumers(ctx)
	if err != nil {
		return err
	}
	managedAfterAdd, err := normalizeManagedConsumers(union(managed, desired))
	if err != nil {
		return err
	}
	toAdd := difference(desired, current)
	ownershipConfirmed := containsAll(managed, desired)
	if len(toAdd) > 0 {
		if err := publisher.saveJournal(ctx, ManagedJournal{Confirmed: managed, PendingAdd: toAdd, PendingDelete: difference(managed, desired)}); err != nil {
			return fmt.Errorf("save Higress mutation intent: %w", err)
		}
		mutationErr := publisher.retry(ctx, func() error {
			return publisher.Console.AddConsumers(ctx, FixedMCPServerName, toAdd)
		})
		current, err = publisher.readConsumers(ctx)
		if err != nil {
			return err
		}
		if !containsAll(current, desired) {
			if mutationErr != nil {
				return fmt.Errorf("add Higress MCP consumers: %w", mutationErr)
			}
			return errors.New("Higress MCP consumers did not converge after add")
		}
		ownershipConfirmed = true
	}
	if !ownershipConfirmed && containsAll(current, desired) && containsAll(journal.PendingAdd, desired) {
		ownershipConfirmed = true
	}
	toDelete := intersection(difference(managedAfterAdd, desired), current)
	if len(toDelete) > 0 {
		if ownershipConfirmed {
			if err := publisher.saveJournal(ctx, ManagedJournal{Confirmed: managedAfterAdd, PendingDelete: toDelete}); err != nil {
				return fmt.Errorf("save confirmed Higress ownership: %w", err)
			}
		}
		mutationErr := publisher.retry(ctx, func() error {
			return publisher.Console.DeleteConsumers(ctx, FixedMCPServerName, toDelete)
		})
		current, err = publisher.readConsumers(ctx)
		if err != nil {
			return err
		}
		if intersects(current, toDelete) {
			if mutationErr != nil {
				return fmt.Errorf("delete Higress MCP consumers: %w", mutationErr)
			}
			return errors.New("Higress MCP consumers did not converge after delete")
		}
	}
	current, err = publisher.readConsumers(ctx)
	if err != nil {
		return err
	}
	if !containsAll(current, desired) || intersects(current, difference(managedAfterAdd, desired)) {
		return errors.New("Higress MCP consumer convergence verification failed")
	}
	if ownershipConfirmed && !equalStrings(managed, desired) {
		if err := publisher.saveJournal(ctx, ManagedJournal{Confirmed: desired}); err != nil {
			return fmt.Errorf("save managed Higress consumers: %w", err)
		}
	}
	return nil
}

func (publisher Publisher) loadJournal(ctx context.Context) (ManagedJournal, error) {
	if journaler, ok := publisher.State.(ManagedStateJournal); ok {
		journal, err := journaler.LoadJournal(ctx)
		if err != nil {
			return ManagedJournal{}, err
		}
		return journal, nil
	}
	managed, err := publisher.State.Load(ctx)
	return ManagedJournal{Confirmed: managed}, err
}

func (publisher Publisher) saveJournal(ctx context.Context, journal ManagedJournal) error {
	journal.Confirmed, _ = normalizeManagedConsumers(journal.Confirmed)
	journal.PendingAdd, _ = normalizeManagedConsumers(journal.PendingAdd)
	journal.PendingDelete, _ = normalizeManagedConsumers(journal.PendingDelete)
	if journaler, ok := publisher.State.(ManagedStateJournal); ok {
		return journaler.SaveJournal(ctx, journal)
	}
	return publisher.State.Save(ctx, journal.Confirmed)
}

func (publisher Publisher) validate() error {
	if publisher.Console == nil || publisher.State == nil || strings.TrimSpace(publisher.Config.BindingsPath) == "" || publisher.Config.MCPServerName != FixedMCPServerName {
		return ErrInvalidConfig
	}
	if publisher.Config.MaxAttempts < 0 || publisher.Config.MaxAttempts > 10 || publisher.Config.RetryInterval < 0 {
		return ErrInvalidConfig
	}
	if publisher.Now != nil && publisher.Now().IsZero() {
		return ErrInvalidConfig
	}
	return nil
}

func (publisher Publisher) readConsumers(ctx context.Context) ([]string, error) {
	var values []string
	err := publisher.retry(ctx, func() error {
		var err error
		values, err = publisher.Console.Consumers(ctx, FixedMCPServerName)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("read Higress MCP consumers: %w", err)
	}
	values, err = normalizeConsumers(values)
	if err != nil {
		return nil, err
	}
	return values, nil
}

func (publisher Publisher) retry(ctx context.Context, operation func() error) error {
	attempts := publisher.Config.MaxAttempts
	if attempts <= 0 {
		attempts = 3
	}
	interval := publisher.Config.RetryInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := operation(); err == nil {
			return nil
		} else {
			last = err
			if !retryable(err) {
				return err
			}
		}
		if attempt+1 == attempts {
			break
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return last
}

type consoleHTTPError struct{ status int }

func (err consoleHTTPError) Error() string {
	return fmt.Sprintf("Higress Console request returned HTTP %d", err.status)
}

func retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var protocol consoleProtocolError
	if errors.As(err, &protocol) {
		return false
	}
	var status consoleHTTPError
	if errors.As(err, &status) {
		return status.status >= 500
	}
	return true
}

type consoleProtocolError struct{ err error }

func (err consoleProtocolError) Error() string { return err.err.Error() }
func (err consoleProtocolError) Unwrap() error { return err.err }

func (publisher Publisher) readBindingDocument() (agentteamsbinding.BindingDocument, error) {
	path, err := safeBindingPath(publisher.Config.BindingsPath)
	if err != nil {
		return agentteamsbinding.BindingDocument{}, err
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return agentteamsbinding.BindingDocument{}, ErrInvalidBindingPath
	}
	file, err := os.Open(path)
	if err != nil {
		return agentteamsbinding.BindingDocument{}, fmt.Errorf("open runtime binding document: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return agentteamsbinding.BindingDocument{}, ErrInvalidBindingPath
	}
	content, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		return agentteamsbinding.BindingDocument{}, fmt.Errorf("read runtime binding document: %w", err)
	}
	after, err := os.Lstat(path)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return agentteamsbinding.BindingDocument{}, ErrInvalidBindingPath
	}
	document, err := agentteamsbinding.DecodeBindingDocument(bytes.NewReader(content))
	if err != nil {
		return agentteamsbinding.BindingDocument{}, err
	}
	now := time.Now().UTC()
	if publisher.Now != nil {
		now = publisher.Now().UTC()
	}
	if err := validateBindingDocumentStructure(document, now); err != nil {
		return agentteamsbinding.BindingDocument{}, err
	}
	normalized, err := agentteamsbinding.NormalizeBindingDocument(document, now, agentteamsbinding.DropExpiredBindings)
	if err != nil {
		return agentteamsbinding.BindingDocument{}, err
	}
	return normalized, nil
}

func validateBindingDocumentStructure(document agentteamsbinding.BindingDocument, now time.Time) error {
	canonical := agentteamsbinding.BindingDocument{SchemaVersion: document.SchemaVersion, Bindings: make([]agentteamsbinding.BindingDocumentEntry, 0, len(document.Bindings))}
	for _, entry := range document.Bindings {
		observed, err := time.Parse(time.RFC3339Nano, entry.ObservedAt)
		if err != nil || observed.Location() != time.UTC || observed.Format(time.RFC3339Nano) != entry.ObservedAt {
			return errors.New("invalid runtime consumer binding lifetime")
		}
		single, err := agentteamsbinding.NormalizeBindingDocument(agentteamsbinding.BindingDocument{
			SchemaVersion: document.SchemaVersion, Bindings: []agentteamsbinding.BindingDocumentEntry{entry},
		}, observed, agentteamsbinding.RejectExpiredBindings)
		if err != nil {
			return err
		}
		validated := single.Bindings[0]
		validated.ObservedAt = now.Format(time.RFC3339Nano)
		validated.ExpiresAt = now.Add(time.Minute).Format(time.RFC3339Nano)
		canonical.Bindings = append(canonical.Bindings, validated)
	}
	_, err := agentteamsbinding.NormalizeBindingDocument(canonical, now, agentteamsbinding.RejectExpiredBindings)
	return err
}

func safeBindingPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, '\x00') {
		return "", ErrInvalidBindingPath
	}
	for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return "", ErrInvalidBindingPath
		}
	}
	clean := filepath.Clean(value)
	if !filepath.IsAbs(clean) || filepath.Base(clean) != "mcp-bindings.json" {
		return "", ErrInvalidBindingPath
	}
	evaluated, err := filepath.EvalSymlinks(clean)
	if err != nil || !samePath(clean, evaluated) {
		return "", ErrInvalidBindingPath
	}
	info, err := os.Lstat(clean)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", ErrInvalidBindingPath
	}
	return clean, nil
}

func samePath(left, right string) bool {
	left, leftErr := filepath.Abs(left)
	right, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	if os.PathSeparator == '\\' {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func validDeliveryLeaderBinding(entry agentteamsbinding.BindingDocumentEntry) bool {
	separator := strings.IndexByte(entry.Principal.RuntimePrincipalID, ':')
	if separator < 2 || entry.Principal.RuntimePrincipalID[0] != '@' {
		return false
	}
	if entry.ConsumerName != "worker-"+entry.Principal.RuntimePrincipalID[1:separator] || !validManagedConsumer(entry.ConsumerName) {
		return false
	}
	if entry.Function != "" && entry.Function != "delivery_leader" {
		return false
	}
	return true
}

func validManagedConsumer(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "worker-") && dnsSubdomain(value)
}

func normalizeManagedConsumers(values []string) ([]string, error) {
	normalized, err := normalizeConsumers(values)
	if err != nil {
		return nil, err
	}
	for _, value := range normalized {
		if !validManagedConsumer(value) {
			return nil, errors.New("managed Higress consumer is outside Haowork ownership")
		}
	}
	return normalized, nil
}

func normalizeConsumers(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || !dnsSubdomain(value) {
			return nil, fmt.Errorf("invalid Higress consumer name")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func dnsSubdomain(value string) bool {
	if value == "" || len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func contains(values []string, target string) bool {
	index := sort.SearchStrings(values, target)
	return index < len(values) && values[index] == target
}

func containsAll(values, expected []string) bool {
	for _, value := range expected {
		if !contains(values, value) {
			return false
		}
	}
	return true
}

func union(left, right []string) []string {
	return append(append([]string(nil), left...), right...)
}

func difference(left, right []string) []string {
	result := make([]string, 0, len(left))
	for _, value := range left {
		if !contains(right, value) {
			result = append(result, value)
		}
	}
	return result
}

func intersection(left, right []string) []string {
	result := make([]string, 0, len(left))
	for _, value := range left {
		if contains(right, value) {
			result = append(result, value)
		}
	}
	return result
}

func intersects(left, right []string) bool {
	return len(intersection(left, right)) > 0
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type FileManagedState struct {
	path          string
	directoryInfo os.FileInfo
}

func (state *FileManagedState) Lock(ctx context.Context) (func() error, error) {
	if state == nil || state.path == "" {
		return nil, ErrInvalidConfig
	}
	if err := state.validatePath(); err != nil {
		return nil, err
	}
	lock := flock.New(state.path + ".lock")
	locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, context.Canceled
	}
	if err := state.validatePath(); err != nil {
		_ = lock.Unlock()
		return nil, err
	}
	return lock.Unlock, nil
}

func (state *FileManagedState) validatePath() error {
	if state == nil || state.path == "" {
		return ErrInvalidConfig
	}
	directory := filepath.Dir(state.path)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrInvalidBindingPath
	}
	if state.directoryInfo != nil {
		current, statErr := os.Stat(directory)
		if statErr != nil || !os.SameFile(state.directoryInfo, current) {
			return ErrInvalidBindingPath
		}
	}
	evaluated, err := filepath.EvalSymlinks(directory)
	if err != nil || !samePath(directory, evaluated) {
		return ErrInvalidBindingPath
	}
	if file, err := os.Lstat(state.path); err == nil {
		if file.Mode()&os.ModeSymlink != 0 || !file.Mode().IsRegular() {
			return ErrInvalidBindingPath
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrInvalidBindingPath
	}
	return nil
}

type managedStateDocument struct {
	SchemaVersion    int      `json:"schema_version"`
	ManagedConsumers []string `json:"managed_consumers"`
	PendingAdd       []string `json:"pending_add,omitempty"`
	PendingDelete    []string `json:"pending_delete,omitempty"`
}

const managedStateSchemaVersion = 2

func NewFileManagedState(path string) (*FileManagedState, error) {
	path = strings.TrimSpace(path)
	for _, part := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return nil, ErrInvalidBindingPath
		}
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || filepath.Base(path) != "managed-consumers.json" || strings.ContainsRune(path, '\x00') {
		return nil, ErrInvalidBindingPath
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, errors.New("create managed consumer state directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil && runtime.GOOS != "windows" {
		return nil, errors.New("protect managed consumer state directory")
	}
	evaluated, err := filepath.EvalSymlinks(directory)
	if err != nil || !samePath(directory, evaluated) {
		return nil, ErrInvalidBindingPath
	}
	if info, err := os.Lstat(path); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return nil, ErrInvalidBindingPath
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, ErrInvalidBindingPath
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		return nil, ErrInvalidBindingPath
	}
	return &FileManagedState{path: path, directoryInfo: directoryInfo}, nil
}

func (state *FileManagedState) Load(ctx context.Context) ([]string, error) {
	journal, err := state.LoadJournal(ctx)
	return journal.Confirmed, err
}

func (state *FileManagedState) LoadJournal(ctx context.Context) (ManagedJournal, error) {
	if state == nil || state.path == "" {
		return ManagedJournal{}, ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return ManagedJournal{}, err
	}
	if err := state.validatePath(); err != nil {
		return ManagedJournal{}, err
	}
	before, err := os.Lstat(state.path)
	if errors.Is(err, os.ErrNotExist) {
		return ManagedJournal{}, nil
	}
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return ManagedJournal{}, errors.New("open managed consumer state")
	}
	file, err := os.Open(state.path)
	if err != nil {
		return ManagedJournal{}, errors.New("open managed consumer state")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return ManagedJournal{}, ErrInvalidBindingPath
	}
	content, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(content) > 1<<20 {
		return ManagedJournal{}, errors.New("read managed consumer state")
	}
	after, err := os.Lstat(state.path)
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return ManagedJournal{}, ErrInvalidBindingPath
	}
	var document managedStateDocument
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return ManagedJournal{}, errors.New("invalid managed consumer state")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ManagedJournal{}, errors.New("invalid managed consumer state")
	}
	if document.SchemaVersion == 1 {
		return ManagedJournal{}, nil
	}
	if document.SchemaVersion != managedStateSchemaVersion {
		return ManagedJournal{}, errors.New("unsupported managed consumer state version")
	}
	confirmed, err := normalizeManagedConsumers(document.ManagedConsumers)
	if err != nil {
		return ManagedJournal{}, err
	}
	pendingAdd, err := normalizeManagedConsumers(document.PendingAdd)
	if err != nil {
		return ManagedJournal{}, err
	}
	pendingDelete, err := normalizeManagedConsumers(document.PendingDelete)
	if err != nil {
		return ManagedJournal{}, err
	}
	return ManagedJournal{Confirmed: confirmed, PendingAdd: pendingAdd, PendingDelete: pendingDelete}, nil
}

func (state *FileManagedState) Save(ctx context.Context, consumers []string) error {
	return state.SaveJournal(ctx, ManagedJournal{Confirmed: consumers})
}

func (state *FileManagedState) SaveJournal(ctx context.Context, journal ManagedJournal) error {
	if state == nil || state.path == "" {
		return ErrInvalidConfig
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := state.validatePath(); err != nil {
		return err
	}
	consumers, err := normalizeManagedConsumers(journal.Confirmed)
	if err != nil {
		return err
	}
	pendingAdd, err := normalizeManagedConsumers(journal.PendingAdd)
	if err != nil {
		return err
	}
	pendingDelete, err := normalizeManagedConsumers(journal.PendingDelete)
	if err != nil {
		return err
	}
	content, err := json.Marshal(managedStateDocument{SchemaVersion: managedStateSchemaVersion, ManagedConsumers: consumers, PendingAdd: pendingAdd, PendingDelete: pendingDelete})
	if err != nil {
		return errors.New("encode managed consumer state")
	}
	temporary, err := os.CreateTemp(filepath.Dir(state.path), ".managed-consumers-*.tmp")
	if err != nil {
		return errors.New("create managed consumer state replacement")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := state.validatePath(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return errors.New("protect managed consumer state replacement")
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return errors.New("write managed consumer state replacement")
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return errors.New("sync managed consumer state replacement")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close managed consumer state replacement")
	}
	if err := state.validatePath(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, state.path); err != nil {
		return errors.New("replace managed consumer state")
	}
	if directory, err := os.Open(filepath.Dir(state.path)); err == nil {
		err = directory.Sync()
		_ = directory.Close()
		if err != nil && runtime.GOOS != "windows" {
			return errors.New("sync managed consumer state directory")
		}
	} else if runtime.GOOS != "windows" {
		return errors.New("open managed consumer state directory")
	}
	return nil
}

type HTTPConsole struct {
	base         *url.URL
	client       *http.Client
	username     string
	password     string
	maxBodyBytes int64
}

func NewHTTPConsole(config Config, client *http.Client) (*HTTPConsole, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(config.ConsoleURL), "/"))
	if err != nil || base.Scheme == "" || base.Host == "" || base.User != nil || (base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" || !allowedConsoleHost(base, config.AllowedConsoleHosts, config.AllowLoopback) {
		return nil, errors.New("invalid Higress Console endpoint")
	}
	if strings.TrimSpace(config.Username) == "" || config.Password == "" || config.MCPServerName != FixedMCPServerName {
		return nil, ErrInvalidConfig
	}
	if client == nil {
		client = http.DefaultClient
	}
	if client.Transport != nil {
		return nil, errors.New("Higress Console rejects external HTTP transports")
	}
	copyClient := *client
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	copyClient.Jar = jar
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: base.Hostname()}, TLSHandshakeTimeout: 10 * time.Second}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(host, base.Hostname()) {
			return nil, errors.New("Console dial target is not allowlisted")
		}
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("Console host resolution failed")
		}
		for _, ip := range ips {
			if !isLoopbackHost(host) && !isClusterLocalHost(host) && unsafeConsoleIP(ip) {
				return nil, errors.New("Console host resolved to a private address")
			}
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
		}
		return nil, errors.New("Console host connection failed")
	}
	copyClient.Transport = transport
	maxBodyBytes := config.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = 1 << 20
	}
	return &HTTPConsole{base: base, client: &copyClient, username: strings.TrimSpace(config.Username), password: config.Password, maxBodyBytes: maxBodyBytes}, nil
}

func unsafeConsoleIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.Equal(net.ParseIP("169.254.169.254"))
}

func allowedConsoleHost(base *url.URL, configured []string, allowLoopback bool) bool {
	host := strings.ToLower(base.Hostname())
	if isLoopbackHost(host) {
		return allowLoopback && base.Scheme == "http" && configuredHost(configured, base.Host)
	}
	for _, allowed := range configured {
		if strings.EqualFold(base.Host, strings.TrimSpace(allowed)) {
			return true
		}
	}
	return false
}

func configuredHost(values []string, host string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), host) {
			return true
		}
	}
	return false
}

func (console *HTTPConsole) Consumers(ctx context.Context, server string) ([]string, error) {
	if server != FixedMCPServerName {
		return nil, ErrInvalidConfig
	}
	if err := console.login(ctx); err != nil {
		return nil, err
	}
	query := url.Values{"mcpServerName": []string{FixedMCPServerName}}
	var response mcpConsumerResponse
	if err := console.doJSON(ctx, http.MethodGet, "/v1/mcpServer/consumers?"+query.Encode(), nil, &response); err != nil {
		return nil, err
	}
	if err := response.validate(); err != nil {
		return nil, consoleProtocolError{err}
	}
	result := make([]string, 0, len(response.Data))
	routeName := "mcp-server-" + FixedMCPServerName + ".internal"
	for _, entry := range response.Data {
		if entry.MCPServerName != FixedMCPServerName && entry.MCPServerName != routeName {
			return nil, consoleProtocolError{errors.New("Higress response belongs to another MCP server")}
		}
		if !dnsSubdomain(entry.ConsumerName) {
			return nil, consoleProtocolError{errors.New("Higress response contains an invalid consumer name")}
		}
		result = append(result, entry.ConsumerName)
	}
	return result, nil
}

func (console *HTTPConsole) AddConsumers(ctx context.Context, server string, consumers []string) error {
	if server != FixedMCPServerName {
		return ErrInvalidConfig
	}
	return console.mutateConsumers(ctx, http.MethodPut, consumers)
}

func (console *HTTPConsole) DeleteConsumers(ctx context.Context, server string, consumers []string) error {
	if server != FixedMCPServerName {
		return ErrInvalidConfig
	}
	return console.mutateConsumers(ctx, http.MethodDelete, consumers)
}

func (console *HTTPConsole) mutateConsumers(ctx context.Context, method string, consumers []string) error {
	normalized, err := normalizeConsumers(consumers)
	if err != nil || len(normalized) == 0 {
		return errors.New("Higress consumer mutation requires named consumers")
	}
	if err := console.login(ctx); err != nil {
		return err
	}
	return console.doJSON(ctx, method, "/v1/mcpServer/consumers", map[string]any{"mcpServerName": FixedMCPServerName, "consumers": normalized}, nil)
}

func (console *HTTPConsole) login(ctx context.Context) error {
	var response loginResponse
	if err := console.doJSON(ctx, http.MethodPost, "/session/login", map[string]string{"username": console.username, "password": console.password}, &response); err != nil {
		return err
	}
	if err := response.validate(console.username); err != nil {
		return consoleProtocolError{err}
	}
	return nil
}

func (console *HTTPConsole) doJSON(ctx context.Context, method, path string, input, output any) error {
	relative, err := url.Parse(path)
	if err != nil || relative.IsAbs() || !strings.HasPrefix(relative.Path, "/") || relative.Fragment != "" {
		return errors.New("invalid Higress Console endpoint")
	}
	exactQuery := url.Values{"mcpServerName": []string{FixedMCPServerName}}.Encode()
	allowed := method == http.MethodPost && relative.Path == "/session/login" && relative.RawQuery == "" ||
		method == http.MethodGet && relative.Path == "/v1/mcpServer/consumers" && relative.RawQuery == exactQuery ||
		(method == http.MethodPut || method == http.MethodDelete) && relative.Path == "/v1/mcpServer/consumers" && relative.RawQuery == ""
	if !allowed {
		return errors.New("Higress Console operation is not allowed")
	}
	endpoint := console.base.ResolveReference(relative)
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := console.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	statusAllowed := response.StatusCode == http.StatusOK
	if method == http.MethodGet || method == http.MethodPost && relative.Path == "/session/login" {
		statusAllowed = response.StatusCode == http.StatusOK || response.StatusCode == http.StatusCreated
	} else if method == http.MethodPut || method == http.MethodDelete {
		statusAllowed = response.StatusCode == http.StatusNoContent
	}
	if !statusAllowed {
		return consoleHTTPError{status: response.StatusCode}
	}
	if output == nil {
		content, err := io.ReadAll(io.LimitReader(response.Body, console.maxBodyBytes+1))
		if err != nil {
			return consoleProtocolError{errors.New("read Higress Console mutation response")}
		}
		if int64(len(content)) > console.maxBodyBytes {
			return consoleProtocolError{errors.New("Higress Console mutation response exceeds the size limit")}
		}
		if len(content) > 0 {
			return consoleProtocolError{errors.New("Higress Console mutation returned an unexpected body")}
		}
		return nil
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, console.maxBodyBytes+1))
	if err != nil {
		return consoleProtocolError{errors.New("read Higress Console response")}
	}
	if int64(len(content)) > console.maxBodyBytes {
		return consoleProtocolError{errors.New("Higress Console response exceeds the size limit")}
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(output); err != nil {
		return consoleProtocolError{err}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return consoleProtocolError{errors.New("Higress Console response contains trailing JSON")}
	}
	return nil
}

type responseMeta struct {
	Success *bool `json:"success,omitempty"`
	Code    *int  `json:"code,omitempty"`
}

type loginResponse struct {
	responseMeta
	Name string `json:"name,omitempty"`
}

func (response loginResponse) validate(username string) error {
	if err := response.responseMeta.validate(); err != nil {
		return err
	}
	if response.Success != nil && *response.Success {
		return nil
	}
	if response.Success == nil && response.Name == username {
		return nil
	}
	return errors.New("Higress Console login did not confirm the requested identity")
}

func (response responseMeta) validate() error {
	if response.Success != nil && !*response.Success {
		return errors.New("Higress Console returned an unsuccessful response")
	}
	if response.Code != nil && *response.Code != 0 && *response.Code != http.StatusOK {
		return errors.New("Higress Console returned a non-success business code")
	}
	return nil
}

type mcpConsumerResponse struct {
	responseMeta
	Data []struct {
		MCPServerName string `json:"mcpServerName"`
		ConsumerName  string `json:"consumerName"`
	} `json:"data"`
}

func (response mcpConsumerResponse) validate() error {
	if err := response.responseMeta.validate(); err != nil {
		return err
	}
	if response.Success == nil || !*response.Success {
		return errors.New("Higress Console consumer response did not confirm success")
	}
	if response.Data == nil {
		return errors.New("Higress Console consumer response omitted data")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func isClusterLocalHost(host string) bool {
	return strings.HasSuffix(host, ".svc") || strings.HasSuffix(host, ".svc.cluster.local")
}
