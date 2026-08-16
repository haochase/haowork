package localcore

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/haochase/haowork/internal/capsule"
)

const (
	healthPath            = "/_haowork/health"
	stopPath              = "/_haowork/stop"
	controlHeader         = "X-Haowork-Control"
	HealthProtocolVersion = "v1"
)

var ErrCoreBusy = errors.New("local core is already serving this project")

type Manager interface {
	Ensure(context.Context, string) (Metadata, error)
	Serve(context.Context, string) error
	Stop(context.Context, string) error
}

type StartRequest struct {
	ProjectRoot string
	ProjectID   string
	ControlKey  string
}

type Starter interface {
	Start(context.Context, StartRequest) (Metadata, error)
}

type HandlerFactory func(Metadata, func()) http.Handler

type healthResponse struct {
	ProjectID       string `json:"project_id"`
	ProtocolVersion string `json:"protocol_version"`
	PID             int    `json:"pid"`
}

type LocalManager struct {
	starter Starter
	client  *http.Client

	ensureMu sync.Mutex
}

func NewManager(starter Starter) *LocalManager {
	return &LocalManager{
		starter: starter,
		client:  &http.Client{Timeout: time.Second},
	}
}

func (m *LocalManager) Ensure(ctx context.Context, start string) (Metadata, error) {
	if err := ctx.Err(); err != nil {
		return Metadata{}, err
	}
	root, projectID, err := projectFrom(start)
	if err != nil {
		return Metadata{}, err
	}
	m.ensureMu.Lock()
	defer m.ensureMu.Unlock()
	startupLock, err := acquireStartupLock(ctx, root)
	if err != nil {
		return Metadata{}, err
	}
	defer startupLock.Unlock()

	metadata, err := ReadMetadata(root)
	switch {
	case err == nil && metadata.ProjectID == projectID && m.healthy(ctx, metadata):
		return metadata, nil
	case err == nil:
		if err := removeMetadataIfOwned(root, metadata); err != nil {
			return Metadata{}, err
		}
	case errors.Is(err, os.ErrNotExist):
		// No local Core metadata exists yet.
	default:
		if err := removeStaleMetadata(root); err != nil {
			return Metadata{}, err
		}
	}

	if m.starter == nil {
		go func() {
			_ = m.serve(context.Background(), root, projectID, true, nil)
		}()
		return m.waitForHealthyCore(ctx, root, projectID)
	}
	return m.start(ctx, root, projectID)
}

func (m *LocalManager) Serve(ctx context.Context, start string) error {
	root, projectID, err := projectFrom(start)
	if err != nil {
		return err
	}
	return m.serve(ctx, root, projectID, false, nil)
}

func (m *LocalManager) ServeWithHandler(ctx context.Context, start string, handlerFactory HandlerFactory) error {
	root, projectID, err := projectFrom(start)
	if err != nil {
		return err
	}
	return m.serve(ctx, root, projectID, false, handlerFactory)
}

func (m *LocalManager) serve(ctx context.Context, root, projectID string, startupLockHeld bool, handlerFactory HandlerFactory) (err error) {
	var startupLock *flock.Flock
	if !startupLockHeld {
		startupLock, err = acquireStartupLock(ctx, root)
		if err != nil {
			return err
		}
	}

	var coreLock *flock.Flock
	var metadata Metadata
	metadataWritten := false
	defer func() {
		if coreLock != nil {
			unlockErr := coreLock.Unlock()
			if err == nil && unlockErr != nil {
				err = unlockErr
			}
		}
		if startupLock != nil {
			unlockErr := startupLock.Unlock()
			if err == nil && unlockErr != nil {
				err = unlockErr
			}
		}
		if !metadataWritten {
			return
		}
		cleanupErr := withStartupLock(context.Background(), root, func() error {
			return removeMetadataIfOwned(root, metadata)
		})
		if err == nil && cleanupErr != nil {
			err = cleanupErr
		}
	}()

	checkHealthy := m.healthy
	if handlerFactory != nil {
		checkHealthy = m.apiHealthy
	}
	existing, readErr := ReadMetadata(root)
	switch {
	case readErr == nil && existing.ProjectID == projectID && checkHealthy(ctx, existing):
		return nil
	case readErr == nil:
		if err := removeMetadataIfOwned(root, existing); err != nil {
			return err
		}
	case errors.Is(readErr, os.ErrNotExist):
		// No local Core metadata exists yet.
	default:
		if err := removeStaleMetadata(root); err != nil {
			return err
		}
	}

	coreLock, err = acquireProjectLock(ctx, root)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()

	controlKey, err := newControlKey()
	if err != nil {
		return err
	}
	metadata = Metadata{
		ProjectID:  projectID,
		Endpoint:   "http://" + listener.Addr().String(),
		PID:        os.Getpid(),
		StartedAt:  time.Now().UTC(),
		ControlKey: controlKey,
	}
	stopRequested := make(chan struct{}, 1)
	requestStop := func() {
		select {
		case stopRequested <- struct{}{}:
		default:
		}
	}
	handler := coreHandler(metadata, stopRequested)
	if handlerFactory != nil {
		if customHandler := handlerFactory(metadata, requestStop); customHandler != nil {
			handler = customHandler
		}
	}
	server := &http.Server{Handler: handler}
	if err := writeMetadata(root, metadata); err != nil {
		return err
	}
	metadataWritten = true
	if startupLock != nil {
		if err := startupLock.Unlock(); err != nil {
			return err
		}
		startupLock = nil
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()

	select {
	case serveErr := <-serveDone:
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return serveErr
	case <-ctx.Done():
		if err := shutdownServer(server); err != nil {
			return err
		}
		serveErr := <-serveDone
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return serveErr
	case <-stopRequested:
		if err := shutdownServer(server); err != nil {
			return err
		}
		serveErr := <-serveDone
		if errors.Is(serveErr, http.ErrServerClosed) {
			return nil
		}
		return serveErr
	}
}

func (m *LocalManager) Stop(ctx context.Context, start string) error {
	return m.stop(ctx, start, m.healthy)
}

// StopVerified refuses to send a control credential unless the Local API health identity matches core.json.
func (m *LocalManager) StopVerified(ctx context.Context, start string) error {
	return m.stop(ctx, start, m.apiHealthy)
}

func (m *LocalManager) stop(ctx context.Context, start string, checkHealthy func(context.Context, Metadata) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	root, projectID, err := projectFrom(start)
	if err != nil {
		return err
	}
	startupLock, err := acquireStartupLock(ctx, root)
	if err != nil {
		return err
	}
	defer startupLock.Unlock()
	metadata, err := ReadMetadata(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return removeStaleMetadata(root)
	}
	if metadata.ProjectID != projectID {
		return removeMetadataIfOwned(root, metadata)
	}
	if !checkHealthy(ctx, metadata) {
		return stopErrorWithCleanup(root, metadata, errors.New("local core health identity does not match metadata"))
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, metadata.Endpoint+stopPath, nil)
	if err != nil {
		return err
	}
	request.Header.Set(controlHeader, metadata.ControlKey)
	response, err := m.httpClient().Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return stopErrorWithCleanup(root, metadata, fmt.Errorf("stop local core: %w", err))
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return stopErrorWithCleanup(root, metadata, fmt.Errorf("stop local core: status %d", response.StatusCode))
	}
	return removeMetadataIfOwned(root, metadata)
}

func (m *LocalManager) start(ctx context.Context, root, projectID string) (metadata Metadata, err error) {
	controlKey, err := newControlKey()
	if err != nil {
		return Metadata{}, err
	}
	defer func() {
		if err != nil {
			_ = removeMetadataIfControlKey(root, controlKey)
		}
	}()

	metadata, err = m.starter.Start(ctx, StartRequest{
		ProjectRoot: root,
		ProjectID:   projectID,
		ControlKey:  controlKey,
	})
	if err != nil {
		return Metadata{}, err
	}
	if metadata.ProjectID == "" {
		metadata.ProjectID = projectID
	}
	if metadata.ProjectID != projectID {
		return Metadata{}, fmt.Errorf("starter returned metadata for project %q, want %q", metadata.ProjectID, projectID)
	}
	if metadata.PID == 0 {
		metadata.PID = os.Getpid()
	}
	if metadata.StartedAt.IsZero() {
		metadata.StartedAt = time.Now().UTC()
	} else {
		metadata.StartedAt = metadata.StartedAt.UTC()
	}
	if metadata.ControlKey == "" {
		metadata.ControlKey = controlKey
	}
	if subtle.ConstantTimeCompare([]byte(metadata.ControlKey), []byte(controlKey)) != 1 {
		return Metadata{}, errors.New("starter returned an unexpected control key")
	}
	if err := validateMetadata(metadata); err != nil {
		return Metadata{}, err
	}
	if !m.healthy(ctx, metadata) {
		return Metadata{}, errors.New("starter returned an unhealthy local core")
	}
	if err := writeMetadata(root, metadata); err != nil {
		return Metadata{}, err
	}
	return metadata, nil
}

func (m *LocalManager) waitForHealthyCore(ctx context.Context, root, projectID string) (Metadata, error) {
	waitCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		metadata, err := ReadMetadata(root)
		if err == nil && metadata.ProjectID == projectID && m.healthy(waitCtx, metadata) {
			return metadata, nil
		}
		select {
		case <-waitCtx.Done():
			return Metadata{}, fmt.Errorf("local core did not become healthy: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func (m *LocalManager) healthy(ctx context.Context, metadata Metadata) bool {
	if err := validateMetadata(metadata); err != nil {
		return false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metadata.Endpoint+healthPath, nil)
	if err != nil {
		return false
	}
	response, err := m.httpClient().Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices
}

func IsHealthy(ctx context.Context, metadata Metadata) bool {
	return (&LocalManager{client: &http.Client{Timeout: time.Second}}).apiHealthy(ctx, metadata)
}

func (m *LocalManager) apiHealthy(ctx context.Context, metadata Metadata) bool {
	if err := validateMetadata(metadata); err != nil {
		return false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metadata.Endpoint+healthPath, nil)
	if err != nil {
		return false
	}
	response, err := m.httpClient().Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	var health healthResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&health); err != nil {
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return false
	}
	return health.ProjectID == metadata.ProjectID &&
		health.ProtocolVersion == HealthProtocolVersion &&
		health.PID == metadata.PID
}

func (m *LocalManager) httpClient() *http.Client {
	if m.client == nil {
		return http.DefaultClient
	}
	return m.client
}

func projectFrom(start string) (string, string, error) {
	root, err := capsule.Find(start)
	if err != nil {
		return "", "", err
	}
	manifest, err := capsule.Load(root)
	if err != nil {
		return "", "", err
	}
	return root, manifest.ProjectID, nil
}

func acquireProjectLock(ctx context.Context, projectRoot string) (*flock.Flock, error) {
	return acquireRuntimeLock(ctx, projectRoot, "core.lock")
}

func acquireStartupLock(ctx context.Context, projectRoot string) (*flock.Flock, error) {
	return acquireRuntimeLock(ctx, projectRoot, "core-start.lock")
}

func withStartupLock(ctx context.Context, projectRoot string, operation func() error) error {
	lock, err := acquireStartupLock(ctx, projectRoot)
	if err != nil {
		return err
	}
	defer lock.Unlock()
	return operation()
}

// WithLaunchLock serializes CLI process launches without blocking lifecycle stop requests.
func WithLaunchLock(ctx context.Context, projectRoot string, operation func() error) error {
	lock, err := acquireRuntimeLock(ctx, projectRoot, "core-open.lock")
	if err != nil {
		return err
	}
	defer lock.Unlock()
	return operation()
}

func acquireRuntimeLock(ctx context.Context, projectRoot, filename string) (*flock.Flock, error) {
	if err := os.MkdirAll(runtimeDir(projectRoot), 0o755); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(runtimeDir(projectRoot), filename))
	locked, err := lock.TryLockContext(ctx, 20*time.Millisecond)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, ErrCoreBusy
	}
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, ErrCoreBusy
	}
	return lock, nil
}

func coreHandler(metadata Metadata, stopRequested chan<- struct{}) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(healthPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(stopPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(controlHeader)), []byte(metadata.ControlKey)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		select {
		case stopRequested <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

func shutdownServer(server *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}

func removeMetadataIfControlKey(projectRoot, controlKey string) error {
	metadata, err := ReadMetadata(projectRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return removeStaleMetadata(projectRoot)
	}
	if subtle.ConstantTimeCompare([]byte(metadata.ControlKey), []byte(controlKey)) != 1 {
		return nil
	}
	return removeMetadataIfOwned(projectRoot, metadata)
}

func stopErrorWithCleanup(projectRoot string, metadata Metadata, stopErr error) error {
	if cleanupErr := removeMetadataIfOwned(projectRoot, metadata); cleanupErr != nil {
		return errors.Join(stopErr, fmt.Errorf("remove local core metadata: %w", cleanupErr))
	}
	return stopErr
}
