package agentteamsbinding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/model"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

var (
	testWorkerGVR = schema.GroupVersionResource{Group: "agentteams.io", Version: "v1beta1", Resource: "workers"}
	testTeamGVR   = schema.GroupVersionResource{Group: "agentteams.io", Version: "v1beta1", Resource: "teams"}
)

func TestWorkerCredentialResolverUsesObservedDeliveryLeaderOnly(t *testing.T) {
	controller, publisher := newTestController(t)
	binding, err := controller.ReconcileDeliveryLeader(context.Background(), testWorkerName)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256([]byte(testGatewayKey))
	if binding.ConsumerName != "worker-leader" || binding.CredentialSHA256 != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("consumer binding = %#v", binding)
	}
	if binding.LogicalActorID != "AGT-P005-LEADER" || binding.RuntimePrincipalID != "@leader:matrix.test" ||
		binding.EnvironmentID != "public" || binding.AgentTeamsInstanceID != testTeamName || binding.BindingRevision != 2 {
		t.Fatalf("runtime principal binding = %#v", binding)
	}
	if binding.ObservedAt != testNow.Format(time.RFC3339Nano) || binding.ExpiresAt != testNow.Add(DefaultBindingTTL).Format(time.RFC3339Nano) {
		t.Fatalf("binding lifetime = %s..%s", binding.ObservedAt, binding.ExpiresAt)
	}
	if publisher.published != binding {
		t.Fatalf("published binding = %#v, want %#v", publisher.published, binding)
	}
}

func TestWorkerCredentialResolverWaitsWhenRuntimeBindingIsNotFound(t *testing.T) {
	controller, publisher := newTestController(t)
	controller.RuntimeBindings = staticRuntimeBindingReader{err: ErrRuntimeBindingNotFound}

	if _, err := controller.ReconcileDeliveryLeader(context.Background(), testWorkerName); !errors.Is(err, ErrRuntimeBindingNotFound) {
		t.Fatalf("error = %v, want ErrRuntimeBindingNotFound", err)
	}
	if publisher.published != (WorkerCredentialBinding{}) {
		t.Fatalf("published binding = %#v, want none", publisher.published)
	}
}

func TestWorkerCredentialResolverWaitsForValidObjectsToBecomeReady(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*unstructured.Unstructured, *unstructured.Unstructured)
		remove schema.GroupVersionResource
	}{
		{name: "Worker not found", remove: testWorkerGVR},
		{name: "Team not found", remove: testTeamGVR},
		{name: "Worker phase", mutate: func(worker, _ *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(worker.Object, "Pending", "status", "phase")
		}},
		{name: "Worker principal", mutate: func(worker, _ *unstructured.Unstructured) {
			unstructured.RemoveNestedField(worker.Object, "status", "matrixUserID")
		}},
		{name: "Worker room", mutate: func(worker, _ *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(worker.Object, "", "status", "roomID")
		}},
		{name: "Team phase", mutate: func(_, team *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(team.Object, "Pending", "status", "phase")
		}},
		{name: "Team members absent", mutate: func(_, team *unstructured.Unstructured) {
			unstructured.RemoveNestedField(team.Object, "status", "members")
		}},
		{name: "leader member absent", mutate: func(_, team *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(team.Object, []any{}, "status", "members")
		}},
		{name: "leader not observed", mutate: func(_, team *unstructured.Unstructured) {
			members, _, _ := unstructured.NestedSlice(team.Object, "status", "members")
			members[0].(map[string]any)["observed"] = false
			_ = unstructured.SetNestedSlice(team.Object, members, "status", "members")
		}},
		{name: "leader not ready", mutate: func(_, team *unstructured.Unstructured) {
			members, _, _ := unstructured.NestedSlice(team.Object, "status", "members")
			members[0].(map[string]any)["ready"] = false
			_ = unstructured.SetNestedSlice(team.Object, members, "status", "members")
		}},
		{name: "leader not running", mutate: func(_, team *unstructured.Unstructured) {
			members, _, _ := unstructured.NestedSlice(team.Object, "status", "members")
			members[0].(map[string]any)["phase"] = "Starting"
			_ = unstructured.SetNestedSlice(team.Object, members, "status", "members")
		}},
		{name: "Worker updating", mutate: func(worker, _ *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(worker.Object, "Updating", "status", "phase")
		}},
		{name: "leader updating", mutate: func(_, team *unstructured.Unstructured) {
			members, _, _ := unstructured.NestedSlice(team.Object, "status", "members")
			members[0].(map[string]any)["phase"] = "Updating"
			_ = unstructured.SetNestedSlice(team.Object, members, "status", "members")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker, team, secret := testObjects()
			if test.mutate != nil {
				test.mutate(worker, team)
			}
			controller, publisher := controllerWithObjects(t, worker, team, secret)
			if test.remove != (schema.GroupVersionResource{}) {
				if err := controller.Dynamic.Resource(test.remove).Namespace(controller.Namespace).Delete(context.Background(), map[schema.GroupVersionResource]string{
					testWorkerGVR: testWorkerName,
					testTeamGVR:   testTeamName,
				}[test.remove], metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			}

			_, err := controller.ReconcileDeliveryLeader(context.Background(), testWorkerName)
			if !errors.Is(err, ErrWorkerCredentialBindingPending) || errors.Is(err, ErrBlockedWorkerCredentialBinding) {
				t.Fatalf("error = %v, want pending-only classification", err)
			}
			if publisher.published != (WorkerCredentialBinding{}) {
				t.Fatalf("published binding while object was pending: %#v", publisher.published)
			}
		})
	}
}

func TestPendingReadinessNeverMasksAuthoritativeBlockers(t *testing.T) {
	for _, test := range []struct {
		name       string
		mutate     func(*unstructured.Unstructured, *unstructured.Unstructured, *model.RuntimeBinding)
		removeTeam bool
	}{
		{name: "pending Worker and invalid Team metadata", mutate: func(worker, team *unstructured.Unstructured, _ *model.RuntimeBinding) {
			_ = unstructured.SetNestedField(worker.Object, "Pending", "status", "phase")
			team.SetLabels(withLabel(team.GetLabels(), missionLabel, "other-mission"))
		}},
		{name: "pending Worker and invalid member runtime name", mutate: func(worker, team *unstructured.Unstructured, _ *model.RuntimeBinding) {
			_ = unstructured.SetNestedField(worker.Object, "Starting", "status", "phase")
			members, _, _ := unstructured.NestedSlice(team.Object, "status", "members")
			members[0].(map[string]any)["runtimeName"] = "Invalid_Name"
			_ = unstructured.SetNestedSlice(team.Object, members, "status", "members")
		}},
		{name: "missing Worker principal and RuntimeBinding principal drift", mutate: func(worker, _ *unstructured.Unstructured, binding *model.RuntimeBinding) {
			unstructured.RemoveNestedField(worker.Object, "status", "matrixUserID")
			binding.RuntimePrincipalID = "@other:matrix.test"
		}},
		{name: "pending Team and member principal conflict", mutate: func(_, team *unstructured.Unstructured, _ *model.RuntimeBinding) {
			_ = unstructured.SetNestedField(team.Object, "Pending", "status", "phase")
			members, _, _ := unstructured.NestedSlice(team.Object, "status", "members")
			members[0].(map[string]any)["matrixUserID"] = "@other:matrix.test"
			_ = unstructured.SetNestedSlice(team.Object, members, "status", "members")
		}},
		{name: "pending Team and duplicate leader status", mutate: func(_, team *unstructured.Unstructured, _ *model.RuntimeBinding) {
			_ = unstructured.SetNestedField(team.Object, "Pending", "status", "phase")
			members, _, _ := unstructured.NestedSlice(team.Object, "status", "members")
			members = append(members, members[0])
			_ = unstructured.SetNestedSlice(team.Object, members, "status", "members")
		}},
		{name: "missing Team and RuntimeBinding environment drift", removeTeam: true, mutate: func(_, _ *unstructured.Unstructured, binding *model.RuntimeBinding) {
			binding.EnvironmentID = "internal"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker, team, secret := testObjects()
			binding := testRuntimeBinding()
			test.mutate(worker, team, &binding)
			controller, publisher := controllerWithObjects(t, worker, team, secret)
			controller.RuntimeBindings = staticRuntimeBindingReader{binding: binding}
			if test.removeTeam {
				if err := controller.Dynamic.Resource(testTeamGVR).Namespace(controller.Namespace).Delete(context.Background(), testTeamName, metav1.DeleteOptions{}); err != nil {
					t.Fatal(err)
				}
			}

			_, err := controller.ReconcileDeliveryLeader(context.Background(), testWorkerName)
			if !errors.Is(err, ErrBlockedWorkerCredentialBinding) || errors.Is(err, ErrWorkerCredentialBindingPending) {
				t.Fatalf("error = %v, want blocker to override pending readiness", err)
			}
			if publisher.published != (WorkerCredentialBinding{}) {
				t.Fatalf("published binding after authoritative blocker: %#v", publisher.published)
			}
		})
	}
}

func TestMixedNotFoundErrorsFailClosedAndPreserveNonNotFoundCause(t *testing.T) {
	for _, target := range []string{"Worker", "Team", "RuntimeBinding"} {
		t.Run(target, func(t *testing.T) {
			controller, publisher := newTestController(t)
			cause := errors.New(target + " authority unavailable")
			switch target {
			case "Worker":
				notFound := apierrors.NewNotFound(testWorkerGVR.GroupResource(), testWorkerName)
				controller.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "workers", func(ktesting.Action) (bool, kruntime.Object, error) {
					return true, nil, errors.Join(notFound, cause)
				})
			case "Team":
				notFound := apierrors.NewNotFound(testTeamGVR.GroupResource(), testTeamName)
				controller.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "teams", func(ktesting.Action) (bool, kruntime.Object, error) {
					return true, nil, errors.Join(notFound, cause)
				})
			case "RuntimeBinding":
				controller.RuntimeBindings = staticRuntimeBindingReader{err: errors.Join(ErrRuntimeBindingNotFound, cause)}
			}

			_, err := controller.ReconcileDeliveryLeader(context.Background(), testWorkerName)
			if !errors.Is(err, ErrBlockedWorkerCredentialBinding) || !errors.Is(err, cause) || errors.Is(err, ErrWorkerCredentialBindingPending) {
				t.Fatalf("error = %v, want blocked error preserving %q", err, cause)
			}
			if publisher.published != (WorkerCredentialBinding{}) {
				t.Fatalf("published binding after mixed NotFound error: %#v", publisher.published)
			}
		})
	}
}

func TestBindingControllerRunWaitsForWorkerStatusAndPublishesWhenReady(t *testing.T) {
	worker, team, secret := testObjects()
	readyWorker := worker.DeepCopy()
	_ = unstructured.SetNestedField(worker.Object, "Pending", "status", "phase")
	controller, _ := controllerWithObjects(t, worker, team, secret)
	publisher := &notificationPublisher{published: make(chan WorkerCredentialBinding, 1)}
	controller.Bindings = publisher
	controller.ReconcileAttempts = 1
	controller.ResyncInterval = 5 * time.Millisecond

	client := controller.Dynamic.(*dynamicfake.FakeDynamicClient)
	pendingObserved := make(chan struct{})
	var pendingOnce sync.Once
	client.PrependReactor("get", "workers", func(action ktesting.Action) (bool, kruntime.Object, error) {
		get := action.(ktesting.GetAction)
		object, err := client.Tracker().Get(testWorkerGVR, controller.Namespace, get.GetName())
		if err == nil {
			observed := object.(*unstructured.Unstructured)
			phase, _, _ := unstructured.NestedString(observed.Object, "status", "phase")
			if phase != "Running" {
				pendingOnce.Do(func() { close(pendingObserved) })
			}
		}
		return true, object, err
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- controller.Run(ctx) }()
	select {
	case <-pendingObserved:
	case <-time.After(time.Second):
		t.Fatal("Run did not observe the pending Worker")
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned while Worker was pending: %v", err)
	default:
	}
	select {
	case binding := <-publisher.published:
		t.Fatalf("published binding while Worker was pending: %#v", binding)
	default:
	}
	if _, err := controller.Dynamic.Resource(testWorkerGVR).Namespace(controller.Namespace).Update(ctx, readyWorker, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case binding := <-publisher.published:
		if binding.BindingRevision != testRuntimeBinding().Revision {
			t.Fatalf("binding revision = %d, want %d", binding.BindingRevision, testRuntimeBinding().Revision)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not publish after Worker became ready")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
}

func TestBindingControllerRunWaitsForDegradedTeamAndPublishesWhenActive(t *testing.T) {
	worker, team, secret := testObjects()
	readyTeam := team.DeepCopy()
	_ = unstructured.SetNestedField(team.Object, "Degraded", "status", "phase")
	controller, _ := controllerWithObjects(t, worker, team, secret)
	publisher := &notificationPublisher{published: make(chan WorkerCredentialBinding, 1)}
	controller.Bindings = publisher
	controller.ReconcileAttempts = 1
	controller.ResyncInterval = 5 * time.Millisecond

	client := controller.Dynamic.(*dynamicfake.FakeDynamicClient)
	degradedObserved := make(chan struct{})
	var degradedOnce sync.Once
	client.PrependReactor("get", "teams", func(action ktesting.Action) (bool, kruntime.Object, error) {
		get := action.(ktesting.GetAction)
		object, err := client.Tracker().Get(testTeamGVR, controller.Namespace, get.GetName())
		if err == nil {
			observed := object.(*unstructured.Unstructured)
			phase, _, _ := unstructured.NestedString(observed.Object, "status", "phase")
			if phase == "Degraded" {
				degradedOnce.Do(func() { close(degradedObserved) })
			}
		}
		return true, object, err
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- controller.Run(ctx) }()
	select {
	case <-degradedObserved:
	case <-time.After(time.Second):
		t.Fatal("Run did not observe the degraded Team")
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned while Team was recoverably degraded: %v", err)
	default:
	}
	select {
	case binding := <-publisher.published:
		t.Fatalf("published binding while Team was degraded: %#v", binding)
	default:
	}
	if _, err := controller.Dynamic.Resource(testTeamGVR).Namespace(controller.Namespace).Update(ctx, readyTeam, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case binding := <-publisher.published:
		if binding.BindingRevision != testRuntimeBinding().Revision {
			t.Fatalf("binding revision = %d, want %d", binding.BindingRevision, testRuntimeBinding().Revision)
		}
	case err := <-done:
		t.Fatalf("Run returned before degraded Team recovered: %v", err)
	case <-time.After(time.Second):
		t.Fatal("Run did not publish after Team became Active")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
}

func TestBindingControllerWatchWaitsForPendingWorkerStatus(t *testing.T) {
	worker, team, secret := testObjects()
	_ = unstructured.SetNestedField(worker.Object, "Starting", "status", "phase")
	controller, _ := controllerWithObjects(t, worker, team, secret)
	publisher := &notificationPublisher{published: make(chan WorkerCredentialBinding, 1)}
	controller.Bindings = publisher
	controller.ReconcileAttempts = 1

	if err := controller.handleWatchEvent(context.Background(), watch.Event{Type: watch.Modified, Object: worker.DeepCopy()}); err != nil {
		t.Fatalf("pending Worker watch event returned an error: %v", err)
	}
	select {
	case binding := <-publisher.published:
		t.Fatalf("published binding for pending Worker watch event: %#v", binding)
	default:
	}
}

func TestBindingControllerRunFailsClosedOnMalformedObservedObject(t *testing.T) {
	worker, team, secret := testObjects()
	members, _, _ := unstructured.NestedSlice(team.Object, "status", "members")
	members[0].(map[string]any)["runtimeName"] = "Invalid_Name"
	_ = unstructured.SetNestedSlice(team.Object, members, "status", "members")
	controller, _ := controllerWithObjects(t, worker, team, secret)
	controller.ReconcileAttempts = 1

	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { done <- controller.Run(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrBlockedWorkerCredentialBinding) || errors.Is(err, ErrWorkerCredentialBindingPending) {
			t.Fatalf("Run error = %v, want blocked-only malformed object error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not fail closed on a malformed observed object")
	}
}

func TestWorkerCredentialResolverFailsClosedOnMalformedPendingFieldsAndReadErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(*unstructured.Unstructured, *unstructured.Unstructured)
		readErr error
	}{
		{name: "Worker phase type", mutate: func(worker, _ *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(worker.Object, true, "status", "phase")
		}},
		{name: "Team phase type", mutate: func(_, team *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(team.Object, true, "status", "phase")
		}},
		{name: "unknown Worker phase", mutate: func(worker, _ *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(worker.Object, "Unknown", "status", "phase")
		}},
		{name: "failed Worker phase", mutate: func(worker, _ *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(worker.Object, "Failed", "status", "phase")
		}},
		{name: "unknown Team phase", mutate: func(_, team *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(team.Object, "Unknown", "status", "phase")
		}},
		{name: "members type", mutate: func(_, team *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(team.Object, "invalid", "status", "members")
		}},
		{name: "malformed spec member", mutate: func(_, team *unstructured.Unstructured) {
			members, _, _ := unstructured.NestedSlice(team.Object, "spec", "workerMembers")
			members = append(members, "invalid")
			_ = unstructured.SetNestedSlice(team.Object, members, "spec", "workerMembers")
		}},
		{name: "invalid runtime name", mutate: func(_, team *unstructured.Unstructured) {
			members, _, _ := unstructured.NestedSlice(team.Object, "status", "members")
			members[0].(map[string]any)["runtimeName"] = "Invalid_Name"
			_ = unstructured.SetNestedSlice(team.Object, members, "status", "members")
		}},
		{name: "duplicate leader", mutate: func(_, team *unstructured.Unstructured) {
			members, _, _ := unstructured.NestedSlice(team.Object, "status", "members")
			members = append(members, members[0])
			_ = unstructured.SetNestedSlice(team.Object, members, "status", "members")
		}},
		{name: "unknown leader phase", mutate: func(_, team *unstructured.Unstructured) {
			members, _, _ := unstructured.NestedSlice(team.Object, "status", "members")
			members[0].(map[string]any)["phase"] = "Unknown"
			_ = unstructured.SetNestedSlice(team.Object, members, "status", "members")
		}},
		{name: "failed leader phase", mutate: func(_, team *unstructured.Unstructured) {
			members, _, _ := unstructured.NestedSlice(team.Object, "status", "members")
			members[0].(map[string]any)["phase"] = "Failed"
			_ = unstructured.SetNestedSlice(team.Object, members, "status", "members")
		}},
		{name: "invalid nonleader member runtime name", mutate: func(_, team *unstructured.Unstructured) {
			specMembers, _, _ := unstructured.NestedSlice(team.Object, "spec", "workerMembers")
			specMembers = append(specMembers, map[string]any{"name": testTeamName + "-build", "role": "worker"})
			_ = unstructured.SetNestedSlice(team.Object, specMembers, "spec", "workerMembers")
			members, _, _ := unstructured.NestedSlice(team.Object, "status", "members")
			members = append(members, map[string]any{
				"name": testTeamName + "-build", "runtimeName": "Invalid_Name", "role": "worker",
				"matrixUserID": "@build:matrix.test", "observed": false, "ready": false, "phase": "Starting",
			})
			_ = unstructured.SetNestedSlice(team.Object, members, "status", "members")
		}},
		{name: "conflicting nonleader member principal", mutate: func(_, team *unstructured.Unstructured) {
			specMembers, _, _ := unstructured.NestedSlice(team.Object, "spec", "workerMembers")
			specMembers = append(specMembers, map[string]any{"name": testTeamName + "-build", "role": "worker"})
			_ = unstructured.SetNestedSlice(team.Object, specMembers, "spec", "workerMembers")
			members, _, _ := unstructured.NestedSlice(team.Object, "status", "members")
			members = append(members, map[string]any{
				"name": testTeamName + "-build", "runtimeName": "build", "role": "worker",
				"matrixUserID": "@other:matrix.test", "observed": false, "ready": false, "phase": "Starting",
			})
			_ = unstructured.SetNestedSlice(team.Object, members, "status", "members")
		}},
		{name: "duplicate spec name with different role", mutate: func(_, team *unstructured.Unstructured) {
			members, _, _ := unstructured.NestedSlice(team.Object, "spec", "workerMembers")
			members = append(members, map[string]any{"name": testWorkerName, "role": "worker"})
			_ = unstructured.SetNestedSlice(team.Object, members, "spec", "workerMembers")
		}},
		{name: "duplicate nonleader spec name", mutate: func(_, team *unstructured.Unstructured) {
			members, _, _ := unstructured.NestedSlice(team.Object, "spec", "workerMembers")
			members = append(members,
				map[string]any{"name": testTeamName + "-build", "role": "worker"},
				map[string]any{"name": testTeamName + "-build", "role": "worker"})
			_ = unstructured.SetNestedSlice(team.Object, members, "spec", "workerMembers")
		}},
		{name: "second spec leader", mutate: func(_, team *unstructured.Unstructured) {
			members, _, _ := unstructured.NestedSlice(team.Object, "spec", "workerMembers")
			members = append(members, map[string]any{"name": testTeamName + "-build", "role": "team_leader"})
			_ = unstructured.SetNestedSlice(team.Object, members, "spec", "workerMembers")
		}},
		{name: "unknown spec role", mutate: func(_, team *unstructured.Unstructured) {
			members, _, _ := unstructured.NestedSlice(team.Object, "spec", "workerMembers")
			members = append(members, map[string]any{"name": testTeamName + "-build", "role": "observer"})
			_ = unstructured.SetNestedSlice(team.Object, members, "spec", "workerMembers")
		}},
		{name: "Worker read error", readErr: errors.New("Worker API unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker, team, secret := testObjects()
			if test.mutate != nil {
				test.mutate(worker, team)
			}
			controller, publisher := controllerWithObjects(t, worker, team, secret)
			if test.readErr != nil {
				controller.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "workers", func(ktesting.Action) (bool, kruntime.Object, error) {
					return true, nil, test.readErr
				})
			}
			_, err := controller.ReconcileDeliveryLeader(context.Background(), testWorkerName)
			if !errors.Is(err, ErrBlockedWorkerCredentialBinding) || errors.Is(err, ErrWorkerCredentialBindingPending) {
				t.Fatalf("error = %v, want blocked-only classification", err)
			}
			if publisher.published != (WorkerCredentialBinding{}) {
				t.Fatalf("published binding after malformed input: %#v", publisher.published)
			}
		})
	}
}

func TestBindingControllerRunWaitsForRuntimeBindingAndPublishesAfterResync(t *testing.T) {
	controller, _ := newTestController(t)
	reader := &mutableRuntimeBindingReader{err: ErrRuntimeBindingNotFound, pendingObserved: make(chan struct{})}
	publisher := &notificationPublisher{published: make(chan WorkerCredentialBinding, 1)}
	controller.RuntimeBindings = reader
	controller.Bindings = publisher
	controller.ResyncInterval = time.Millisecond
	controller.ReconcileAttempts = 1

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- controller.Run(ctx) }()
	select {
	case <-reader.pendingObserved:
	case <-time.After(time.Second):
		t.Fatal("Run did not observe the pending RuntimeBinding")
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned while RuntimeBinding was pending: %v", err)
	default:
	}
	select {
	case binding := <-publisher.published:
		t.Fatalf("published binding while RuntimeBinding was pending: %#v", binding)
	default:
	}
	reader.set(testRuntimeBinding(), nil)
	select {
	case binding := <-publisher.published:
		if binding.BindingRevision != testRuntimeBinding().Revision {
			t.Fatalf("binding revision = %d, want %d", binding.BindingRevision, testRuntimeBinding().Revision)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not publish after RuntimeBinding became active")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
}

func TestBindingControllerRunFailsClosedOnRuntimeBindingReaderErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "read failure", err: errors.New("governance state unavailable")},
		{name: "multiple active", err: errors.New("exactly one active RuntimeBinding is required")},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller, _ := newTestController(t)
			publisher := &notificationPublisher{published: make(chan WorkerCredentialBinding, 1)}
			controller.RuntimeBindings = staticRuntimeBindingReader{err: test.err}
			controller.Bindings = publisher
			controller.ReconcileAttempts = 1

			err := controller.Run(context.Background())
			if !errors.Is(err, ErrBlockedWorkerCredentialBinding) || errors.Is(err, ErrRuntimeBindingNotFound) {
				t.Fatalf("Run error = %v, want fail-closed reader error", err)
			}
			select {
			case binding := <-publisher.published:
				t.Fatalf("published binding after reader error: %#v", binding)
			default:
			}
		})
	}
}

func TestOnlyBindingPendingRequiresEveryErrorLeafToBePending(t *testing.T) {
	alias := runtimeBindingNotFoundAlias{}
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "custom Is", err: alias, want: true},
		{name: "Worker pending", err: ErrWorkerCredentialBindingPending, want: true},
		{name: "wrapped custom Is", err: fmt.Errorf("pending: %w", alias), want: true},
		{name: "all pending join", err: errors.Join(ErrRuntimeBindingNotFound, alias), want: true},
		{name: "mixed join", err: errors.Join(ErrRuntimeBindingNotFound, errors.New("revocation failed")), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := onlyBindingPending(test.err); got != test.want {
				t.Fatalf("onlyBindingPending() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestWorkerCredentialResolverFailsClosedOnRuntimeBindingReadError(t *testing.T) {
	controller, _ := newTestController(t)
	readErr := errors.New("governance state unavailable")
	controller.RuntimeBindings = staticRuntimeBindingReader{err: readErr}

	if _, err := controller.ReconcileDeliveryLeader(context.Background(), testWorkerName); !errors.Is(err, ErrBlockedWorkerCredentialBinding) {
		t.Fatalf("error = %v, want ErrBlockedWorkerCredentialBinding", err)
	}
}

func TestWorkerCredentialResolverRejectsSecretBeforeKubernetesGetWhenNotAllowed(t *testing.T) {
	controller, _ := newTestController(t)
	controller.AllowedSecretNames = []string{"agentteams-creds-other-worker"}
	controller.Core.(*kubernetesfake.Clientset).PrependReactor("get", "secrets", func(ktesting.Action) (bool, kruntime.Object, error) {
		t.Fatal("controller attempted an unauthorized Secret get")
		return true, nil, nil
	})
	if _, err := controller.ReconcileDeliveryLeader(context.Background(), testWorkerName); !errors.Is(err, ErrBlockedWorkerCredentialBinding) {
		t.Fatalf("error = %v, want allowlist rejection", err)
	}
}

func TestWorkerCredentialResolverReportsRevocationFailure(t *testing.T) {
	worker, team, secret := testObjects()
	deleting := metav1.NewTime(time.Now())
	worker.SetDeletionTimestamp(&deleting)
	controller, _ := controllerWithObjects(t, worker, team, secret)
	revokeErr := errors.New("persist revocation failed")
	controller.Bindings = &failedRevocationPublisher{removeErr: revokeErr}
	_, err := controller.ReconcileDeliveryLeader(context.Background(), testWorkerName)
	if !errors.Is(err, ErrBlockedWorkerCredentialBinding) || !errors.Is(err, revokeErr) {
		t.Fatalf("error = %v, want blocked and revocation errors", err)
	}
}

func TestBindingControllerRetriesRevocationAndReturnsHealthFailure(t *testing.T) {
	worker, team, secret := testObjects()
	deleting := metav1.NewTime(time.Now())
	worker.SetDeletionTimestamp(&deleting)
	controller, _ := controllerWithObjects(t, worker, team, secret)
	publisher := &failedRevocationPublisher{removeErr: errors.New("persist revocation failed")}
	controller.Bindings = publisher
	controller.ReconcileAttempts = 2
	controller.RetryInterval = time.Nanosecond
	err := controller.reconcileDeliveryLeaderWithRetry(context.Background(), testWorkerName)
	if err == nil || publisher.removeCalls != 2 {
		t.Fatalf("error=%v remove calls=%d, want bounded two-attempt health failure", err, publisher.removeCalls)
	}
}

func TestWorkerCredentialResolverRejectsWrongControllerMissionOrFunction(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*unstructured.Unstructured, *unstructured.Unstructured)
	}{
		{name: "controller", mutate: func(worker, _ *unstructured.Unstructured) {
			worker.SetLabels(withLabel(worker.GetLabels(), controllerLabel, "other-controller"))
		}},
		{name: "mission", mutate: func(worker, team *unstructured.Unstructured) {
			team.SetAnnotations(withLabel(team.GetAnnotations(), missionIDAnnotation, "MSN-OTHER"))
		}},
		{name: "function", mutate: func(worker, _ *unstructured.Unstructured) {
			worker.SetLabels(withLabel(worker.GetLabels(), functionLabel, "build"))
		}},
		{name: "matrix principal", mutate: func(_ *unstructured.Unstructured, team *unstructured.Unstructured) {
			members, _, _ := unstructured.NestedSlice(team.Object, "status", "members")
			members[0].(map[string]any)["matrixUserID"] = "@other:matrix.test"
			_ = unstructured.SetNestedSlice(team.Object, members, "status", "members")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker, team, secret := testObjects()
			test.mutate(worker, team)
			controller, _ := controllerWithObjects(t, worker, team, secret)
			if _, err := controller.ReconcileDeliveryLeader(context.Background(), testWorkerName); !errors.Is(err, ErrBlockedWorkerCredentialBinding) {
				t.Fatalf("error = %v, want ErrBlockedWorkerCredentialBinding", err)
			}
		})
	}
}

func TestWorkerCredentialResolverRejectsDeletingWorkerAndSecret(t *testing.T) {
	deleting := metav1.NewTime(time.Now())
	for _, target := range []string{"worker", "team", "secret"} {
		t.Run(target, func(t *testing.T) {
			worker, team, secret := testObjects()
			switch target {
			case "worker":
				worker.SetDeletionTimestamp(&deleting)
			case "team":
				team.SetDeletionTimestamp(&deleting)
			case "secret":
				secret.DeletionTimestamp = &deleting
			}
			controller, _ := controllerWithObjects(t, worker, team, secret)
			if _, err := controller.ReconcileDeliveryLeader(context.Background(), testWorkerName); !errors.Is(err, ErrBlockedWorkerCredentialBinding) {
				t.Fatalf("error = %v, want deleting object rejection", err)
			}
		})
	}
}

func TestWorkerCredentialResolverRevokesPublishedBindingWhenWorkerStartsDeleting(t *testing.T) {
	root := t.TempDir()
	publisher, err := newTestFileBindingPublisher(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(context.Background(), testBinding()); err != nil {
		t.Fatal(err)
	}
	worker, team, secret := testObjects()
	deleting := metav1.NewTime(time.Now())
	worker.SetDeletionTimestamp(&deleting)
	controller, _ := controllerWithObjects(t, worker, team, secret)
	controller.Bindings = publisher
	if _, err := controller.ReconcileDeliveryLeader(context.Background(), testWorkerName); !errors.Is(err, ErrBlockedWorkerCredentialBinding) {
		t.Fatalf("error = %v, want deleting Worker rejection", err)
	}
	content, err := os.ReadFile(filepath.Join(root, "mcp-bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), testBinding().ConsumerName) {
		t.Fatalf("stale binding was not revoked: %s", content)
	}
}

func TestBindingControllerStartupPrunesBindingForWorkerDeletedWhileOffline(t *testing.T) {
	root := t.TempDir()
	publisher, err := newTestFileBindingPublisher(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(context.Background(), testBinding()); err != nil {
		t.Fatal(err)
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), map[schema.GroupVersionResource]string{
		testWorkerGVR: "WorkerList", testTeamGVR: "TeamList",
	})
	controller := Controller{
		Dynamic: dynamicClient, Core: kubernetesfake.NewSimpleClientset(), Namespace: "haowork-public",
		ControllerName: "haowork-public-agentteams-controller", RuntimeBindings: staticRuntimeBindingReader{}, Bindings: publisher,
	}
	if err := controller.reconcileCurrent(context.Background()); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "mcp-bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), testBinding().ConsumerName) {
		t.Fatalf("offline-deleted Worker left a stale binding: %s", content)
	}
}

func TestWorkerCredentialResolverReturnsDigestWithoutRawGatewayKey(t *testing.T) {
	worker, team, secret := testObjects()
	secret.Data["FUTURE_OFFICIAL_FIELD"] = []byte("must-not-be-read")
	controller, publisher := controllerWithObjects(t, worker, team, secret)
	binding, err := controller.ReconcileDeliveryLeader(context.Background(), testWorkerName)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(struct {
		Binding   WorkerCredentialBinding
		Published WorkerCredentialBinding
	}{binding, publisher.published})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{testGatewayKey, "matrix-password", "minio-password", "matrix-token", "must-not-be-read"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("binding output leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestWorkerCredentialResolverRejectsSecretIdentityDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*corev1.Secret)
	}{
		{name: "worker label", mutate: func(secret *corev1.Secret) { secret.Labels[workerLabel] = "other-worker" }},
		{name: "controller label", mutate: func(secret *corev1.Secret) { secret.Labels[controllerLabel] = "other-controller" }},
		{name: "service account token type", mutate: func(secret *corev1.Secret) { secret.Type = corev1.SecretTypeServiceAccountToken }},
		{name: "wrong optional owner uid", mutate: func(secret *corev1.Secret) {
			controlled := true
			secret.OwnerReferences = []metav1.OwnerReference{{APIVersion: "agentteams.io/v1beta1", Kind: "Worker", Name: testWorkerName, UID: types.UID("wrong-worker-uid"), Controller: &controlled}}
		}},
		{name: "missing gateway key", mutate: func(secret *corev1.Secret) { delete(secret.Data, gatewayKeyName) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker, team, secret := testObjects()
			test.mutate(secret)
			controller, _ := controllerWithObjects(t, worker, team, secret)
			if _, err := controller.ReconcileDeliveryLeader(context.Background(), testWorkerName); !errors.Is(err, ErrBlockedWorkerCredentialBinding) {
				t.Fatalf("error = %v, want Secret identity rejection", err)
			}
		})
	}
}

func TestWorkerCredentialResolverRejectsRuntimeBindingDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*model.RuntimeBinding)
	}{
		{name: "principal", mutate: func(binding *model.RuntimeBinding) { binding.RuntimePrincipalID = "@other:matrix.test" }},
		{name: "environment", mutate: func(binding *model.RuntimeBinding) { binding.EnvironmentID = "internal" }},
		{name: "instance", mutate: func(binding *model.RuntimeBinding) { binding.AgentTeamsInstanceID = "other-team" }},
		{name: "inactive", mutate: func(binding *model.RuntimeBinding) { binding.Status = "inactive" }},
		{name: "revision", mutate: func(binding *model.RuntimeBinding) { binding.Revision = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker, team, secret := testObjects()
			controller, _ := controllerWithObjects(t, worker, team, secret)
			reader := controller.RuntimeBindings.(staticRuntimeBindingReader)
			test.mutate(&reader.binding)
			controller.RuntimeBindings = reader
			if _, err := controller.ReconcileDeliveryLeader(context.Background(), testWorkerName); !errors.Is(err, ErrBlockedWorkerCredentialBinding) {
				t.Fatalf("error = %v, want RuntimeBinding drift rejection", err)
			}
		})
	}
}

func TestFileBindingPublisherUsesFixedAtomicPrivatePath(t *testing.T) {
	root := t.TempDir()
	publisher, err := newTestFileBindingPublisher(t, root)
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding()
	if err := publisher.Publish(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "mcp-bindings.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), testGatewayKey) || !strings.Contains(string(content), binding.CredentialSHA256) {
		t.Fatalf("published document is not digest-only: %s", content)
	}
	if matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".mcp-bindings-*.tmp")); len(matches) != 0 {
		t.Fatalf("temporary binding files remain: %v", matches)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("binding mode = %v, err=%v", info.Mode().Perm(), err)
		}
	}
}

func TestFileBindingPublisherAtomicallyReplacesCurrentActorBinding(t *testing.T) {
	root := t.TempDir()
	publisher, err := newTestFileBindingPublisher(t, root)
	if err != nil {
		t.Fatal(err)
	}
	first := testBinding()
	if err := publisher.Publish(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.BindingRevision++
	second.CredentialSHA256 = strings.Repeat("b", 64)
	if err := publisher.Publish(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "mcp-bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), first.CredentialSHA256) || strings.Count(string(content), `"consumer_name"`) != 1 || !strings.Contains(string(content), second.CredentialSHA256) {
		t.Fatalf("replaced binding document = %s", content)
	}
}

func TestFileBindingPublisherRejectsInvalidExistingBinding(t *testing.T) {
	root := t.TempDir()
	publisher, err := newTestFileBindingPublisher(t, root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mcp-bindings.json"), []byte(`{"bindings":[{"consumer_name":"forged","credential_sha256":"plaintext","principal":{"logical_actor_id":"AGT-FORGED","runtime_principal_id":"forged","environment_id":"public","agentteams_instance_id":"forged","binding_revision":1}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := publisher.Publish(context.Background(), testBinding()); err == nil {
		t.Fatal("publisher preserved an invalid existing binding")
	}
}

func TestFileBindingPublisherRejectsConsumerCollision(t *testing.T) {
	root := t.TempDir()
	publisher, err := newTestFileBindingPublisher(t, root)
	if err != nil {
		t.Fatal(err)
	}
	first := testBinding()
	if err := publisher.Publish(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.LogicalActorID = "AGT-OTHER"
	second.RuntimePrincipalID = "@other:matrix.test"
	second.CredentialSHA256 = strings.Repeat("c", 64)
	if err := publisher.Publish(context.Background(), second); err == nil {
		t.Fatal("publisher accepted two logical actors for one Consumer")
	}
}

func TestFileBindingPublisherRejectsSymlinkedRuntimePath(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "bindings.json")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(root, "mcp-bindings.json")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
	publisher, err := newTestFileBindingPublisher(t, root)
	if err == nil {
		err = publisher.Publish(context.Background(), testBinding())
	}
	if !errors.Is(err, ErrUnsafeBindingPath) {
		t.Fatalf("error = %v, want ErrUnsafeBindingPath", err)
	}
}

func TestBindingControllerServiceAccountCannotReadUnrelatedSecretKinds(t *testing.T) {
	for _, name := range []string{"haowork-binding-controller.yaml", "haowork-binding-controller-internal.yaml"} {
		content, err := os.ReadFile(filepath.Join("..", "..", "deploy", "agentteams", "v1.2.2", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		for _, required := range []string{"name: haowork-agentteams-binding-controller", `resources: ["workers", "teams"]`, `verbs: ["get", "list", "watch"]`, `resources: ["secrets"]`, `verbs: ["get"]`} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s omits %q", name, required)
			}
		}
		for _, forbidden := range []string{`verbs: ["get", "list"]`, `resources: ["secrets"]\n    verbs: ["get", "list"`, "cluster-admin", "ClusterRole", `verbs: ["create"]`, `verbs: ["update"]`, `verbs: ["patch"]`, `verbs: ["delete"]`} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s contains forbidden RBAC token %q", name, forbidden)
			}
		}
	}
}

func TestBindingControllerCannotReadHigressConsoleSecret(t *testing.T) {
	for _, name := range []string{"haowork-binding-controller.yaml", "haowork-binding-controller-internal.yaml"} {
		content, err := os.ReadFile(filepath.Join("..", "..", "deploy", "agentteams", "v1.2.2", name))
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ToLower(string(content))
		for _, forbidden := range []string{"higress-console", "adminusername", "adminpassword", "minio_root", "matrix-appservice"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s contains forbidden secret source %q", name, forbidden)
			}
		}
	}
}

const (
	testWorkerName = "haowork-msn-001-delivery-leader"
	testTeamName   = "haowork-msn-001"
	testGatewayKey = "test-only-gateway-key"
)

var testNow = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

type staticRuntimeBindingReader struct {
	binding model.RuntimeBinding
	err     error
}

type mutableRuntimeBindingReader struct {
	mu              sync.RWMutex
	binding         model.RuntimeBinding
	err             error
	pendingObserved chan struct{}
	pendingOnce     sync.Once
}

func (reader *mutableRuntimeBindingReader) CurrentRuntimeBinding(context.Context, string) (model.RuntimeBinding, error) {
	reader.mu.RLock()
	binding, err := reader.binding, reader.err
	reader.mu.RUnlock()
	if errors.Is(err, ErrRuntimeBindingNotFound) && reader.pendingObserved != nil {
		reader.pendingOnce.Do(func() { close(reader.pendingObserved) })
	}
	return binding, err
}

func (reader *mutableRuntimeBindingReader) set(binding model.RuntimeBinding, err error) {
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.binding, reader.err = binding, err
}

type runtimeBindingNotFoundAlias struct{}

func (runtimeBindingNotFoundAlias) Error() string { return "pending RuntimeBinding" }
func (runtimeBindingNotFoundAlias) Is(target error) bool {
	return target == ErrRuntimeBindingNotFound
}

type notificationPublisher struct{ published chan WorkerCredentialBinding }

func (publisher *notificationPublisher) Publish(_ context.Context, binding WorkerCredentialBinding) error {
	select {
	case publisher.published <- binding:
	default:
	}
	return nil
}

func (reader staticRuntimeBindingReader) CurrentRuntimeBinding(context.Context, string) (model.RuntimeBinding, error) {
	if reader.err != nil {
		return model.RuntimeBinding{}, reader.err
	}
	return reader.binding, nil
}

type capturePublisher struct{ published WorkerCredentialBinding }

func (publisher *capturePublisher) Publish(_ context.Context, binding WorkerCredentialBinding) error {
	publisher.published = binding
	return nil
}

type failedRevocationPublisher struct {
	removeErr   error
	removeCalls int
}

func (*failedRevocationPublisher) Publish(context.Context, WorkerCredentialBinding) error { return nil }
func (publisher *failedRevocationPublisher) RemoveInstance(context.Context, string) error {
	publisher.removeCalls++
	return publisher.removeErr
}

func newTestController(t *testing.T) (Controller, *capturePublisher) {
	t.Helper()
	worker, team, secret := testObjects()
	return controllerWithObjects(t, worker, team, secret)
}

func newTestFileBindingPublisher(t *testing.T, root string) (*FileBindingPublisher, error) {
	t.Helper()
	publisher, err := NewFileBindingPublisher(root)
	if publisher != nil {
		publisher.clock = func() time.Time { return testNow }
	}
	return publisher, err
}

func controllerWithObjects(t *testing.T, worker, team *unstructured.Unstructured, secret *corev1.Secret) (Controller, *capturePublisher) {
	t.Helper()
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(kruntime.NewScheme(), map[schema.GroupVersionResource]string{
		testWorkerGVR: "WorkerList", testTeamGVR: "TeamList",
	}, worker, team)
	publisher := &capturePublisher{}
	return Controller{
		Dynamic: dynamicClient, Core: kubernetesfake.NewSimpleClientset(secret), Namespace: "haowork-public",
		ControllerName: "haowork-public-agentteams-controller", AllowedSecretNames: []string{credentialSecretPrefix + testWorkerName},
		RuntimeBindings: staticRuntimeBindingReader{binding: testRuntimeBinding()}, Bindings: publisher,
		Clock: func() time.Time { return testNow }, BindingTTL: DefaultBindingTTL,
	}, publisher
}

func testObjects() (*unstructured.Unstructured, *unstructured.Unstructured, *corev1.Secret) {
	labels := map[string]string{
		controllerLabel: "haowork-public-agentteams-controller", missionLabel: "msn-001", environmentLabel: "public",
	}
	annotations := map[string]string{missionIDAnnotation: "MSN-001", missionHashAnnotation: strings.Repeat("a", 64)}
	workerLabels := withLabel(labels, functionLabel, deliveryLeaderFunction)
	workerAnnotations := withLabel(annotations, logicalActorAnnotation, "AGT-P005-LEADER")
	worker := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "agentteams.io/v1beta1", "kind": "Worker",
		"metadata": map[string]any{"name": testWorkerName, "namespace": "haowork-public", "uid": "worker-uid", "labels": stringMapAny(workerLabels), "annotations": stringMapAny(workerAnnotations)},
		"spec":     map[string]any{"workerName": testWorkerName, "state": "Running"},
		"status":   map[string]any{"phase": "Running", "matrixUserID": "@leader:matrix.test", "roomID": "!leader:matrix.test"},
	}}
	team := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "agentteams.io/v1beta1", "kind": "Team",
		"metadata": map[string]any{"name": testTeamName, "namespace": "haowork-public", "uid": "team-uid", "labels": stringMapAny(labels), "annotations": stringMapAny(annotations)},
		"spec":     map[string]any{"teamName": testTeamName, "workerMembers": []any{map[string]any{"name": testWorkerName, "role": "team_leader"}}},
		"status": map[string]any{"phase": "Active", "members": []any{map[string]any{
			"name": testWorkerName, "runtimeName": "leader", "role": "team_leader", "matrixUserID": "@leader:matrix.test", "roomID": "!leader:matrix.test", "observed": true, "ready": true, "phase": "Running",
		}}},
	}}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: credentialSecretPrefix + testWorkerName, Namespace: "haowork-public", UID: types.UID("secret-uid"), Labels: map[string]string{controllerLabel: "haowork-public-agentteams-controller", workerLabel: testWorkerName}},
		Data:       map[string][]byte{gatewayKeyName: []byte(testGatewayKey), "WORKER_PASSWORD": []byte("matrix-password"), "WORKER_MINIO_PASSWORD": []byte("minio-password"), "WORKER_MATRIX_TOKEN": []byte("matrix-token")},
	}
	return worker, team, secret
}

func testRuntimeBinding() model.RuntimeBinding {
	return model.RuntimeBinding{LogicalActorID: "AGT-P005-LEADER", Revision: 2, EnvironmentID: "public", AgentTeamsInstanceID: testTeamName, RuntimePrincipalID: "@leader:matrix.test", Status: "active"}
}

func testBinding() WorkerCredentialBinding {
	digest := sha256.Sum256([]byte(testGatewayKey))
	return WorkerCredentialBinding{
		ConsumerName: "worker-leader", LogicalActorID: "AGT-P005-LEADER", RuntimePrincipalID: "@leader:matrix.test",
		EnvironmentID: "public", AgentTeamsInstanceID: testTeamName, BindingRevision: 2, CredentialSHA256: hex.EncodeToString(digest[:]),
		ObservedAt: testNow.Format(time.RFC3339Nano), ExpiresAt: testNow.Add(DefaultBindingTTL).Format(time.RFC3339Nano),
	}
}

func withLabel(source map[string]string, key, value string) map[string]string {
	result := make(map[string]string, len(source)+1)
	for k, v := range source {
		result[k] = v
	}
	result[key] = value
	return result
}

func stringMapAny(source map[string]string) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func TestSamePathAcceptsWindowsLongPathPrefix(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path normalization contract")
	}
	path := filepath.Join(t.TempDir(), "mcp-bindings.json")
	longPath := `\\?\` + path
	if !samePath(path, longPath) {
		t.Fatalf("samePath(%q, %q) = false, want true", path, longPath)
	}
}
