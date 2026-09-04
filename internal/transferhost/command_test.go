package transferhost

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/cli"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/physicalacceptance"
)

func TestKeysGenerateCommandCreatesPairWithoutWritingKeyMaterial(t *testing.T) {
	root := t.TempDir()
	privatePath := filepath.Join(root, "private.pem")
	publicPath := filepath.Join(root, "public.pem")
	privateBytes, publicBytes := runKeysGenerate(t, privatePath, publicPath)

	if len(privateBytes) == 0 || len(publicBytes) == 0 {
		t.Fatal("keys generate did not create both key files")
	}
}

func TestBootstrapCommandRequiresConfirmationAndWritesConfiguredTopology(t *testing.T) {
	fixture := newProviderFixture(t)
	provider := FileProvider{Path: fixture.configPath}
	commands := append(cli.DefaultCommands(), NewKeysCommand, NewBootstrapCommand(provider))
	var stdout, stderr bytes.Buffer
	code := cli.ExecuteWithDependencies(context.Background(), []string{"--project", fixture.projectRoot, "bootstrap", "--actor", "USR-OWNER"}, &stdout, &stderr, cli.Dependencies{Commands: commands, TransferProvider: provider})
	if code != cli.ExitApproval {
		t.Fatalf("bootstrap without confirm code=%d stderr=%q", code, stderr.String())
	}
	events, err := eventstore.New(fixture.projectRoot).ReadAll(context.Background())
	if err != nil || len(events) != 1 {
		t.Fatalf("unconfirmed bootstrap events=%d err=%v", len(events), err)
	}

	stdout.Reset()
	stderr.Reset()
	code = cli.ExecuteWithDependencies(context.Background(), []string{"--json", "--project", fixture.projectRoot, "bootstrap", "--actor", "USR-OWNER", "--confirm"}, &stdout, &stderr, cli.Dependencies{Commands: commands, TransferProvider: provider})
	if code != cli.ExitOK || !bytes.Contains(stdout.Bytes(), []byte(`"bindings":1`)) {
		t.Fatalf("confirmed bootstrap code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestProtectCommandRequiresConfirmationThenSecuresExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transfer-host.json")
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o666); err != nil {
		t.Fatal(err)
	}
	commands := append(cli.DefaultCommands(), NewProtectCommand)
	var stdout, stderr bytes.Buffer
	code := cli.ExecuteWithDependencies(context.Background(), []string{"protect", path}, &stdout, &stderr, cli.Dependencies{Commands: commands, TransferProvider: FileProvider{}})
	if code != cli.ExitApproval {
		t.Fatalf("protect without confirm code=%d stderr=%q", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = cli.ExecuteWithDependencies(context.Background(), []string{"--json", "protect", path, "--confirm"}, &stdout, &stderr, cli.Dependencies{Commands: commands, TransferProvider: FileProvider{}})
	if code != cli.ExitOK || !bytes.Contains(stdout.Bytes(), []byte(`"protected":1`)) {
		t.Fatalf("protect code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if err := ValidateOwnerOnlyFile(path); err != nil {
		t.Fatalf("protected file = %v", err)
	}
}

func TestPhysicalStatusCommandReportsEvidenceClassification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "evidence.json")
	evidence := completePhysicalEvidence("usb-path-replay")
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	commands := append(cli.DefaultCommands(), NewPhysicalCommand)
	code := cli.ExecuteWithDependencies(context.Background(), []string{"--json", "physical", "status", "--input", path}, &stdout, &stderr, cli.Dependencies{Commands: commands, TransferProvider: FileProvider{}})
	if code != cli.ExitOK || !bytes.Contains(stdout.Bytes(), []byte(`"status":"USB_PATH_REPLAY"`)) {
		t.Fatalf("physical status code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func completePhysicalEvidence(transport string) physicalacceptance.Evidence {
	return physicalacceptance.Evidence{
		SchemaVersion: 1, TransferID: "XFR-PHYSICAL-001", Transport: transport,
		Stages: []physicalacceptance.StageEvidence{
			{Name: "public-export", SourceEnvironmentID: "public", TargetEnvironmentID: "internal", ArtifactSHA256: strings.Repeat("a", 64), Verified: true, Transport: transport},
			{Name: "internal-import", SourceEnvironmentID: "public", TargetEnvironmentID: "internal", ArtifactSHA256: strings.Repeat("a", 64), Verified: true, Transport: transport},
			{Name: "internal-return", SourceEnvironmentID: "internal", TargetEnvironmentID: "public", ArtifactSHA256: strings.Repeat("b", 64), Verified: true, Transport: transport},
			{Name: "public-merge", SourceEnvironmentID: "internal", TargetEnvironmentID: "public", ArtifactSHA256: strings.Repeat("b", 64), Verified: true, Transport: transport},
		},
		Network: physicalacceptance.NetworkEvidence{PublicToInternalBlocked: true, InternalToPublicBlocked: true, PublicCoreHealthy: true, InternalCoreHealthy: true},
	}
}

func TestKeysGenerateCommandRefusesExistingOutput(t *testing.T) {
	root := t.TempDir()
	privatePath := filepath.Join(root, "private.pem")
	publicPath := filepath.Join(root, "public.pem")
	runKeysGenerate(t, privatePath, publicPath)

	var stdout, stderr bytes.Buffer
	code := cli.ExecuteWithDependencies(context.Background(), []string{"keys", "generate", "--private", privatePath, "--public", publicPath}, &stdout, &stderr, cli.Dependencies{Commands: append(cli.DefaultCommands(), NewKeysCommand), TransferProvider: FileProvider{}})
	if code == cli.ExitOK {
		t.Fatal("keys generate overwrote an existing key pair")
	}
}

func runKeysGenerate(t *testing.T, privatePath, publicPath string) ([]byte, []byte) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := cli.ExecuteWithDependencies(context.Background(), []string{"--json", "keys", "generate", "--private", privatePath, "--public", publicPath}, &stdout, &stderr, cli.Dependencies{Commands: append(cli.DefaultCommands(), NewKeysCommand), TransferProvider: FileProvider{}})
	if code != cli.ExitOK {
		t.Fatalf("keys generate code=%d stderr=%q", code, stderr.String())
	}
	privateBytes, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	publicBytes, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stdout.Bytes(), privateBytes) || bytes.Contains(stdout.Bytes(), publicBytes) || bytes.Contains(stderr.Bytes(), privateBytes) || bytes.Contains(stderr.Bytes(), publicBytes) {
		t.Fatal("keys generate wrote key material to command output")
	}
	return privateBytes, publicBytes
}
