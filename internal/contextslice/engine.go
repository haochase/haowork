package contextslice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/haochase/haowork/internal/model"
)

var ErrSourceOutsideProjectRoot = errors.New("source is outside project root")

type Builder struct {
	reader SourceReader
	state  model.ProjectState
	ids    IDGenerator
}

func NewBuilder(reader SourceReader, state model.ProjectState, ids IDGenerator) *Builder {
	return &Builder{
		reader: reader,
		state:  state,
		ids:    ids,
	}
}

func (b *Builder) Build(ctx context.Context, request ContextRequest) (model.ContextSlice, error) {
	if b.reader == nil {
		return model.ContextSlice{}, fmt.Errorf("source is unreadable: reader is not configured")
	}
	if b.ids == nil {
		return model.ContextSlice{}, fmt.Errorf("context slice id generator is not configured")
	}

	sourceRefs, err := normalizePaths(request.Sources)
	if err != nil {
		return model.ContextSlice{}, err
	}
	allowedPaths, err := normalizePaths(request.AllowedPaths)
	if err != nil {
		return model.ContextSlice{}, err
	}
	deniedPaths, err := normalizePaths(request.DeniedPaths)
	if err != nil {
		return model.ContextSlice{}, err
	}
	if err := rejectPathOverlap(allowedPaths, deniedPaths); err != nil {
		return model.ContextSlice{}, err
	}
	expectedDigests, err := normalizeExpectedDigests(request.ExpectedDigests)
	if err != nil {
		return model.ContextSlice{}, err
	}
	if err := validateSourceScope(sourceRefs, allowedPaths, deniedPaths); err != nil {
		return model.ContextSlice{}, err
	}
	if err := validateExpectedDigestSources(sourceRefs, expectedDigests); err != nil {
		return model.ContextSlice{}, err
	}

	sources, err := b.readSources(ctx, sourceRefs, expectedDigests, request.Reason)
	if err != nil {
		return model.ContextSlice{}, err
	}
	revision, err := b.revision(request)
	if err != nil {
		return model.ContextSlice{}, err
	}
	id, err := b.ids.New("ctx")
	if err != nil {
		return model.ContextSlice{}, fmt.Errorf("create context slice id: %w", err)
	}

	slice := model.ContextSlice{
		ID:           id,
		TaskID:       request.TaskID,
		GoalVersion:  b.state.Goal.Version,
		Revision:     revision,
		Summary:      request.Reason,
		Sources:      sources,
		AllowedPaths: allowedPaths,
		DeniedPaths:  deniedPaths,
		SupersedesID: request.SupersedesID,
	}
	slice.SliceHash, err = canonicalHash(canonicalSlice{
		TaskID:       slice.TaskID,
		GoalVersion:  slice.GoalVersion,
		Revision:     slice.Revision,
		Summary:      slice.Summary,
		Sources:      slice.Sources,
		AllowedPaths: slice.AllowedPaths,
		DeniedPaths:  slice.DeniedPaths,
	})
	if err != nil {
		return model.ContextSlice{}, fmt.Errorf("hash context slice: %w", err)
	}

	return slice, nil
}

type canonicalSlice struct {
	TaskID       string                `json:"task_id"`
	GoalVersion  int                   `json:"goal_version"`
	Revision     int                   `json:"revision"`
	Summary      string                `json:"summary"`
	Sources      []model.ContextSource `json:"sources"`
	AllowedPaths []string              `json:"allowed_paths"`
	DeniedPaths  []string              `json:"denied_paths"`
}

func (b *Builder) readSources(ctx context.Context, sourceRefs []string, expectedDigests map[string]string, reason string) ([]model.ContextSource, error) {
	sources := make([]model.ContextSource, 0, len(sourceRefs))
	for _, ref := range sourceRefs {
		contents, err := b.readSource(ctx, ref)
		if err != nil {
			return nil, err
		}
		digest := contentDigest(contents)
		if expected, ok := expectedDigests[ref]; ok && digest != expected {
			return nil, fmt.Errorf("source digest mismatch: %s", ref)
		}
		sources = append(sources, model.ContextSource{
			Kind:   "file",
			Ref:    ref,
			Digest: digest,
			Reason: reason,
		})
	}
	return sources, nil
}

func (b *Builder) readSource(ctx context.Context, ref string) ([]byte, error) {
	opened, err := b.reader.Open(ctx, ref)
	if err != nil {
		if errors.Is(err, ErrSourceOutsideProjectRoot) {
			return nil, err
		}
		return nil, fmt.Errorf("source is unreadable: %s: %w", ref, err)
	}
	contents, readErr := io.ReadAll(opened)
	closeErr := opened.Close()
	if readErr != nil {
		return nil, fmt.Errorf("source is unreadable: %s: %w", ref, readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("source is unreadable: %s: %w", ref, closeErr)
	}
	return contents, nil
}

type FileSourceReader struct {
	root string
}

func NewFileSourceReader(projectRoot string) (*FileSourceReader, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve project root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve project root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect project root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("project root is not a directory: %s", root)
	}
	return &FileSourceReader{root: root}, nil
}

func (r *FileSourceReader) Open(ctx context.Context, ref string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cleaned, err := normalizePath(ref)
	if err != nil {
		return nil, err
	}
	target, err := filepath.Abs(filepath.Join(r.root, filepath.FromSlash(cleaned)))
	if err != nil {
		return nil, fmt.Errorf("resolve source: %w", err)
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, fmt.Errorf("open source: %w", err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("open source: %w", err)
	}
	if err := validateOpenedSource(r.root, resolvedTarget, ref, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func validateOpenedSource(root, resolvedTarget, ref string, file *os.File) error {
	if !isWithinProjectRoot(root, resolvedTarget) {
		return fmt.Errorf("%w: %s", ErrSourceOutsideProjectRoot, ref)
	}
	resolvedInfo, err := os.Stat(resolvedTarget)
	if err != nil {
		return fmt.Errorf("inspect source: %w", err)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened source: %w", err)
	}
	if !os.SameFile(resolvedInfo, fileInfo) {
		return fmt.Errorf("source changed while opening: %s", ref)
	}
	return nil
}

func isWithinProjectRoot(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func validateSourceScope(sourceRefs, allowedPaths, deniedPaths []string) error {
	for _, ref := range sourceRefs {
		for _, deniedPath := range deniedPaths {
			if pathsOverlap(ref, deniedPath) {
				return fmt.Errorf("source is denied by project scope: %s", ref)
			}
		}
		if len(allowedPaths) == 0 {
			continue
		}
		allowed := false
		for _, allowedPath := range allowedPaths {
			if pathWithin(ref, allowedPath) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("source is outside allowed project scope: %s", ref)
		}
	}
	return nil
}

func validateExpectedDigestSources(sourceRefs []string, expectedDigests map[string]string) error {
	selectedSources := make(map[string]struct{}, len(sourceRefs))
	for _, ref := range sourceRefs {
		selectedSources[ref] = struct{}{}
	}
	refs := make([]string, 0, len(expectedDigests))
	for ref := range expectedDigests {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		if _, ok := selectedSources[ref]; !ok {
			return fmt.Errorf("source digest mismatch: expected digest for unselected source %s", ref)
		}
	}
	return nil
}

func (b *Builder) revision(request ContextRequest) (int, error) {
	if request.SupersedesID == "" {
		return 1, nil
	}
	previous, ok := b.state.Contexts[request.SupersedesID]
	if !ok {
		return 0, fmt.Errorf("context slice to supersede is not found: %s", request.SupersedesID)
	}
	if previous.TaskID != request.TaskID {
		return 0, fmt.Errorf("context slice to supersede belongs to a different task: %s", request.SupersedesID)
	}
	return previous.Revision + 1, nil
}

func normalizePaths(paths []string) ([]string, error) {
	normalized := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, rawPath := range paths {
		cleaned, err := normalizePath(rawPath)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		normalized = append(normalized, cleaned)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func normalizeExpectedDigests(digests map[string]string) (map[string]string, error) {
	normalized := make(map[string]string, len(digests))
	for ref, digest := range digests {
		cleaned, err := normalizePath(ref)
		if err != nil {
			return nil, err
		}
		if existing, ok := normalized[cleaned]; ok && existing != digest {
			return nil, fmt.Errorf("source digest mismatch: conflicting expected digest for %s", cleaned)
		}
		normalized[cleaned] = digest
	}
	return normalized, nil
}

func normalizePath(rawPath string) (string, error) {
	if rawPath == "" {
		return "", fmt.Errorf("path is outside project scope: empty path")
	}
	normalized := strings.ReplaceAll(rawPath, "\\", "/")
	if path.IsAbs(normalized) || hasWindowsVolume(normalized) {
		return "", fmt.Errorf("path is outside project scope: %s", rawPath)
	}
	cleaned := path.Clean(normalized)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path is outside project scope: %s", rawPath)
	}
	return cleaned, nil
}

func hasWindowsVolume(value string) bool {
	return len(value) >= 2 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':'
}

func rejectPathOverlap(allowedPaths, deniedPaths []string) error {
	for _, allowed := range allowedPaths {
		for _, denied := range deniedPaths {
			if pathsOverlap(allowed, denied) {
				return fmt.Errorf("allowed path overlaps denied path: %s and %s", allowed, denied)
			}
		}
	}
	return nil
}

func pathsOverlap(first, second string) bool {
	return first == second || strings.HasPrefix(first, second+"/") || strings.HasPrefix(second, first+"/")
}

func pathWithin(candidate, scope string) bool {
	return candidate == scope || strings.HasPrefix(candidate, scope+"/")
}

func contentDigest(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func canonicalHash(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
