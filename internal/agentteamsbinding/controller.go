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
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/haochase/haowork/internal/model"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

const (
	controllerLabel        = "agentteams.io/controller"
	workerLabel            = "agentteams.io/worker"
	missionLabel           = "haowork.io/mission"
	environmentLabel       = "haowork.io/environment"
	functionLabel          = "haowork.io/agent-function"
	missionIDAnnotation    = "haowork.io/mission-id"
	missionHashAnnotation  = "haowork.io/mission-hash"
	logicalActorAnnotation = "haowork.io/logical-actor-id"

	deliveryLeaderFunction = "delivery_leader"
	credentialSecretPrefix = "agentteams-creds-"
	gatewayKeyName         = "WORKER_GATEWAY_KEY"
	bindingRelativePath    = "mcp-bindings.json"
)

var (
	workerGVR = schema.GroupVersionResource{Group: "agentteams.io", Version: "v1beta1", Resource: "workers"}
	teamGVR   = schema.GroupVersionResource{Group: "agentteams.io", Version: "v1beta1", Resource: "teams"}

	ErrBlockedWorkerCredentialBinding = errors.New("BLOCKED_WORKER_CREDENTIAL_BINDING")
	ErrWorkerCredentialBindingPending = errors.New("Worker credential binding pending")
	ErrRuntimeBindingNotFound         = errors.New("active RuntimeBinding not found")
	ErrUnsafeBindingPath              = errors.New("unsafe runtime binding path")
)

// WorkerCredentialBinding is the credential-free identity document shared
// with the MCP host and the route publisher.
type WorkerCredentialBinding struct {
	ConsumerName         string `json:"consumer_name"`
	LogicalActorID       string `json:"logical_actor_id"`
	RuntimePrincipalID   string `json:"runtime_principal_id"`
	EnvironmentID        string `json:"environment_id"`
	AgentTeamsInstanceID string `json:"agentteams_instance_id"`
	BindingRevision      int    `json:"binding_revision"`
	CredentialSHA256     string `json:"credential_sha256"`
	ObservedAt           string `json:"observed_at"`
	ExpiresAt            string `json:"expires_at"`
}

type RuntimeBindingReader interface {
	CurrentRuntimeBinding(context.Context, string) (model.RuntimeBinding, error)
}

type BindingPublisher interface {
	Publish(context.Context, WorkerCredentialBinding) error
}

type bindingRevoker interface {
	RemoveInstance(context.Context, string) error
}

type bindingPruner interface {
	RetainInstances(context.Context, map[string]struct{}) error
}

type Controller struct {
	Dynamic            dynamic.Interface
	Core               kubernetes.Interface
	Namespace          string
	ControllerName     string
	AllowedSecretNames []string
	RuntimeBindings    RuntimeBindingReader
	Bindings           BindingPublisher
	Clock              func() time.Time
	BindingTTL         time.Duration
	ResyncInterval     time.Duration
	ReconcileAttempts  int
	RetryInterval      time.Duration
}

// ReconcileDeliveryLeader re-reads every authoritative object around the
// Secret access. A changing object is rejected and retried by the watch loop.
func (controller Controller) ReconcileDeliveryLeader(ctx context.Context, workerName string) (result WorkerCredentialBinding, resultErr error) {
	if err := controller.validate(); err != nil {
		return WorkerCredentialBinding{}, err
	}
	workerName = strings.TrimSpace(workerName)
	if !dnsSubdomain(workerName) || !strings.HasSuffix(workerName, "-delivery-leader") {
		return WorkerCredentialBinding{}, blocked("invalid Delivery Leader Worker name")
	}
	teamName := strings.TrimSuffix(workerName, "-delivery-leader")
	defer func() {
		if resultErr == nil {
			return
		}
		if revoker, ok := controller.Bindings.(bindingRevoker); ok {
			if revokeErr := revoker.RemoveInstance(ctx, teamName); revokeErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("revoke runtime consumer binding: %w", revokeErr))
			}
		}
	}()

	firstWorker, firstTeam, firstBinding, facts, err := controller.readFacts(ctx, workerName)
	if err != nil {
		return WorkerCredentialBinding{}, err
	}
	secretName := credentialSecretPrefix + facts.credentialName
	firstSecret, firstDigest, err := controller.readCredentialDigest(ctx, secretName, firstWorker.GetUID(), facts)
	if err != nil {
		return WorkerCredentialBinding{}, err
	}

	secondWorker, secondTeam, secondBinding, secondFacts, err := controller.readFacts(ctx, workerName)
	if err != nil {
		return WorkerCredentialBinding{}, err
	}
	if !sameObservedObject(firstWorker, secondWorker) || !sameObservedObject(firstTeam, secondTeam) ||
		!reflect.DeepEqual(firstBinding, secondBinding) || !reflect.DeepEqual(facts, secondFacts) {
		return WorkerCredentialBinding{}, blocked("runtime identity changed during credential resolution")
	}
	secondSecret, secondDigest, err := controller.readCredentialDigest(ctx, secretName, secondWorker.GetUID(), secondFacts)
	if err != nil {
		return WorkerCredentialBinding{}, err
	}
	if !sameSecretIdentity(firstSecret, secondSecret) || firstDigest != secondDigest {
		return WorkerCredentialBinding{}, blocked("Worker credential changed during resolution")
	}

	observedAt := controller.now()
	result = WorkerCredentialBinding{
		ConsumerName:         "worker-" + facts.runtimeName,
		LogicalActorID:       secondBinding.LogicalActorID,
		RuntimePrincipalID:   secondBinding.RuntimePrincipalID,
		EnvironmentID:        secondBinding.EnvironmentID,
		AgentTeamsInstanceID: secondBinding.AgentTeamsInstanceID,
		BindingRevision:      secondBinding.Revision,
		CredentialSHA256:     secondDigest,
		ObservedAt:           observedAt.Format(time.RFC3339Nano),
		ExpiresAt:            observedAt.Add(controller.effectiveBindingTTL()).Format(time.RFC3339Nano),
	}
	if err := validatePublishedBinding(result, observedAt); err != nil {
		return WorkerCredentialBinding{}, err
	}
	if err := controller.Bindings.Publish(ctx, result); err != nil {
		return WorkerCredentialBinding{}, fmt.Errorf("publish runtime consumer binding: %w", err)
	}
	return result, nil
}

func (controller Controller) validate() error {
	if controller.Dynamic == nil || controller.Core == nil || controller.RuntimeBindings == nil || controller.Bindings == nil ||
		!dnsLabel(strings.TrimSpace(controller.Namespace)) || strings.TrimSpace(controller.ControllerName) == "" {
		return errors.New("binding controller dependencies, namespace, and controller name are required")
	}
	if controller.effectiveBindingTTL() <= 0 || controller.effectiveBindingTTL() > MaxBindingTTL || len(controller.AllowedSecretNames) == 0 {
		return errors.New("binding controller TTL and allowed Secret names are required")
	}
	seen := make(map[string]struct{}, len(controller.AllowedSecretNames))
	for _, name := range controller.AllowedSecretNames {
		name = strings.TrimSpace(name)
		if !strings.HasPrefix(name, credentialSecretPrefix) || !dnsSubdomain(name) {
			return errors.New("binding controller allowed Secret name is invalid")
		}
		if _, exists := seen[name]; exists {
			return errors.New("binding controller allowed Secret names must be unique")
		}
		seen[name] = struct{}{}
	}
	return nil
}

func (controller Controller) effectiveBindingTTL() time.Duration {
	if controller.BindingTTL == 0 {
		return DefaultBindingTTL
	}
	return controller.BindingTTL
}

func (controller Controller) now() time.Time {
	if controller.Clock != nil {
		return controller.Clock().UTC()
	}
	return time.Now().UTC()
}

func (controller Controller) reconcileDeliveryLeaderWithRetry(ctx context.Context, workerName string) error {
	attempts := controller.ReconcileAttempts
	if attempts <= 0 {
		attempts = 3
	}
	interval := controller.RetryInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if _, err := controller.ReconcileDeliveryLeader(ctx, workerName); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt == attempts {
			break
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ctx.Err(), lastErr)
		case <-timer.C:
		}
	}
	return fmt.Errorf("binding reconciliation exhausted after %d attempts: %w", attempts, lastErr)
}

type observedFacts struct {
	teamName, credentialName, runtimeName, logicalActorID, principalID, environmentID string
}

func (controller Controller) readFacts(ctx context.Context, workerName string) (*unstructured.Unstructured, *unstructured.Unstructured, model.RuntimeBinding, observedFacts, error) {
	worker, err := controller.Dynamic.Resource(workerGVR).Namespace(controller.Namespace).Get(ctx, workerName, metav1.GetOptions{})
	if err != nil {
		if onlyKubernetesNotFound(err) {
			return nil, nil, model.RuntimeBinding{}, observedFacts{}, pending("Delivery Leader Worker is not available yet")
		}
		return nil, nil, model.RuntimeBinding{}, observedFacts{}, blockedWithCause("Delivery Leader Worker is unavailable", err)
	}
	facts, workerReady, err := controller.validateWorker(worker, workerName)
	if err != nil {
		return nil, nil, model.RuntimeBinding{}, observedFacts{}, err
	}
	team, err := controller.Dynamic.Resource(teamGVR).Namespace(controller.Namespace).Get(ctx, facts.teamName, metav1.GetOptions{})
	teamReady := false
	if err != nil {
		if !onlyKubernetesNotFound(err) {
			return nil, nil, model.RuntimeBinding{}, observedFacts{}, blockedWithCause("Delivery Leader Team is unavailable", err)
		}
		team = nil
	} else {
		teamReady, err = controller.validateTeam(team, worker, &facts)
		if err != nil {
			return nil, nil, model.RuntimeBinding{}, observedFacts{}, err
		}
	}
	binding, err := controller.RuntimeBindings.CurrentRuntimeBinding(ctx, facts.logicalActorID)
	bindingReady := true
	if onlyRuntimeBindingNotFound(err) {
		bindingReady = false
	} else if err != nil {
		return nil, nil, model.RuntimeBinding{}, observedFacts{}, blockedWithCause("read published RuntimeBinding", err)
	} else if !validRuntimeBinding(binding, facts) {
		return nil, nil, model.RuntimeBinding{}, observedFacts{}, blocked("published RuntimeBinding does not match observed Delivery Leader")
	}
	if !workerReady || !teamReady || !bindingReady {
		if !bindingReady {
			return nil, nil, model.RuntimeBinding{}, observedFacts{}, errors.Join(
				pending("Delivery Leader runtime identity is not fully observed yet"),
				ErrRuntimeBindingNotFound,
			)
		}
		return nil, nil, model.RuntimeBinding{}, observedFacts{}, pending("Delivery Leader runtime identity is not fully observed yet")
	}
	return worker, team, binding, facts, nil
}

func (controller Controller) validateWorker(worker *unstructured.Unstructured, workerName string) (observedFacts, bool, error) {
	if worker == nil || worker.GetAPIVersion() != "agentteams.io/v1beta1" || worker.GetKind() != "Worker" ||
		worker.GetName() != workerName || worker.GetNamespace() != controller.Namespace || worker.GetUID() == "" || worker.GetDeletionTimestamp() != nil {
		return observedFacts{}, false, blocked("invalid Delivery Leader Worker identity")
	}
	labels := worker.GetLabels()
	annotations := worker.GetAnnotations()
	missionID := strings.TrimSpace(annotations[missionIDAnnotation])
	missionHash := strings.TrimSpace(annotations[missionHashAnnotation])
	logicalActorID := strings.TrimSpace(annotations[logicalActorAnnotation])
	environmentID := strings.TrimSpace(labels[environmentLabel])
	if labels[controllerLabel] != controller.ControllerName || labels[functionLabel] != deliveryLeaderFunction ||
		labels[missionLabel] == "" || missionID == "" || environmentID == "" || logicalActorID == "" || !sha256Hex(missionHash) {
		return observedFacts{}, false, blocked("Delivery Leader Worker governance metadata is invalid")
	}
	specName, specFound, specErr := unstructured.NestedString(worker.Object, "spec", "workerName")
	if specErr != nil || !specFound || specName != workerName {
		return observedFacts{}, false, blocked("Delivery Leader Worker spec is invalid")
	}
	phase, phaseFound, phaseErr := unstructured.NestedString(worker.Object, "status", "phase")
	principal, principalFound, principalErr := unstructured.NestedString(worker.Object, "status", "matrixUserID")
	roomID, roomFound, roomErr := unstructured.NestedString(worker.Object, "status", "roomID")
	if phaseErr != nil || principalErr != nil || roomErr != nil {
		return observedFacts{}, false, blocked("Delivery Leader Worker status is invalid")
	}
	phaseReady, err := workerPhaseReady(phase, phaseFound)
	if err != nil {
		return observedFacts{}, false, err
	}
	teamName := strings.TrimSuffix(workerName, "-delivery-leader")
	if !dnsSubdomain(teamName) {
		return observedFacts{}, false, blocked("Delivery Leader Team name is invalid")
	}
	facts := observedFacts{teamName: teamName, credentialName: workerName, logicalActorID: logicalActorID, environmentID: environmentID}
	if principalFound {
		facts.principalID = principal
	}
	return facts, phaseReady && principalFound && principal != "" && roomFound && roomID != "", nil
}

func workerPhaseReady(phase string, found bool) (bool, error) {
	if !found || phase == "" {
		return false, nil
	}
	switch phase {
	case "Running":
		return true, nil
	case "Pending", "Starting", "Updating":
		return false, nil
	default:
		return false, blocked("Delivery Leader Worker phase is not eligible for binding")
	}
}

func (controller Controller) validateTeam(team, worker *unstructured.Unstructured, facts *observedFacts) (bool, error) {
	if team == nil || team.GetAPIVersion() != "agentteams.io/v1beta1" || team.GetKind() != "Team" ||
		team.GetName() != facts.teamName || team.GetNamespace() != controller.Namespace || team.GetUID() == "" || team.GetDeletionTimestamp() != nil {
		return false, blocked("invalid Delivery Leader Team identity")
	}
	workerLabels, teamLabels := worker.GetLabels(), team.GetLabels()
	workerAnnotations, teamAnnotations := worker.GetAnnotations(), team.GetAnnotations()
	for _, key := range []string{controllerLabel, missionLabel, environmentLabel} {
		if teamLabels[key] == "" || teamLabels[key] != workerLabels[key] {
			return false, blocked("Worker and Team labels do not match")
		}
	}
	for _, key := range []string{missionIDAnnotation, missionHashAnnotation} {
		if teamAnnotations[key] == "" || teamAnnotations[key] != workerAnnotations[key] {
			return false, blocked("Worker and Team mission annotations do not match")
		}
	}
	specName, specFound, specErr := unstructured.NestedString(team.Object, "spec", "teamName")
	if specErr != nil || !specFound || specName != facts.teamName || !teamReferencesLeader(team, worker.GetName()) {
		return false, blocked("Team does not own a valid Delivery Leader reference")
	}
	phase, phaseFound, phaseErr := unstructured.NestedString(team.Object, "status", "phase")
	if phaseErr != nil {
		return false, blocked("Team status is invalid")
	}
	phaseReady, phaseReadinessErr := teamPhaseReady(phase, phaseFound)
	members, found, err := unstructured.NestedSlice(team.Object, "status", "members")
	if err != nil {
		return false, blocked("Team member status is invalid")
	}
	if !found {
		if phaseReadinessErr != nil {
			return false, phaseReadinessErr
		}
		return false, nil
	}
	seenNames := make(map[string]struct{}, len(members))
	var leader observedMemberFacts
	leaderFound := false
	for _, raw := range members {
		member, ok := raw.(map[string]any)
		if !ok {
			return false, blocked("Team member status is invalid")
		}
		memberFacts, err := validateObservedMember(member)
		if err != nil {
			return false, err
		}
		if _, duplicate := seenNames[memberFacts.name]; duplicate {
			return false, blocked("Team member status names must be unique")
		}
		seenNames[memberFacts.name] = struct{}{}
		expectedRole, referenced := teamMemberRole(team, memberFacts.name)
		if !referenced || (memberFacts.role != "" && memberFacts.role != expectedRole) {
			return false, blocked("Team member status does not match Team spec")
		}
		if memberFacts.name != worker.GetName() {
			continue
		}
		leader, leaderFound = memberFacts, true
	}
	if phaseReadinessErr != nil {
		return false, phaseReadinessErr
	}
	if !leaderFound {
		return false, nil
	}
	leaderReady, err := validateLeaderStatus(leader, facts)
	if err != nil {
		return false, err
	}
	return phaseReady && leaderReady, nil
}

func teamPhaseReady(phase string, found bool) (bool, error) {
	if !found || phase == "" || phase == "Pending" || phase == "Degraded" {
		return false, nil
	}
	if phase == "Active" {
		return true, nil
	}
	return false, blocked("Delivery Leader Team phase is not eligible for binding")
}

type observedMemberFacts struct {
	name, role, runtimeName, principalID string
	ready                                bool
}

func validateObservedMember(member map[string]any) (observedMemberFacts, error) {
	name, ok := member["name"].(string)
	if !ok || !dnsSubdomain(name) {
		return observedMemberFacts{}, blocked("Team member identity is invalid")
	}
	result := observedMemberFacts{name: name, runtimeName: name}
	if rawRole, found := member["role"]; found {
		role, ok := rawRole.(string)
		if !ok || (role != "team_leader" && role != "worker") {
			return observedMemberFacts{}, blocked("Team member role is invalid")
		}
		result.role = role
	}
	if rawRuntimeName, found := member["runtimeName"]; found {
		runtimeName, ok := rawRuntimeName.(string)
		if !ok || (runtimeName != "" && !dnsSubdomain(runtimeName)) {
			return observedMemberFacts{}, blocked("Team member runtime name is invalid")
		}
		if runtimeName != "" {
			result.runtimeName = runtimeName
		}
	}
	if rawPrincipal, found := member["matrixUserID"]; found {
		principalID, ok := rawPrincipal.(string)
		if !ok {
			return observedMemberFacts{}, blocked("Team member principal is invalid")
		}
		result.principalID = principalID
	}
	if result.principalID != "" && !strings.HasPrefix(result.principalID, "@"+result.runtimeName+":") {
		return observedMemberFacts{}, blocked("Team member principal does not match runtime name")
	}
	if rawRoomID, found := member["roomID"]; found {
		if _, ok := rawRoomID.(string); !ok {
			return observedMemberFacts{}, blocked("Team member room is invalid")
		}
	}
	observed, observedFound := member["observed"]
	ready, readyFound := member["ready"]
	memberPhase, phaseFound := member["phase"]
	observedValue, observedTypeOK := observed.(bool)
	readyValue, readyTypeOK := ready.(bool)
	phaseValue, phaseTypeOK := memberPhase.(string)
	if (observedFound && !observedTypeOK) || (readyFound && !readyTypeOK) || (phaseFound && !phaseTypeOK) {
		return observedMemberFacts{}, blocked("Team member readiness status is invalid")
	}
	phaseReady, err := workerPhaseReady(phaseValue, phaseFound)
	if err != nil {
		return observedMemberFacts{}, blocked("Team member phase is not eligible for binding")
	}
	result.ready = result.role != "" && result.principalID != "" && observedFound && observedValue && readyFound && readyValue && phaseReady
	return result, nil
}

func validateLeaderStatus(leader observedMemberFacts, facts *observedFacts) (bool, error) {
	if leader.role != "" && leader.role != "team_leader" {
		return false, blocked("Team Delivery Leader role does not match Team spec")
	}
	if facts.principalID != "" && leader.principalID != "" && leader.principalID != facts.principalID {
		return false, blocked("Team Delivery Leader principal does not match Worker status")
	}
	if facts.principalID == "" && leader.principalID != "" {
		facts.principalID = leader.principalID
	}
	if facts.principalID != "" && !strings.HasPrefix(facts.principalID, "@"+leader.runtimeName+":") {
		return false, blocked("Team runtime name does not match Matrix principal")
	}
	facts.runtimeName = leader.runtimeName
	return leader.role == "team_leader" && leader.ready, nil
}

func teamMemberRole(team *unstructured.Unstructured, memberName string) (string, bool) {
	members, _, _ := unstructured.NestedSlice(team.Object, "spec", "workerMembers")
	for _, raw := range members {
		member := raw.(map[string]any)
		if member["name"] == memberName {
			return member["role"].(string), true
		}
	}
	return "", false
}

func teamReferencesLeader(team *unstructured.Unstructured, workerName string) bool {
	members, found, err := unstructured.NestedSlice(team.Object, "spec", "workerMembers")
	if err != nil || !found {
		return false
	}
	matches := 0
	seenNames := make(map[string]struct{}, len(members))
	leaderName := ""
	for _, raw := range members {
		member, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		name, nameOK := member["name"].(string)
		role, roleOK := member["role"].(string)
		if !nameOK || !dnsSubdomain(name) || !roleOK || (role != "team_leader" && role != "worker") {
			return false
		}
		if _, duplicate := seenNames[name]; duplicate {
			return false
		}
		seenNames[name] = struct{}{}
		if role == "team_leader" {
			matches++
			leaderName = name
		}
	}
	return matches == 1 && leaderName == workerName
}

func validRuntimeBinding(binding model.RuntimeBinding, facts observedFacts) bool {
	return binding.Status == "active" && binding.Revision > 0 && binding.LogicalActorID == facts.logicalActorID &&
		binding.RuntimePrincipalID != "" && (facts.principalID == "" || binding.RuntimePrincipalID == facts.principalID) && binding.EnvironmentID == facts.environmentID &&
		binding.AgentTeamsInstanceID == facts.teamName
}

func (controller Controller) readCredentialDigest(ctx context.Context, secretName string, workerUID types.UID, facts observedFacts) (*corev1.Secret, string, error) {
	if secretName != credentialSecretPrefix+facts.credentialName || !dnsSubdomain(secretName) {
		return nil, "", blocked("invalid AgentTeams credential Secret name")
	}
	allowed := false
	for _, name := range controller.AllowedSecretNames {
		if strings.TrimSpace(name) == secretName {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, "", blocked("AgentTeams credential Secret is not allowlisted")
	}
	secret, err := controller.Core.CoreV1().Secrets(controller.Namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil || !validCredentialSecret(secret, controller.Namespace, controller.ControllerName, facts.credentialName, workerUID) {
		return nil, "", blocked("AgentTeams credential Secret identity is invalid")
	}
	key := secret.Data[gatewayKeyName]
	if len(key) == 0 {
		return nil, "", blocked("AgentTeams Worker gateway credential is unavailable")
	}
	digest := sha256.Sum256(key)
	clear(key)
	return secret, hex.EncodeToString(digest[:]), nil
}

func validCredentialSecret(secret *corev1.Secret, namespace, controllerName, workerName string, workerUID types.UID) bool {
	if secret == nil || secret.Name != credentialSecretPrefix+workerName || secret.Namespace != namespace ||
		secret.UID == "" || secret.DeletionTimestamp != nil || (secret.Type != "" && secret.Type != corev1.SecretTypeOpaque) ||
		secret.Labels[controllerLabel] != controllerName || secret.Labels[workerLabel] != workerName {
		return false
	}
	owners := secret.GetOwnerReferences()
	if len(owners) == 0 {
		return true // AgentTeams v1.2.2 does not publish OwnerReferences on credential Secrets.
	}
	if len(owners) != 1 {
		return false
	}
	owner := owners[0]
	return owner.APIVersion == "agentteams.io/v1beta1" && owner.Kind == "Worker" && owner.Name == workerName && owner.UID == workerUID
}

func sameObservedObject(left, right *unstructured.Unstructured) bool {
	if left == nil || right == nil {
		return false
	}
	return left.GetUID() == right.GetUID() && left.GetResourceVersion() == right.GetResourceVersion() &&
		left.GetGeneration() == right.GetGeneration() && reflect.DeepEqual(left.Object, right.Object)
}

func sameSecretIdentity(left, right *corev1.Secret) bool {
	if left == nil || right == nil {
		return false
	}
	return left.UID == right.UID && left.ResourceVersion == right.ResourceVersion && left.Name == right.Name &&
		left.Namespace == right.Namespace && reflect.DeepEqual(left.Labels, right.Labels) && reflect.DeepEqual(left.Type, right.Type)
}

func validatePublishedBinding(binding WorkerCredentialBinding, now time.Time) error {
	document := BindingDocument{SchemaVersion: BindingDocumentSchemaVersion, Bindings: []BindingDocumentEntry{toDocumentBinding(binding)}}
	if _, err := NormalizeBindingDocument(document, now, RejectExpiredBindings); err != nil {
		return blocked("derived runtime consumer binding is invalid")
	}
	return nil
}

func blocked(reason string) error {
	return fmt.Errorf("%w: %s", ErrBlockedWorkerCredentialBinding, reason)
}

func blockedWithCause(reason string, cause error) error {
	return fmt.Errorf("%w: %s: %w", ErrBlockedWorkerCredentialBinding, reason, cause)
}

func pending(reason string) error {
	return fmt.Errorf("%w: %s", ErrWorkerCredentialBindingPending, reason)
}

// Run maintains the binding from Worker and Team watches, with a bounded
// resync so file-based RuntimeBinding changes are also observed.
func (controller Controller) Run(ctx context.Context) error {
	if err := controller.validate(); err != nil {
		return err
	}
	if err := controller.reconcileCurrent(ctx); err != nil {
		return err
	}
	selector := controllerLabel + "=" + controller.ControllerName
	workerWatch, err := controller.Dynamic.Resource(workerGVR).Namespace(controller.Namespace).Watch(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("watch AgentTeams Workers: %w", err)
	}
	defer workerWatch.Stop()
	teamWatch, err := controller.Dynamic.Resource(teamGVR).Namespace(controller.Namespace).Watch(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("watch AgentTeams Teams: %w", err)
	}
	defer teamWatch.Stop()
	interval := controller.ResyncInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-workerWatch.ResultChan():
			if !ok {
				return errors.New("AgentTeams Worker watch closed")
			}
			if err := controller.handleWatchEvent(ctx, event); err != nil {
				return err
			}
		case event, ok := <-teamWatch.ResultChan():
			if !ok {
				return errors.New("AgentTeams Team watch closed")
			}
			if err := controller.handleWatchEvent(ctx, event); err != nil {
				return err
			}
		case <-ticker.C:
			if err := controller.reconcileCurrent(ctx); err != nil {
				return err
			}
		}
	}
}

func (controller Controller) reconcileCurrent(ctx context.Context) error {
	selector := controllerLabel + "=" + controller.ControllerName + "," + functionLabel + "=" + deliveryLeaderFunction
	workers, err := controller.Dynamic.Resource(workerGVR).Namespace(controller.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("list Delivery Leader Workers: %w", err)
	}
	currentInstances := make(map[string]struct{}, len(workers.Items))
	for i := range workers.Items {
		name := workers.Items[i].GetName()
		if dnsSubdomain(name) && strings.HasSuffix(name, "-delivery-leader") {
			currentInstances[strings.TrimSuffix(name, "-delivery-leader")] = struct{}{}
		}
	}
	if pruner, ok := controller.Bindings.(bindingPruner); ok {
		if err := pruner.RetainInstances(ctx, currentInstances); err != nil {
			return fmt.Errorf("prune runtime consumer bindings: %w", err)
		}
	}
	for i := range workers.Items {
		if err := controller.reconcileDeliveryLeaderWithRetry(ctx, workers.Items[i].GetName()); err != nil && !onlyBindingPending(err) {
			return err
		}
	}
	return nil
}

func (controller Controller) handleWatchEvent(ctx context.Context, event watch.Event) error {
	object, ok := event.Object.(*unstructured.Unstructured)
	if !ok || object == nil {
		return errors.New("invalid AgentTeams watch event")
	}
	var names []string
	switch object.GetKind() {
	case "Worker":
		if object.GetLabels()[functionLabel] == deliveryLeaderFunction {
			names = []string{object.GetName()}
		}
	case "Team":
		members, _, _ := unstructured.NestedSlice(object.Object, "spec", "workerMembers")
		for _, raw := range members {
			member, ok := raw.(map[string]any)
			if ok && member["role"] == "team_leader" {
				if name, ok := member["name"].(string); ok {
					names = append(names, name)
				}
			}
		}
	}
	for _, name := range names {
		if err := controller.reconcileDeliveryLeaderWithRetry(ctx, name); err != nil && !onlyBindingPending(err) {
			return err
		}
	}
	return nil
}

func onlyBindingPending(err error) bool {
	return everyErrorLeafMatches(err, func(leaf error) bool {
		return errors.Is(leaf, ErrRuntimeBindingNotFound) || errors.Is(leaf, ErrWorkerCredentialBindingPending)
	})
}

func onlyKubernetesNotFound(err error) bool {
	return everyErrorLeafMatches(err, apierrors.IsNotFound)
}

func onlyRuntimeBindingNotFound(err error) bool {
	return everyErrorLeafMatches(err, func(leaf error) bool {
		return errors.Is(leaf, ErrRuntimeBindingNotFound)
	})
}

func everyErrorLeafMatches(err error, match func(error) bool) bool {
	if err == nil || match == nil {
		return false
	}
	if wrapped, ok := err.(interface{ Unwrap() []error }); ok {
		children := wrapped.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !everyErrorLeafMatches(child, match) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		if child := wrapped.Unwrap(); child != nil {
			return everyErrorLeafMatches(child, match)
		}
	}
	return match(err)
}

type FileBindingPublisher struct {
	root, path, lockPath string
	clock                func() time.Time
}

func NewFileBindingPublisher(projectRoot string) (*FileBindingPublisher, error) {
	root, err := filepath.Abs(strings.TrimSpace(projectRoot))
	if err != nil || strings.TrimSpace(projectRoot) == "" {
		return nil, ErrUnsafeBindingPath
	}
	publisher := &FileBindingPublisher{
		root: root, path: filepath.Join(root, bindingRelativePath), lockPath: filepath.Join(root, "mcp-bindings.lock"),
		clock: func() time.Time { return time.Now().UTC() },
	}
	if err := publisher.ensureSafeRuntimeDirectory(); err != nil {
		return nil, err
	}
	return publisher, nil
}

func (publisher *FileBindingPublisher) Publish(ctx context.Context, binding WorkerCredentialBinding) error {
	if publisher == nil || validatePublishedBinding(binding, publisher.now()) != nil {
		return blocked("runtime consumer binding is invalid")
	}
	return publisher.withLock(ctx, func() error {
		document, err := publisher.readDocument()
		if err != nil {
			return err
		}
		replaced := false
		for i := range document.Bindings {
			if document.Bindings[i].Principal.LogicalActorID == binding.LogicalActorID {
				document.Bindings[i] = toDocumentBinding(binding)
				replaced = true
			}
		}
		if !replaced {
			document.Bindings = append(document.Bindings, toDocumentBinding(binding))
		}
		normalized, err := NormalizeBindingDocument(document, publisher.now(), RejectExpiredBindings)
		if err != nil {
			return err
		}
		return publisher.writeDocument(normalized)
	})
}

func (publisher *FileBindingPublisher) RemoveInstance(ctx context.Context, instanceID string) error {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return nil
	}
	return publisher.withLock(ctx, func() error {
		document, err := publisher.readDocument()
		if err != nil {
			return err
		}
		kept := document.Bindings[:0]
		for _, binding := range document.Bindings {
			if binding.Principal.AgentTeamsInstanceID != instanceID {
				kept = append(kept, binding)
			}
		}
		document.Bindings = kept
		return publisher.writeDocument(document)
	})
}

func (publisher *FileBindingPublisher) RetainInstances(ctx context.Context, instanceIDs map[string]struct{}) error {
	if instanceIDs == nil {
		return errors.New("current AgentTeams instance set is required")
	}
	return publisher.withLock(ctx, func() error {
		document, err := publisher.readDocument()
		if err != nil {
			return err
		}
		kept := document.Bindings[:0]
		for _, binding := range document.Bindings {
			if _, exists := instanceIDs[binding.Principal.AgentTeamsInstanceID]; exists {
				kept = append(kept, binding)
			}
		}
		document.Bindings = kept
		return publisher.writeDocument(document)
	})
}

func toDocumentBinding(binding WorkerCredentialBinding) BindingDocumentEntry {
	return BindingDocumentEntry{
		ConsumerName: binding.ConsumerName, CredentialSHA256: binding.CredentialSHA256, ObservedAt: binding.ObservedAt, ExpiresAt: binding.ExpiresAt,
		Principal: BindingDocumentPrincipal{
			LogicalActorID: binding.LogicalActorID, RuntimePrincipalID: binding.RuntimePrincipalID, EnvironmentID: binding.EnvironmentID,
			AgentTeamsInstanceID: binding.AgentTeamsInstanceID, BindingRevision: binding.BindingRevision,
		}}
}

func (publisher *FileBindingPublisher) withLock(ctx context.Context, action func() error) error {
	if err := publisher.ensureSafeRuntimeDirectory(); err != nil {
		return err
	}
	lock := flock.New(publisher.lockPath)
	locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil || !locked {
		return fmt.Errorf("lock runtime binding document: %w", err)
	}
	defer lock.Unlock()
	if err := os.Chmod(publisher.lockPath, 0o600); err != nil {
		return err
	}
	if err := publisher.ensureSafeRuntimeDirectory(); err != nil {
		return err
	}
	return action()
}

func (publisher *FileBindingPublisher) readDocument() (BindingDocument, error) {
	file, err := os.Open(publisher.path)
	if errors.Is(err, os.ErrNotExist) {
		return BindingDocument{SchemaVersion: BindingDocumentSchemaVersion, Bindings: []BindingDocumentEntry{}}, nil
	}
	if err != nil {
		return BindingDocument{}, fmt.Errorf("open runtime binding document: %w", err)
	}
	defer file.Close()
	document, err := DecodeBindingDocument(file)
	if err != nil {
		return BindingDocument{}, err
	}
	return NormalizeBindingDocument(document, publisher.now(), DropExpiredBindings)
}

func (publisher *FileBindingPublisher) writeDocument(document BindingDocument) error {
	normalized, err := NormalizeBindingDocument(document, publisher.now(), RejectExpiredBindings)
	if err != nil {
		return err
	}
	parentBefore, err := os.Open(filepath.Dir(publisher.path))
	if err != nil {
		return err
	}
	defer parentBefore.Close()
	infoBefore, err := parentBefore.Stat()
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(publisher.path), ".mcp-bindings-*.tmp")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(normalized); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	parentAfter, err := os.Open(filepath.Dir(publisher.path))
	if err != nil {
		return err
	}
	infoAfter, statErr := parentAfter.Stat()
	parentAfter.Close()
	if statErr != nil || !os.SameFile(infoBefore, infoAfter) {
		return ErrUnsafeBindingPath
	}
	if err := os.Rename(temporaryName, publisher.path); err != nil {
		return err
	}
	if err := os.Chmod(publisher.path, 0o600); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	return parentBefore.Sync()
}

func (publisher *FileBindingPublisher) ensureSafeRuntimeDirectory() error {
	if publisher == nil || publisher.root == "" || publisher.path != filepath.Join(publisher.root, bindingRelativePath) {
		return ErrUnsafeBindingPath
	}
	if err := safeExistingDirectory(publisher.root); err != nil {
		return err
	}
	if info, err := os.Lstat(publisher.path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafeBindingPath
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (publisher *FileBindingPublisher) now() time.Time {
	if publisher != nil && publisher.clock != nil {
		return publisher.clock().UTC()
	}
	return time.Now().UTC()
}

func safeExistingDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafeBindingPath
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ErrUnsafeBindingPath
	}
	absoluteResolved, err := filepath.Abs(resolved)
	if err != nil || !samePath(path, absoluteResolved) {
		return ErrUnsafeBindingPath
	}
	return nil
}

func samePath(left, right string) bool {
	if filepath.Separator == '\\' {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func sha256Hex(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func dnsLabel(value string) bool {
	if len(value) == 0 || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return false
		}
	}
	return true
}

func dnsSubdomain(value string) bool {
	if len(value) == 0 || len(value) > 253 {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if !dnsLabel(part) {
			return false
		}
	}
	return true
}
