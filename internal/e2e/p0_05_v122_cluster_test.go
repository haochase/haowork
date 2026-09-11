//go:build agentteams_cluster_e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/skillruntime"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestP005V122MatrixJSONAcceptsOfficialExtensions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"start":"opaque-start","end":"opaque-end","chunk":[{"type":"m.room.message","event_id":"$event","sender":"@worker:test","room_id":"!room:test","origin_server_ts":1720000000000,"state_key":"","unsigned":{"age":10},"content":{"org.haowork.run_id":"RUN-001","msgtype":"m.notice","body":"dispatch","m.relates_to":{"rel_type":"m.thread"}}}]}`))
	}))
	defer server.Close()
	t.Setenv("HAOWORK_P005_CLUSTER_PUBLIC_MATRIX_URL", server.URL)
	fixture := &p005V122ClusterFixture{t: t, ctx: context.Background(), publicZone: p005V122Zone{name: "public"}}
	var messages struct {
		Chunk []struct {
			Type    string `json:"type"`
			EventID string `json:"event_id"`
			Content struct {
				RunID string `json:"org.haowork.run_id"`
			} `json:"content"`
		} `json:"chunk"`
	}
	fixture.matrixJSON(http.MethodGet, "/_matrix/client/v3/rooms/%21room%3Atest/messages?dir=b&limit=100", []byte("test-token"), nil, &messages, "BLOCKED_MATRIX_EXTENSION_TEST")
	if len(messages.Chunk) != 1 || messages.Chunk[0].EventID != "$event" || messages.Chunk[0].Content.RunID != "RUN-001" {
		t.Fatalf("official Matrix extension response decoded incorrectly: %#v", messages)
	}
}

func TestP005V122MatrixDecoderRejectsMalformedKnownFieldsAndTrailingJSON(t *testing.T) {
	decode := func(body string) error {
		var messages struct {
			Chunk []struct {
				EventID string `json:"event_id"`
				Content struct {
					RunID string `json:"org.haowork.run_id"`
				} `json:"content"`
			} `json:"chunk"`
		}
		return p005V122DecodeMatrixJSON(strings.NewReader(body), &messages)
	}
	for name, body := range map[string]string{
		"multiple documents": `{"chunk":[]}{"chunk":[]}`,
		"numeric event id":   `{"chunk":[{"event_id":7,"content":{"org.haowork.run_id":"RUN-001"}}]}`,
		"numeric run id":     `{"chunk":[{"event_id":"$event","content":{"org.haowork.run_id":7}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := decode(body); err == nil {
				t.Fatal("Matrix decoder accepted malformed response")
			}
		})
	}
}

func TestP005V122ManagerDenialRequiresNonRPCGatewayResponse(t *testing.T) {
	for name, test := range map[string]struct {
		result p005V122MCPResult
		want   bool
	}{
		"Higress unauthorized": {result: p005V122MCPResult{status: http.StatusUnauthorized, code: "invalid_response"}, want: true},
		"Higress forbidden":    {result: p005V122MCPResult{status: http.StatusForbidden, code: "invalid_response"}, want: true},
		"MCP unauthorized":     {result: p005V122MCPResult{status: http.StatusUnauthorized, isError: true, code: "unauthorized"}},
		"MCP domain rejection": {result: p005V122MCPResult{status: http.StatusOK, isError: true, code: "principal_mismatch"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := p005V122ManagerDeniedAtGateway(test.result); got != test.want {
				t.Fatalf("gateway denial = %t, want %t", got, test.want)
			}
		})
	}
}

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

func TestP005V122DeliveryLeaderSubmitsStructuredReceipt(t *testing.T) {
	fixture := newP005V122ClusterFixture(t)
	for _, zone := range fixture.allZones() {
		fixture.withZone(zone, func() {
			topology := fixture.requireFiveRoleTopology()
			fixture.requireDeliveryLeaderSubmitsStructuredReceipt(topology)
		})
	}
}

func TestP005V122MatrixReplyWithoutReceiptRemainsBlocked(t *testing.T) {
	fixture := newP005V122ClusterFixture(t)
	for _, zone := range fixture.allZones() {
		fixture.withZone(zone, func() {
			topology := fixture.requireFiveRoleTopology()
			fixture.requireMatrixReplyWithoutReceiptRemainsBlocked(topology)
		})
	}
}

func TestP005V122ManagerCannotSubmitLeaderReceipt(t *testing.T) {
	fixture := newP005V122ClusterFixture(t)
	for _, zone := range fixture.allZones() {
		fixture.withZone(zone, func() {
			topology := fixture.requireFiveRoleTopology()
			fixture.requireManagerCannotSubmitLeaderReceipt(topology)
		})
	}
}

func TestP005V122ReceiptArtifactDigestMismatchFailsClosed(t *testing.T) {
	fixture := newP005V122ClusterFixture(t)
	for _, zone := range fixture.allZones() {
		fixture.withZone(zone, func() {
			topology := fixture.requireFiveRoleTopology()
			fixture.requireReceiptArtifactDigestMismatchFailsClosed(topology)
		})
	}
}

func TestP005V122CrossNamespaceReceiptCallIsDenied(t *testing.T) {
	fixture := newP005V122ClusterFixture(t)
	fixture.requireCrossNamespaceReceiptCallDenied()
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

func TestP005V122RestartKeepsOneReceiptAndOneTrace(t *testing.T) {
	fixture := newP005V122ClusterFixture(t)
	for _, zone := range fixture.allZones() {
		fixture.withZone(zone, func() {
			topology := fixture.requireFiveRoleTopology()
			fixture.requireRestartKeepsOneReceiptAndOneTrace(topology)
		})
	}
}

func TestP005V122ExecutionAssignmentIDAcceptedByReceiptSkillSchema(t *testing.T) {
	registry, err := skillruntime.Load(filepath.Join(p005V122FindRepoRoot(t), "skills"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := registry.Resolve("submit_execution_report", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"assignment_id": "ASN-46fc8933c009dd44765c6f4b01aeaf9d",
		"status":        "succeeded", "summary_sha256": strings.Repeat("a", 64),
		"artifacts": []any{}, "checks": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := skillruntime.ValidateJSONInput(definition, payload); err != nil {
		t.Fatalf("real deterministic Assignment ID rejected by receipt Skill schema: %v", err)
	}
}

func TestP005V122ZoneSwitchRefreshesMissionConfig(t *testing.T) {
	fixture := &p005V122ClusterFixture{
		t:            t,
		publicZone:   p005V122Zone{name: "public", namespace: p005V122PublicNamespace, release: "haowork-public-agentteams"},
		internalZone: p005V122Zone{name: "internal", namespace: p005V122InternalNamespace, release: "haowork-internal-agentteams"},
	}
	set := map[string]string{
		"HAOWORK_P005_CLUSTER_MISSION_ID":               "MSN-PUBLIC",
		"HAOWORK_P005_CLUSTER_CONTROLLER_NAME":          "controller-public",
		"HAOWORK_P005_CLUSTER_MODEL":                    "model-public",
		"HAOWORK_P005_CLUSTER_MANAGER_RUNTIME":          "openclaw",
		"HAOWORK_P005_CLUSTER_MANAGER_IMAGE":            "registry.example.test/manager@sha256:" + strings.Repeat("a", 64),
		"HAOWORK_P005_CLUSTER_WORKER_RUNTIME":           "openclaw",
		"HAOWORK_P005_CLUSTER_MCP_URL":                  "http://public.example.test/mcp",
		"HAOWORK_P005_CLUSTER_MCP_SERVER_NAME":          "haowork-mcp",
		"HAOWORK_P005_CLUSTER_MCP_TRANSPORT":            "http",
		"HAOWORK_P005_CLUSTER_HUMAN_NAME":               "public-owner",
		"HAOWORK_P005_CLUSTER_INTERNAL_MISSION_ID":      "MSN-INTERNAL",
		"HAOWORK_P005_CLUSTER_INTERNAL_CONTROLLER_NAME": "controller-internal",
		"HAOWORK_P005_CLUSTER_INTERNAL_MODEL":           "model-internal",
		"HAOWORK_P005_CLUSTER_INTERNAL_MANAGER_IMAGE":   "registry.example.test/manager@sha256:" + strings.Repeat("b", 64),
		"HAOWORK_P005_CLUSTER_INTERNAL_MCP_URL":         "http://internal.example.test/mcp",
		"HAOWORK_P005_CLUSTER_INTERNAL_HUMAN_NAME":      "internal-owner",
	}
	for key, value := range set {
		t.Setenv(key, value)
	}
	fixture.withZone(fixture.publicZone, func() {
		public := fixture.missionConfigForPublicZone()
		if public.model != "model-public" || public.controllerName != "controller-public" {
			t.Fatalf("public mission config = %#v", public)
		}
		fixture.missionConfig = p005V122MissionConfig{}
		fixture.publicZone = fixture.internalZone
		fixture.internalZone = p005V122Zone{name: "public", namespace: p005V122PublicNamespace, release: "haowork-public-agentteams"}
		for key, value := range map[string]string{
			"HAOWORK_P005_CLUSTER_MISSION_ID":      "MSN-INTERNAL",
			"HAOWORK_P005_CLUSTER_CONTROLLER_NAME": "controller-internal",
			"HAOWORK_P005_CLUSTER_MODEL":           "model-internal",
			"HAOWORK_P005_CLUSTER_MANAGER_IMAGE":   set["HAOWORK_P005_CLUSTER_INTERNAL_MANAGER_IMAGE"],
			"HAOWORK_P005_CLUSTER_MCP_URL":         "http://internal.example.test/mcp",
			"HAOWORK_P005_CLUSTER_HUMAN_NAME":      "internal-owner",
		} {
			t.Setenv(key, value)
		}
		internal := fixture.missionConfigForPublicZone()
		if internal.model != "model-internal" || internal.controllerName != "controller-internal" || internal.missionID != "MSN-INTERNAL" {
			t.Fatalf("internal mission config reused public values = %#v", internal)
		}
	})
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
	sourceLabels := map[string]string{"app.kubernetes.io/name": "haowork-network-probe", "haowork.io/zone": "public"}
	targetLabels := map[string]string{"kubernetes.io/metadata.name": "haowork-internal", "haowork.io/zone": "internal"}
	policy := networkingv1.NetworkPolicy{
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: "app.kubernetes.io/name", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"haowork-higress-binding-publisher"},
			}}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"haowork.io/zone": "public"}},
			}}}},
		},
	}
	if !p005V122PolicyDeniesOppositeNamespace(policy, sourceLabels, targetLabels) {
		t.Fatal("default deny policy should prove public-to-internal egress denial")
	}
	if p005V122PolicyDeniesOppositeNamespace(policy, map[string]string{"app.kubernetes.io/name": "haowork-higress-binding-publisher"}, targetLabels) {
		t.Fatal("policy that does not select the source Pod was accepted as denial evidence")
	}
	policy.Spec.Egress = append(policy.Spec.Egress, networkingv1.NetworkPolicyEgressRule{To: []networkingv1.NetworkPolicyPeer{{
		NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"haowork.io/zone": "internal"}},
	}}})
	if p005V122PolicyDeniesOppositeNamespace(policy, sourceLabels, targetLabels) {
		t.Fatal("policy allowing the opposite zone was accepted as denial evidence")
	}
	policy.Spec.Egress[len(policy.Spec.Egress)-1].To[0].NamespaceSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "haowork-internal"}}
	if p005V122PolicyDeniesOppositeNamespace(policy, sourceLabels, targetLabels) {
		t.Fatal("policy allowing the opposite namespace by name was accepted as denial evidence")
	}
	policy.Spec.Egress[len(policy.Spec.Egress)-1].To = []networkingv1.NetworkPolicyPeer{{}}
	if p005V122PolicyDeniesOppositeNamespace(policy, sourceLabels, targetLabels) {
		t.Fatal("empty egress peer was accepted as denial evidence")
	}
	policy.Spec.Egress[len(policy.Spec.Egress)-1].To = []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/8"}}}
	if p005V122PolicyDeniesOppositeNamespace(policy, sourceLabels, targetLabels) {
		t.Fatal("opaque IPBlock was accepted as denial evidence")
	}
}

type errProbe string

func (err errProbe) Error() string { return string(err) }
