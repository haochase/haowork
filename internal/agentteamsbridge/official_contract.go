package agentteamsbridge

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

const (
	OfficialRepository               = "https://github.com/agentscope-ai/AgentTeams"
	OfficialTag                      = "v1.2.2"
	OfficialCommit                   = "849182af8e017168a5a200a87b1062142caf462d"
	OfficialChartPath                = "helm/agentteams"
	OfficialChartVersion             = "1.1.1"
	OfficialAPIGroup                 = "agentteams.io"
	OfficialAPIVersion               = "v1beta1"
	OfficialControllerOwnershipLabel = "agentteams.io/controller"
	ImageResolutionBlocked           = "BLOCKED_IMAGE_DIGEST"
	ImageResolutionResolved          = "RESOLVED"
	ImageResolutionUnavailable       = "UNAVAILABLE_UPSTREAM"
	ImageRequirementActive           = "ACTIVE"
	ImageRequirementOptional         = "OPTIONAL"
	maxOfficialContractBytes         = 1 << 20
)

var (
	ErrInvalidOfficialContract = errors.New("invalid AgentTeams official contract")
)

// OfficialContract pins the exact upstream facts that Haowork may use when
// deploying or talking to AgentTeams. It deliberately contains no credentials.
type OfficialContract struct {
	SchemaVersion            int               `json:"schema_version"`
	Repository               string            `json:"repository"`
	Tag                      string            `json:"tag"`
	Commit                   string            `json:"commit"`
	ChartPath                string            `json:"chart_path"`
	ChartVersion             string            `json:"chart_version"`
	ChartAppVersion          string            `json:"chart_app_version"`
	UpstreamManifest         UpstreamManifest  `json:"upstream_manifest"`
	ChartDependencies        []ChartDependency `json:"chart_dependencies"`
	APIGroup                 string            `json:"api_group"`
	APIVersion               string            `json:"api_version"`
	Kinds                    []string          `json:"kinds"`
	ControllerOwnershipLabel string            `json:"controller_ownership_label"`
	ImageResolution          ImageResolution   `json:"image_resolution"`
}

type ChartDependency struct {
	Name       string `json:"name"`
	Repository string `json:"repository"`
	Version    string `json:"version"`
	LockDigest string `json:"lock_digest"`
}

type UpstreamManifest struct {
	Algorithm string         `json:"algorithm"`
	Files     []ManifestFile `json:"files"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type ImageResolution struct {
	Status            string            `json:"status"`
	Reason            string            `json:"reason"`
	DeploymentProfile DeploymentProfile `json:"deployment_profile"`
	RenderedInventory RenderedInventory `json:"rendered_inventory"`
	Images            []ImageLock       `json:"images"`
}

type DeploymentProfile struct {
	ManagerRuntime string `json:"manager_runtime"`
	WorkerRuntime  string `json:"worker_runtime"`
}

type RenderedInventory struct {
	Status         string      `json:"status"`
	Reason         string      `json:"reason"`
	ManifestSHA256 string      `json:"manifest_sha256"`
	Images         []ImageLock `json:"images"`
}

type ImageLock struct {
	Name             string `json:"name"`
	Repository       string `json:"repository"`
	Tag              string `json:"tag"`
	ResolvedDigest   string `json:"resolved_digest"`
	ResolutionStatus string `json:"resolution_status"`
	Requirement      string `json:"requirement"`
	Reason           string `json:"reason"`
	Source           string `json:"source"`
}

// LoadOfficialContract reads a small, strict JSON lock file without fetching
// from the network or mutating the pinned upstream snapshot.
func LoadOfficialContract(path string) (OfficialContract, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return OfficialContract{}, err
	}
	if len(encoded) > maxOfficialContractBytes {
		return OfficialContract{}, fmt.Errorf("%w: lock file exceeds %d bytes", ErrInvalidOfficialContract, maxOfficialContractBytes)
	}
	return ParseOfficialContract(encoded)
}

// ParseOfficialContract decodes a single JSON value and rejects drift from the
// official v1.2.2 source, chart, CRD, and image-resolution contract.
func ParseOfficialContract(encoded []byte) (OfficialContract, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var contract OfficialContract
	if err := decoder.Decode(&contract); err != nil {
		return OfficialContract{}, fmt.Errorf("%w: decode lock file: %v", ErrInvalidOfficialContract, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return OfficialContract{}, fmt.Errorf("%w: lock file contains multiple JSON values", ErrInvalidOfficialContract)
		}
		return OfficialContract{}, fmt.Errorf("%w: trailing lock file content: %v", ErrInvalidOfficialContract, err)
	}
	if err := contract.Validate(); err != nil {
		return OfficialContract{}, err
	}
	return contract, nil
}

func (contract OfficialContract) Validate() error {
	if contract.SchemaVersion != 2 {
		return invalidOfficialContract("schema_version must be 2")
	}
	if contract.Repository != OfficialRepository || contract.Tag != OfficialTag || contract.Commit != OfficialCommit {
		return invalidOfficialContract("repository, tag, or commit does not match AgentTeams v1.2.2")
	}
	if contract.ChartPath != OfficialChartPath || contract.ChartVersion != OfficialChartVersion || contract.ChartAppVersion != OfficialChartVersion {
		return invalidOfficialContract("chart metadata does not match the pinned upstream source")
	}
	if err := validateUpstreamManifest(contract.UpstreamManifest); err != nil {
		return err
	}
	if err := validateChartDependencies(contract.ChartDependencies); err != nil {
		return err
	}
	if contract.APIGroup != OfficialAPIGroup || contract.APIVersion != OfficialAPIVersion {
		return invalidOfficialContract("API group/version must be agentteams.io/v1beta1")
	}
	if contract.ControllerOwnershipLabel != OfficialControllerOwnershipLabel {
		return invalidOfficialContract("controller ownership label does not match upstream")
	}
	if err := validateKinds(contract.Kinds); err != nil {
		return err
	}
	return validateImageResolution(contract.ImageResolution)
}

func validateUpstreamManifest(manifest UpstreamManifest) error {
	expected := map[string]string{
		"helm/agentteams/Chart.yaml":                       "5c7b1b8d0968db7b452049e27e012b9668b38143b4236dea6b139e8f0467a18e",
		"helm/agentteams/Chart.lock":                       "f4ada56a4107df94d1a3175f683490c4f143c8381a66a81619aa33d42a46aa43",
		"helm/agentteams/values.yaml":                      "83da031e460c3ec102ad99baf5f19b447e9b19a11ab17598b309ced5ff066e97",
		"helm/agentteams/crds/managers.agentteams.io.yaml": "2c279e6c4203b320ffa73fb8f88a7639e5a1e8dd9a00c848579963154f7ea10a",
		"helm/agentteams/crds/workers.agentteams.io.yaml":  "3864240f99e7fa2f15e33c6886a1012fb736c871cd021e87ed2ae499234a1286",
		"helm/agentteams/crds/teams.agentteams.io.yaml":    "bd75a92d6187d0283061d3291cf102365cf86fb2b01f8a8ddc8f4b5530fc7342",
		"helm/agentteams/crds/humans.agentteams.io.yaml":   "4637e64377856574fa4fe9bb76567fb7a926cc2bd0b1f1504afd1f71261bb897",
	}
	if manifest.Algorithm != "sha256" || len(manifest.Files) != len(expected) {
		return invalidOfficialContract("upstream manifest is incomplete")
	}
	seen := make(map[string]struct{}, len(manifest.Files))
	for _, file := range manifest.Files {
		if _, duplicated := seen[file.Path]; duplicated || expected[file.Path] != file.SHA256 || !isLowerSHA256(file.SHA256) {
			return invalidOfficialContract("upstream manifest does not match pinned Chart or CRD files")
		}
		seen[file.Path] = struct{}{}
	}
	return nil
}

func isLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func (contract OfficialContract) HasKind(kind string) bool {
	for _, candidate := range contract.Kinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

func (contract OfficialContract) DeploymentReady() bool {
	profile := contract.ImageResolution.DeploymentProfile
	return contract.ValidateDeploymentProfile(profile.ManagerRuntime, profile.WorkerRuntime) == nil
}

func (contract OfficialContract) SupportsWorkerRuntime(runtime string) bool {
	name := "worker-" + strings.TrimSpace(runtime)
	for _, image := range contract.ImageResolution.Images {
		if image.Name == name {
			return image.ResolutionStatus == ImageResolutionResolved && validDigest(image.ResolvedDigest)
		}
	}
	return false
}

func (contract OfficialContract) ValidateDeploymentProfile(managerRuntime, workerRuntime string) error {
	if contract.ImageResolution.Status != ImageResolutionResolved || contract.ImageResolution.RenderedInventory.Status != ImageResolutionResolved {
		return invalidOfficialContract("active deployment image inventory is not resolved")
	}
	if strings.TrimSpace(managerRuntime) != "openclaw" {
		return invalidOfficialContract("manager runtime is not locked for this deployment")
	}
	for _, name := range []string{"controller", "element-web", "manager", "matrix-tuwunel", "storage-minio"} {
		if !resolvedImage(contract.ImageResolution.Images, name) {
			return invalidOfficialContract("required image " + name + " is not resolved")
		}
	}
	workerImage := "worker-" + strings.TrimSpace(workerRuntime)
	if !resolvedImage(contract.ImageResolution.Images, workerImage) {
		return invalidOfficialContract("required image " + workerImage + " is not resolved")
	}
	return nil
}

func validateChartDependencies(dependencies []ChartDependency) error {
	if len(dependencies) != 1 {
		return invalidOfficialContract("exactly one chart dependency is required")
	}
	dependency := dependencies[0]
	if dependency.Name != "higress" || dependency.Repository != "https://higress.io/helm-charts" || dependency.Version != "2.2.1" || dependency.LockDigest != "sha256:bfda3317506f04c1088d398ca7b10137409999ec54e1d36b7b5d525145ee931b" {
		return invalidOfficialContract("Higress chart dependency does not match the pinned upstream Chart.lock")
	}
	return nil
}

func validateKinds(kinds []string) error {
	required := []string{"Human", "Manager", "Team", "Worker"}
	if len(kinds) != len(required) {
		return invalidOfficialContract("Manager, Worker, Team, and Human are all required")
	}
	actual := append([]string(nil), kinds...)
	sort.Strings(actual)
	for index, kind := range required {
		if actual[index] != kind {
			return invalidOfficialContract("Manager, Worker, Team, and Human are all required")
		}
	}
	return nil
}

func validateImageResolution(resolution ImageResolution) error {
	if resolution.Status != ImageResolutionResolved {
		return invalidOfficialContract("image resolution status is invalid")
	}
	if len(resolution.Images) == 0 {
		return invalidOfficialContract("at least one direct chart image is required")
	}
	if strings.TrimSpace(resolution.Reason) != "" {
		return invalidOfficialContract("resolved image inventory must not contain a blocking reason")
	}
	if resolution.DeploymentProfile.ManagerRuntime != "openclaw" || resolution.DeploymentProfile.WorkerRuntime != "openclaw" {
		return invalidOfficialContract("deployment profile must select the audited OpenClaw runtimes")
	}
	if err := validateRenderedInventory(resolution.RenderedInventory); err != nil {
		return err
	}
	expectedImages := map[string]ImageLock{
		"matrix-tuwunel": {
			Repository:       "higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/tuwunel",
			Tag:              "20260216",
			ResolvedDigest:   "sha256:fa0f68cf591c90b12888c2df76c2ce03fb50a7cd4a9c7fe0199480b291932c00",
			ResolutionStatus: ImageResolutionResolved,
			Requirement:      ImageRequirementActive,
		},
		"storage-minio": {
			Repository:       "higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/minio",
			Tag:              "20260216",
			ResolvedDigest:   "sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e",
			ResolutionStatus: ImageResolutionResolved,
			Requirement:      ImageRequirementActive,
		},
		"controller": {
			Repository:       "higress-registry.cn-hangzhou.cr.aliyuncs.com/agentteams/agentteams-controller",
			Tag:              "v1.2.2",
			ResolvedDigest:   "sha256:a0709506e6dd047bc6aadcfd8d77c8f193683d4326795c263f32b7be9e791570",
			ResolutionStatus: ImageResolutionResolved,
			Requirement:      ImageRequirementActive,
		},
		"manager": {
			Repository:       "higress-registry.cn-hangzhou.cr.aliyuncs.com/agentteams/agentteams-manager",
			Tag:              "v1.2.2",
			ResolvedDigest:   "sha256:dd11878943e4a425ff38dcc152c9d44ea0e68d97bac89f711207134b8636c0fb",
			ResolutionStatus: ImageResolutionResolved,
			Requirement:      ImageRequirementActive,
		},
		"element-web": {
			Repository:       "higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/element-web",
			Tag:              "20260216",
			ResolvedDigest:   "sha256:827ae9ebea5ec0eeb487660f4f04e5789b666667f17a0d63b5c0e4ad8b9b9ca1",
			ResolutionStatus: ImageResolutionResolved,
			Requirement:      ImageRequirementActive,
		},
		"worker-openclaw": {
			Repository:       "higress-registry.cn-hangzhou.cr.aliyuncs.com/agentteams/agentteams-worker",
			Tag:              "v1.2.2",
			ResolvedDigest:   "sha256:301f9e311654eca203246fa666d63a126244ea8793f700603d2a6d37b7ffea75",
			ResolutionStatus: ImageResolutionResolved,
			Requirement:      ImageRequirementActive,
		},
		"worker-copaw": {
			Repository:       "higress-registry.cn-hangzhou.cr.aliyuncs.com/agentteams/agentteams-copaw-worker",
			Tag:              "v1.2.2",
			ResolvedDigest:   "sha256:7a6780ef76b6c7b056a2c343eeabc697f70108dae153afe8ddb76a3fad9a41b4",
			ResolutionStatus: ImageResolutionResolved,
			Requirement:      ImageRequirementOptional,
		},
		"worker-hermes": {
			Repository:       "higress-registry.cn-hangzhou.cr.aliyuncs.com/agentteams/agentteams-hermes-worker",
			Tag:              "v1.2.2",
			ResolvedDigest:   "sha256:e611f38e1aa2451c97b979ae944a787f0db69c9d65c21c72a05ab33b53288e4e",
			ResolutionStatus: ImageResolutionResolved,
			Requirement:      ImageRequirementOptional,
		},
		"worker-openhuman": {
			Repository:       "higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/agentteams-openhuman-worker",
			Tag:              "v1.2.2",
			ResolutionStatus: ImageResolutionUnavailable,
			Requirement:      ImageRequirementOptional,
		},
	}
	seen := make(map[string]struct{}, len(resolution.Images))
	for _, image := range resolution.Images {
		if strings.TrimSpace(image.Name) == "" || strings.TrimSpace(image.Repository) == "" || strings.TrimSpace(image.Tag) == "" || strings.TrimSpace(image.Source) == "" {
			return invalidOfficialContract("image name, repository, tag, and source are required")
		}
		if strings.Contains(strings.ToLower(image.Repository), "latest") || strings.Contains(strings.ToLower(image.Tag), "latest") || strings.Contains(strings.ToUpper(image.Repository), "REPLACE_WITH") || strings.Contains(strings.ToUpper(image.Tag), "REPLACE_WITH") {
			return invalidOfficialContract("image references must not use latest or placeholders")
		}
		if _, ok := seen[image.Name]; ok {
			return invalidOfficialContract("image names must be unique")
		}
		expected, ok := expectedImages[image.Name]
		if !ok || image.Repository != expected.Repository || image.Tag != expected.Tag || image.ResolvedDigest != expected.ResolvedDigest || image.ResolutionStatus != expected.ResolutionStatus || image.Requirement != expected.Requirement {
			return invalidOfficialContract("direct chart image repository or tag does not match the pinned upstream source")
		}
		seen[image.Name] = struct{}{}
		if image.ResolutionStatus == ImageResolutionResolved {
			if !validDigest(image.ResolvedDigest) || strings.TrimSpace(image.Reason) != "" {
				return invalidOfficialContract("resolved image requires an immutable digest and no blocking reason")
			}
		} else if image.ResolutionStatus == ImageResolutionUnavailable {
			if image.Requirement != ImageRequirementOptional || image.ResolvedDigest != "" || strings.TrimSpace(image.Reason) == "" {
				return invalidOfficialContract("unavailable image must be optional and explain the upstream gap")
			}
		}
	}
	if len(seen) != len(expectedImages) {
		return invalidOfficialContract("direct chart image set does not match the pinned upstream source")
	}
	return nil
}

func validateRenderedInventory(inventory RenderedInventory) error {
	if inventory.Status != ImageResolutionResolved || strings.TrimSpace(inventory.Reason) != "" || !isLowerSHA256(inventory.ManifestSHA256) {
		return invalidOfficialContract("rendered image inventory is not resolved")
	}
	expected := map[string]ImageLock{
		"controller":         {Repository: "higress-registry.cn-hangzhou.cr.aliyuncs.com/agentteams/agentteams-controller", Tag: "v1.2.2", ResolvedDigest: "sha256:a0709506e6dd047bc6aadcfd8d77c8f193683d4326795c263f32b7be9e791570"},
		"element-web":        {Repository: "higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/element-web", Tag: "20260216", ResolvedDigest: "sha256:827ae9ebea5ec0eeb487660f4f04e5789b666667f17a0d63b5c0e4ad8b9b9ca1"},
		"higress-console":    {Repository: "higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/console", Tag: "2.2.1", ResolvedDigest: "sha256:90ccdbb078375aad42f874feddba9d964eca34f192ee8dbab7d9a22079b580a4"},
		"higress-controller": {Repository: "higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/higress", Tag: "2.2.1", ResolvedDigest: "sha256:7d62ee8dbc5d45dd8659e8a1f507bdc2183d375ac40695b8a5a2238b3051081f"},
		"higress-gateway":    {Repository: "higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/gateway", Tag: "2.2.1", ResolvedDigest: "sha256:5fe4760c83ccfd8a126fcbbf04af20dd7f33e7e0edc2866b11972ee873e5fd12"},
		"higress-pilot":      {Repository: "higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/pilot", Tag: "2.2.1", ResolvedDigest: "sha256:fcd65ced98be1c39a3c18834e60c54015f71e99f47801678538d43c3f25657d1"},
		"matrix-tuwunel":     {Repository: "higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/tuwunel", Tag: "20260216", ResolvedDigest: "sha256:fa0f68cf591c90b12888c2df76c2ce03fb50a7cd4a9c7fe0199480b291932c00"},
		"storage-minio":      {Repository: "higress-registry.cn-hangzhou.cr.aliyuncs.com/higress/minio", Tag: "20260216", ResolvedDigest: "sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e"},
	}
	if len(inventory.Images) != len(expected) {
		return invalidOfficialContract("rendered image inventory set is incomplete")
	}
	seen := make(map[string]struct{}, len(inventory.Images))
	canonical := make([]string, 0, len(inventory.Images))
	for _, image := range inventory.Images {
		want, ok := expected[image.Name]
		if !ok || image.Repository != want.Repository || image.Tag != want.Tag || image.ResolvedDigest != want.ResolvedDigest || image.ResolutionStatus != ImageResolutionResolved || image.Requirement != ImageRequirementActive || strings.TrimSpace(image.Source) == "" || strings.TrimSpace(image.Reason) != "" {
			return invalidOfficialContract("rendered image inventory does not match the audited deployment")
		}
		if _, duplicate := seen[image.Name]; duplicate {
			return invalidOfficialContract("rendered image inventory names must be unique")
		}
		seen[image.Name] = struct{}{}
		canonical = append(canonical, image.Name+"|"+image.Repository+"|"+image.Tag+"|"+image.ResolvedDigest)
	}
	sort.Strings(canonical)
	digest := sha256.Sum256([]byte(strings.Join(canonical, "\n") + "\n"))
	if fmt.Sprintf("%x", digest) != inventory.ManifestSHA256 {
		return invalidOfficialContract("rendered image inventory manifest hash does not match")
	}
	return nil
}

func resolvedImage(images []ImageLock, name string) bool {
	for _, image := range images {
		if image.Name == name {
			return image.ResolutionStatus == ImageResolutionResolved && validDigest(image.ResolvedDigest)
		}
	}
	return false
}

func validDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && isLowerSHA256(strings.TrimPrefix(value, "sha256:"))
}

func invalidOfficialContract(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidOfficialContract, message)
}
