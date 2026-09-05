//go:build agentteams_cluster_e2e

package e2e_test

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// These acceptance tests intentionally require a live, officially deployed
// AgentTeams v1.2.2 cluster. They never replace the cluster with fakes or skip
// missing prerequisites: an unavailable prerequisite is a blocked delivery.
func TestP005V122OfficialClusterBaseline(t *testing.T) {
	fixture := newP005V122ClusterFixture(t)
	fixture.requireOfficialBaseline()
}

func TestP005V122FiveRoleTopologyAndSkillDelivery(t *testing.T) {
	fixture := newP005V122ClusterFixture(t)
	topology := fixture.requireFiveRoleTopology()
	fixture.requireRoleScopedSkills(topology)
}

func TestP005V122MatrixArtifactAndMCPDataPath(t *testing.T) {
	fixture := newP005V122ClusterFixture(t)
	topology := fixture.requireFiveRoleTopology()
	fixture.requireMatrixArtifactAndMCPDataPath(topology)
}

func TestP005V122CrossNamespaceTrafficIsDenied(t *testing.T) {
	fixture := newP005V122ClusterFixture(t)
	fixture.requireCrossNamespaceTrafficDenied()
}

func TestP005V122CrossZoneProbeTargetsUseOfficialHelmServices(t *testing.T) {
	targets, err := p005V122DefaultCrossZoneProbeTargets(p005V122PublicNamespace)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string, len(targets))
	for _, target := range targets {
		got[target.Component] = target.URL
	}
	want := map[string]string{
		"matrix":  "http://haowork-internal-agentteams-tuwunel.haowork-internal.svc.cluster.local:6167/_matrix/client/versions",
		"minio":   "http://haowork-internal-agentteams-minio.haowork-internal.svc.cluster.local:9000/minio/health/live",
		"higress": "http://higress-gateway.haowork-internal.svc.cluster.local/",
	}
	if len(got) != len(want) {
		t.Fatalf("probe targets = %#v, want only deployed dual-zone services %#v", got, want)
	}
	for component, expected := range want {
		if got[component] != expected {
			t.Fatalf("%s target=%q, want %q", component, got[component], expected)
		}
	}
}

func TestP005V122SelectsUniqueRunningProbePod(t *testing.T) {
	pods := []corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Name: "haowork-network-probe-old"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "probe"}}}, Status: corev1.PodStatus{Phase: corev1.PodFailed}},
		{ObjectMeta: metav1.ObjectMeta{Name: "haowork-network-probe-7c9d954dd6-8mjw2"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "probe"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}},
	}
	name, err := p005V122UniqueRunningProbePod(pods)
	if err != nil || name != "haowork-network-probe-7c9d954dd6-8mjw2" {
		t.Fatalf("selected probe=%q err=%v", name, err)
	}
}

func TestP005V122ExecutionIDProducesDistinctGovernedRunIDs(t *testing.T) {
	data, err := p005V122GovernedRunID("data", "e2e-7f3a")
	if err != nil {
		t.Fatal(err)
	}
	restart, err := p005V122GovernedRunID("restart", "e2e-7f3a")
	if err != nil {
		t.Fatal(err)
	}
	if data == restart || data != "RUN-P005-V122-DATA-E2E-7F3A" || restart != "RUN-P005-V122-RESTART-E2E-7F3A" {
		t.Fatalf("run IDs data=%q restart=%q", data, restart)
	}
}

func TestP005V122ExecutionIDProducesDistinctInvocationIDs(t *testing.T) {
	first, err := p005V122GovernedInvocationID("E2E-7f3a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := p005V122GovernedInvocationID("E2E-91bc")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || first != "INV-P005-V122-E2E-7F3A" || second != "INV-P005-V122-E2E-91BC" {
		t.Fatalf("invocation IDs first=%q second=%q", first, second)
	}
}

func TestP005V122BindingRolloutAnnotationsChangeWithSecretResourceVersion(t *testing.T) {
	first, err := p005V122BindingRolloutAnnotations([]byte(`{"bindings":[]}`), "120")
	if err != nil {
		t.Fatal(err)
	}
	second, err := p005V122BindingRolloutAnnotations([]byte(`{"bindings":[]}`), "121")
	if err != nil {
		t.Fatal(err)
	}
	if first["haowork.io/runtime-binding-sha256"] != second["haowork.io/runtime-binding-sha256"] {
		t.Fatal("same binding document must retain its content digest")
	}
	if first["haowork.io/runtime-binding-resource-version"] == second["haowork.io/runtime-binding-resource-version"] {
		t.Fatal("updated Secret resource version must force a new MCP Pod template")
	}
}

func TestP005V122MissionConfigRequiresPinnedManagerImage(t *testing.T) {
	image := p005V122RequiredManagerImage("registry.example.test/agentteams-manager:v1.2.2@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if !strings.Contains(image, "@sha256:") {
		t.Fatalf("manager image = %q", image)
	}
}

func TestP005V122TopologyWaitCoversObservedColdStart(t *testing.T) {
	if p005V122TopologyReadyTimeout < 8*time.Minute {
		t.Fatalf("topology timeout = %s", p005V122TopologyReadyTimeout)
	}
}

func TestP005V122CoreBridgeClientBudgetExceedsServerRunBudget(t *testing.T) {
	if p005V122CoreBridgeClientTimeout != 4*time.Minute {
		t.Fatalf("Core Bridge E2E client timeout = %s", p005V122CoreBridgeClientTimeout)
	}
}

func TestP005V122FixtureBudgetCoversTopologyAndPostChecks(t *testing.T) {
	if p005V122FixtureTimeout-p005V122TopologyReadyTimeout < 7*time.Minute {
		t.Fatalf("fixture timeout %s does not leave enough post-topology budget", p005V122FixtureTimeout)
	}
}

func TestP005V122RestartResumesWithoutDuplicateGovernanceEvents(t *testing.T) {
	fixture := newP005V122ClusterFixture(t)
	topology := fixture.requireFiveRoleTopology()
	fixture.requireRestartResumeWithoutDuplicateGovernanceEvents(topology)
}

func TestP005V122EvidenceRejectsSensitiveFields(t *testing.T) {
	if err := p005V122RejectSensitiveEvidence(map[string]any{
		"baseline": map[string]any{"authorization": "Bearer private-value"},
	}); err == nil {
		t.Fatal("sensitive evidence field was accepted")
	}
	if err := p005V122RejectSensitiveEvidence(map[string]any{
		"baseline": map[string]any{"cluster_name": "haowork-p005-v122"},
	}); err != nil {
		t.Fatalf("safe evidence was rejected: %v", err)
	}
}

func TestP005V122ProbeClassificationRequiresPolicyEvidenceForAmbiguousNetworkErrors(t *testing.T) {
	for _, test := range []struct {
		name           string
		err            error
		stderr         string
		policyVerified bool
		wantOK         bool
	}{
		{name: "timeout", err: errProbe("exit code 4"), stderr: "wget: download timed out", wantOK: true},
		{name: "explicit egress deny", err: errProbe("exit code 4"), stderr: "egress denied by NetworkPolicy", wantOK: true},
		{name: "refused without policy evidence", err: errProbe("exit code 4"), stderr: "connection refused", wantOK: false},
		{name: "refused with policy evidence", err: errProbe("exit code 4"), stderr: "connection refused", policyVerified: true, wantOK: true},
		{name: "http forbidden is connected", err: errProbe("exit code 8"), stderr: "HTTP/1.1 403 Forbidden", policyVerified: true, wantOK: false},
		{name: "missing wget is not a policy result", err: errProbe("exit code 1"), stderr: "wget: not found", policyVerified: true, wantOK: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := p005V122ClassifyProbeResult(test.err, test.stderr, test.policyVerified)
			if (err == nil) != test.wantOK {
				t.Fatalf("classification err=%v, wantOK=%t", err, test.wantOK)
			}
		})
	}
}

func TestP005V122PolicyDenialRequiresDefaultDenyWithoutOppositeZoneAllowance(t *testing.T) {
	policy := networkingv1.NetworkPolicy{
		Spec: networkingv1.NetworkPolicySpec{
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"haowork.io/zone": "public"}},
			}}}},
		},
	}
	if !p005V122PolicyDeniesOppositeNamespace(policy, "public", "haowork-internal") {
		t.Fatal("default deny policy should prove public-to-internal egress denial")
	}
	policy.Spec.Egress = append(policy.Spec.Egress, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"haowork.io/zone": "internal"}},
	}}})
	if p005V122PolicyDeniesOppositeNamespace(policy, "public", "haowork-internal") {
		t.Fatal("policy allowing the opposite zone was accepted as denial evidence")
	}
}

type errProbe string

func (err errProbe) Error() string { return string(err) }
