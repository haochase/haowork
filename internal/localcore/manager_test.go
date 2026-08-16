package localcore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
)

func TestEnsureStartsCoreAndWritesMetadata(t *testing.T) {
	root, projectID := initializeProject(t)
	core := healthyCore(t, nil)
	defer core.Close()

	var request StartRequest
	manager := NewManager(starterFunc(func(_ context.Context, got StartRequest) (Metadata, error) {
		request = got
		return Metadata{Endpoint: core.URL, PID: 4242}, nil
	}))

	metadata, err := manager.Ensure(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if request.ProjectRoot != root {
		t.Fatalf("Starter project root = %q, want %q", request.ProjectRoot, root)
	}
	if request.ProjectID != projectID {
		t.Fatalf("Starter project id = %q, want %q", request.ProjectID, projectID)
	}
	if metadata.ProjectID != projectID {
		t.Fatalf("Metadata.ProjectID = %q, want %q", metadata.ProjectID, projectID)
	}
	if metadata.Endpoint != core.URL {
		t.Fatalf("Metadata.Endpoint = %q, want %q", metadata.Endpoint, core.URL)
	}
	if metadata.ControlKey != request.ControlKey {
		t.Fatal("Metadata.ControlKey did not preserve the generated control key")
	}
	if metadata.StartedAt.IsZero() {
		t.Fatal("Metadata.StartedAt is zero")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(metadata.ControlKey)
	if err != nil {
		t.Fatalf("ControlKey is not base64url: %v", err)
	}
	if len(decoded) != 32 {
		t.Fatalf("ControlKey byte length = %d, want 32", len(decoded))
	}

	persisted, err := ReadMetadata(root)
	if err != nil {
		t.Fatal(err)
	}
	if persisted != metadata {
		t.Fatalf("persisted metadata = %#v, want %#v", persisted, metadata)
	}
}

func TestEnsureReusesHealthyCore(t *testing.T) {
	root, _ := initializeProject(t)
	core := healthyCore(t, nil)
	defer core.Close()

	first := NewManager(starterFunc(func(_ context.Context, _ StartRequest) (Metadata, error) {
		return Metadata{Endpoint: core.URL, PID: 4242}, nil
	}))
	want, err := first.Ensure(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	second := NewManager(starterFunc(func(context.Context, StartRequest) (Metadata, error) {
		t.Fatal("Starter.Start called for a healthy Core")
		return Metadata{}, nil
	}))
	got, err := second.Ensure(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Ensure() = %#v, want %#v", got, want)
	}
}

func TestHealthyRejectsProjectMismatchInHealthResponse(t *testing.T) {
	root, projectID := initializeProject(t)
	_ = root
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_haowork/health" || r.Method != http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"project_id":"PRJ-OTHER","protocol_version":"v1","pid":4242}`))
	}))
	defer core.Close()

	metadata := Metadata{
		ProjectID:  projectID,
		Endpoint:   core.URL,
		PID:        4242,
		StartedAt:  time.Now().UTC(),
		ControlKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32)),
	}
	if IsHealthy(context.Background(), metadata) {
		t.Fatal("IsHealthy() accepted a 2xx response for a different project")
	}
}

func TestEnsureLeavesHealthyMetadataWhenContextIsCancelled(t *testing.T) {
	root, _ := initializeProject(t)
	core := healthyCore(t, nil)
	defer core.Close()

	first := NewManager(starterFunc(func(_ context.Context, _ StartRequest) (Metadata, error) {
		return Metadata{Endpoint: core.URL, PID: 4242}, nil
	}))
	want, err := first.Ensure(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	second := NewManager(starterFunc(func(context.Context, StartRequest) (Metadata, error) {
		t.Fatal("Starter.Start called after context cancellation")
		return Metadata{}, nil
	}))
	if _, err := second.Ensure(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ensure() error = %v, want context cancellation", err)
	}
	got, err := ReadMetadata(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("metadata after cancelled Ensure = %#v, want %#v", got, want)
	}
}

func TestEnsureRecoversStaleMetadata(t *testing.T) {
	root, _ := initializeProject(t)
	stale := healthyCore(t, nil)
	first := NewManager(starterFunc(func(_ context.Context, _ StartRequest) (Metadata, error) {
		return Metadata{Endpoint: stale.URL, PID: 4242}, nil
	}))
	old, err := first.Ensure(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	stale.Close()

	replacement := healthyCore(t, nil)
	defer replacement.Close()
	starts := 0
	second := NewManager(starterFunc(func(_ context.Context, _ StartRequest) (Metadata, error) {
		starts++
		return Metadata{Endpoint: replacement.URL, PID: 4343}, nil
	}))
	got, err := second.Ensure(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if starts != 1 {
		t.Fatalf("Starter.Start calls = %d, want 1", starts)
	}
	if got.Endpoint != replacement.URL {
		t.Fatalf("Metadata.Endpoint = %q, want replacement %q", got.Endpoint, replacement.URL)
	}
	if got.ControlKey == old.ControlKey {
		t.Fatal("stale metadata control key was reused")
	}
}

func TestEnsureCleansMetadataWrittenByFailingStarter(t *testing.T) {
	root, projectID := initializeProject(t)
	core := healthyCore(t, nil)
	defer core.Close()

	manager := NewManager(starterFunc(func(_ context.Context, request StartRequest) (Metadata, error) {
		metadata := Metadata{
			ProjectID:  projectID,
			Endpoint:   core.URL,
			PID:        4242,
			StartedAt:  time.Now().UTC(),
			ControlKey: request.ControlKey,
		}
		if err := writeMetadata(root, metadata); err != nil {
			t.Fatal(err)
		}
		return Metadata{}, errors.New("starter failed after writing metadata")
	}))

	if _, err := manager.Ensure(context.Background(), root); err == nil {
		t.Fatal("Ensure() error = nil, want starter failure")
	}
	if _, err := os.Stat(filepath.Join(root, ".haowork", "runtime", "core.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata remains after starter failure: %v", err)
	}
}

func TestEnsureSerializesInjectedStartersAcrossManagers(t *testing.T) {
	root, _ := initializeProject(t)
	core := healthyCore(t, nil)
	defer core.Close()

	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseStarter := func() {
		releaseOnce.Do(func() {
			close(release)
		})
	}
	defer releaseStarter()
	var mu sync.Mutex
	starts := 0
	starter := starterFunc(func(_ context.Context, _ StartRequest) (Metadata, error) {
		mu.Lock()
		starts++
		call := starts
		mu.Unlock()
		if call == 1 {
			close(firstStarted)
		} else {
			secondStarted <- struct{}{}
		}
		<-release
		return Metadata{Endpoint: core.URL, PID: 4242}, nil
	})

	firstDone := make(chan error, 1)
	go func() {
		_, err := NewManager(starter).Ensure(context.Background(), root)
		firstDone <- err
	}()
	<-firstStarted
	secondDone := make(chan error, 1)
	go func() {
		_, err := NewManager(starter).Ensure(context.Background(), root)
		secondDone <- err
	}()
	select {
	case <-secondStarted:
		t.Fatal("second Starter.Start ran before the first startup completed")
	case <-time.After(100 * time.Millisecond):
	}

	releaseStarter()
	for _, done := range []<-chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Ensure() did not finish")
		}
	}
	mu.Lock()
	gotStarts := starts
	mu.Unlock()
	if gotStarts != 1 {
		t.Fatalf("Starter.Start calls = %d, want 1", gotStarts)
	}
}

func TestStopDeletesMetadata(t *testing.T) {
	root, _ := initializeProject(t)
	var mu sync.Mutex
	var stopControlKey string
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_haowork/health":
			w.WriteHeader(http.StatusNoContent)
		case "/_haowork/stop":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			mu.Lock()
			stopControlKey = r.Header.Get("X-Haowork-Control")
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer core.Close()

	manager := NewManager(starterFunc(func(_ context.Context, _ StartRequest) (Metadata, error) {
		return Metadata{Endpoint: core.URL, PID: 4242}, nil
	}))
	metadata, err := manager.Ensure(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Stop(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotControlKey := stopControlKey
	mu.Unlock()
	if gotControlKey != metadata.ControlKey {
		t.Fatalf("stop control key = %q, want generated control key", gotControlKey)
	}
	if _, err := os.Stat(filepath.Join(root, ".haowork", "runtime", "core.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("core metadata still exists or stat failed: %v", err)
	}
}

func TestStopCleansUnreachableCoreMetadata(t *testing.T) {
	root, _ := initializeProject(t)
	core := healthyCore(t, nil)
	manager := NewManager(starterFunc(func(_ context.Context, _ StartRequest) (Metadata, error) {
		return Metadata{Endpoint: core.URL, PID: 4242}, nil
	}))
	if _, err := manager.Ensure(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	core.Close()

	if err := manager.Stop(context.Background(), root); err == nil {
		t.Fatal("Stop() error = nil, want unreachable Core failure")
	}
	if _, err := os.Stat(filepath.Join(root, ".haowork", "runtime", "core.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata remains after failed stop: %v", err)
	}
}

func TestStopLeavesHealthyMetadataWhenContextIsCancelled(t *testing.T) {
	root, _ := initializeProject(t)
	core := healthyCore(t, nil)
	defer core.Close()
	manager := NewManager(starterFunc(func(_ context.Context, _ StartRequest) (Metadata, error) {
		return Metadata{Endpoint: core.URL, PID: 4242}, nil
	}))
	want, err := manager.Ensure(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.Stop(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop() error = %v, want context cancellation", err)
	}
	got, err := ReadMetadata(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("metadata after cancelled Stop = %#v, want %#v", got, want)
	}
}

func TestStopLeavesHealthyMetadataWhenRequestTimesOut(t *testing.T) {
	root, _ := initializeProject(t)
	stopEntered := make(chan struct{})
	releaseStop := make(chan struct{})
	var releaseOnce sync.Once
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_haowork/health":
			w.WriteHeader(http.StatusNoContent)
		case "/_haowork/stop":
			close(stopEntered)
			<-releaseStop
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer core.Close()
	defer func() {
		releaseOnce.Do(func() {
			close(releaseStop)
		})
	}()
	manager := NewManager(starterFunc(func(_ context.Context, _ StartRequest) (Metadata, error) {
		return Metadata{Endpoint: core.URL, PID: 4242}, nil
	}))
	want, err := manager.Ensure(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	stopDone := make(chan error, 1)
	go func() {
		stopDone <- manager.Stop(ctx, root)
	}()
	<-stopEntered
	select {
	case err := <-stopDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Stop() error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop() did not return after context deadline")
	}
	got, err := ReadMetadata(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("metadata after timed-out Stop = %#v, want %#v", got, want)
	}
	releaseOnce.Do(func() {
		close(releaseStop)
	})
}

func TestStopCleansMetadataAfterRejectedStop(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			root, _ := initializeProject(t)
			core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/_haowork/health":
					w.WriteHeader(http.StatusNoContent)
				case "/_haowork/stop":
					w.WriteHeader(status)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer core.Close()
			manager := NewManager(starterFunc(func(_ context.Context, _ StartRequest) (Metadata, error) {
				return Metadata{Endpoint: core.URL, PID: 4242}, nil
			}))
			if _, err := manager.Ensure(context.Background(), root); err != nil {
				t.Fatal(err)
			}
			if err := manager.Stop(context.Background(), root); err == nil {
				t.Fatal("Stop() error = nil, want rejected stop error")
			}
			if _, err := os.Stat(filepath.Join(root, ".haowork", "runtime", "core.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("metadata remains after rejected Stop: %v", err)
			}
		})
	}
}

func TestStopSerializesWithSubsequentServe(t *testing.T) {
	root, _ := initializeProject(t)
	stopEntered := make(chan struct{})
	releaseStop := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(releaseStop)
		})
	}
	oldCore := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_haowork/health":
			w.WriteHeader(http.StatusNoContent)
		case "/_haowork/stop":
			close(stopEntered)
			<-releaseStop
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer oldCore.Close()
	defer release()
	manager := NewManager(starterFunc(func(_ context.Context, _ StartRequest) (Metadata, error) {
		return Metadata{Endpoint: oldCore.URL, PID: 4242}, nil
	}))
	old, err := manager.Ensure(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- manager.Stop(context.Background(), root)
	}()
	<-stopEntered
	serveContext, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- NewManager(nil).Serve(serveContext, root)
	}()

	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		metadata, err := ReadMetadata(root)
		if err != nil {
			t.Fatal(err)
		}
		if metadata.ControlKey != old.ControlKey {
			t.Fatal("subsequent Serve rewrote metadata before Stop released the startup lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	release()
	select {
	case err := <-stopDone:
		if err == nil {
			t.Fatal("Stop() error = nil, want rejected stop error")
		}
	case <-time.After(time.Second):
		t.Fatal("Stop() did not return")
	}
	newMetadata := waitForMetadata(t, root)
	if newMetadata.ControlKey == old.ControlKey {
		t.Fatal("subsequent Serve did not replace stale metadata")
	}
	cancelServe()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not stop")
	}
}

func TestServeBindsLoopbackAndCleansUpMetadata(t *testing.T) {
	root, _ := initializeProject(t)
	manager := NewManager(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- manager.Serve(ctx, root)
	}()

	metadata := waitForMetadata(t, root)
	endpoint, err := url.Parse(metadata.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.Scheme != "http" || endpoint.Hostname() != "127.0.0.1" {
		t.Fatalf("Core endpoint = %q, want loopback HTTP endpoint", metadata.Endpoint)
	}
	response, err := http.Get(metadata.Endpoint + "/_haowork/health")
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("health status = %d, want %d", response.StatusCode, http.StatusNoContent)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve() did not stop after context cancellation")
	}
	if _, err := os.Stat(filepath.Join(root, ".haowork", "runtime", "core.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("core metadata still exists or stat failed: %v", err)
	}
}

func TestServeReusesHealthyCore(t *testing.T) {
	root, _ := initializeProject(t)
	first := NewManager(nil)
	firstContext, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.Serve(firstContext, root)
	}()
	waitForMetadata(t, root)

	secondContext, cancelSecond := context.WithTimeout(context.Background(), time.Second)
	defer cancelSecond()
	if err := NewManager(nil).Serve(secondContext, root); err != nil {
		t.Fatalf("second Serve() error = %v, want nil after reusing healthy Core", err)
	}

	cancelFirst()
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first Serve() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first Serve() did not stop after context cancellation")
	}
}

func TestConcurrentServeRechecksHealthyMetadataAndAllowsStop(t *testing.T) {
	root, projectID := initializeProject(t)
	healthEntered := make(chan struct{}, 1)
	releaseHealth := make(chan struct{})
	firstContext, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- NewManager(nil).ServeWithHandler(firstContext, root, func(metadata Metadata, stop func()) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case healthPath:
					select {
					case healthEntered <- struct{}{}:
					default:
					}
					select {
					case <-releaseHealth:
					case <-r.Context().Done():
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(struct {
						ProjectID       string `json:"project_id"`
						ProtocolVersion string `json:"protocol_version"`
						PID             int    `json:"pid"`
					}{ProjectID: projectID, ProtocolVersion: "v1", PID: metadata.PID})
				case stopPath:
					if r.Method != http.MethodPost || r.Header.Get(controlHeader) != metadata.ControlKey {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					stop()
					w.WriteHeader(http.StatusNoContent)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			})
		})
	}()
	waitForMetadata(t, root)

	secondDone := make(chan error, 1)
	go func() {
		secondContext, cancelSecond := context.WithTimeout(context.Background(), time.Second)
		defer cancelSecond()
		secondDone <- NewManager(nil).Serve(secondContext, root)
	}()
	select {
	case <-healthEntered:
	case <-time.After(time.Second):
		t.Fatal("redundant Serve did not verify the existing Core before taking core.lock")
	}

	stopDone := make(chan error, 1)
	go func() {
		stopDone <- NewManager(nil).Stop(context.Background(), root)
	}()
	close(releaseHealth)

	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("redundant Serve() error = %v, want nil after reusing healthy Core", err)
		}
	case <-time.After(time.Second):
		t.Fatal("redundant Serve did not finish")
	}
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop() remained blocked by redundant Serve")
	}
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first Serve did not stop")
	}
}

func TestServeWaitsForStartupLockBeforeCoreLock(t *testing.T) {
	root, _ := initializeProject(t)
	startupLock, err := acquireStartupLock(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	serveContext, cancelServe := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- NewManager(nil).Serve(serveContext, root)
	}()
	defer func() {
		cancelServe()
		if err := startupLock.Unlock(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-serveDone:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Serve() error = %v, want context cancellation", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Serve() did not exit after context cancellation")
		}
	}()

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		probeContext, cancelProbe := context.WithTimeout(context.Background(), 20*time.Millisecond)
		coreLock, err := acquireProjectLock(probeContext, root)
		cancelProbe()
		if errors.Is(err, ErrCoreBusy) {
			t.Fatal("Serve() acquired core.lock while a default Ensure-style startup lock was held")
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := coreLock.Unlock(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestServeReturnsCancelledContext(t *testing.T) {
	root, _ := initializeProject(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := NewManager(nil).Serve(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatalf("Serve() error = %v, want context cancellation", err)
	}
}

func TestOpenBrowserUsesBootstrapFragment(t *testing.T) {
	browser := &fakeBrowser{}
	if err := OpenBrowser(context.Background(), browser, "http://127.0.0.1:4400/workbench?mode=local", "bootstrap-token"); err != nil {
		t.Fatal(err)
	}
	opened, err := url.Parse(browser.url)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(opened.RawQuery, "bootstrap") {
		t.Fatalf("browser URL query contains bootstrap token: %q", opened.RawQuery)
	}
	if opened.Fragment != "bootstrap=bootstrap-token" {
		t.Fatalf("browser URL fragment = %q, want bootstrap token", opened.Fragment)
	}
}

type starterFunc func(context.Context, StartRequest) (Metadata, error)

func (f starterFunc) Start(ctx context.Context, request StartRequest) (Metadata, error) {
	return f(ctx, request)
}

type fakeBrowser struct {
	url string
}

func (b *fakeBrowser) Open(_ context.Context, target string) error {
	b.url = target
	return nil
}

func healthyCore(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	if handler == nil {
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/_haowork/health" || r.Method != http.MethodGet {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
	}
	return httptest.NewServer(handler)
}

func initializeProject(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	manifest, err := app.InitializeProject(context.Background(), app.InitializeProjectInput{
		Root:               root,
		Name:               "Local Core test",
		ProjectID:          "PRJ-LOCALCORE",
		Goal:               "Keep local work governed",
		Invariants:         []string{"loopback only"},
		CompletionCriteria: []string{"lifecycle verified"},
		Actor: model.Actor{
			ID:   "USR-OWNER",
			Kind: model.ActorHuman,
			Role: model.RoleOwner,
		},
	}, &testkit.IDs{}, testkit.Clock{Value: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	return root, manifest.ProjectID
}

func waitForMetadata(t *testing.T, root string) Metadata {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		metadata, err := ReadMetadata(root)
		if err == nil {
			return metadata
		}
		if !isRetryableMetadataTransition(err) {
			t.Fatalf("ReadMetadata() error = %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for core metadata")
	return Metadata{}
}

func isRetryableMetadataTransition(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if runtime.GOOS != "windows" {
		return false
	}
	return errors.Is(err, syscall.Errno(32)) || errors.Is(err, syscall.Errno(33))
}
