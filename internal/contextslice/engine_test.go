package contextslice

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/haochase/haowork/internal/model"
)

func TestBuilderSameInputProducesSameSliceHash(t *testing.T) {
	t.Parallel()

	reader := stubReader{files: map[string][]byte{
		"docs/guide.md":      []byte("guide"),
		"internal/main.go":   []byte("package internal"),
		"internal/notes.txt": []byte("notes"),
	}}
	builder := NewBuilder(reader, model.ProjectState{
		Goal: model.GoalVersion{Version: 3},
	}, sequenceIDs{})
	request := ContextRequest{
		TaskID:       "task-1",
		Reason:       "implement task",
		Sources:      []string{"internal\\main.go", "docs/guide.md", "internal/main.go"},
		AllowedPaths: []string{"internal", "docs"},
		DeniedPaths:  []string{".env", "tmp"},
		Actor:        model.Actor{ID: "agent-1", Kind: model.ActorAgent, Role: model.RoleAgent},
	}

	first, err := builder.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build() first error = %v", err)
	}
	second, err := builder.Build(context.Background(), request)
	if err != nil {
		t.Fatalf("Build() second error = %v", err)
	}

	if first.SliceHash != second.SliceHash {
		t.Fatalf("SliceHash differs for same input: %q != %q", first.SliceHash, second.SliceHash)
	}
	wantSources := []model.ContextSource{
		{Kind: "file", Ref: "docs/guide.md", Digest: digest([]byte("guide")), Reason: "implement task"},
		{Kind: "file", Ref: "internal/main.go", Digest: digest([]byte("package internal")), Reason: "implement task"},
	}
	if !reflect.DeepEqual(first.Sources, wantSources) {
		t.Fatalf("Sources = %#v, want %#v", first.Sources, wantSources)
	}
	if !reflect.DeepEqual(first.AllowedPaths, []string{"docs", "internal"}) {
		t.Fatalf("AllowedPaths = %#v", first.AllowedPaths)
	}
	if !reflect.DeepEqual(first.DeniedPaths, []string{".env", "tmp"}) {
		t.Fatalf("DeniedPaths = %#v", first.DeniedPaths)
	}
}

func TestBuilderRejectsUnreadableOrChangedSource(t *testing.T) {
	t.Parallel()

	state := model.ProjectState{Goal: model.GoalVersion{Version: 1}}
	tests := []struct {
		name    string
		reader  stubReader
		request ContextRequest
		want    string
	}{
		{
			name:   "unreadable source",
			reader: stubReader{err: errors.New("disk unavailable")},
			request: ContextRequest{
				TaskID: "task-1", Sources: []string{"docs/missing.md"},
			},
			want: "source is unreadable",
		},
		{
			name:   "changed source",
			reader: stubReader{files: map[string][]byte{"docs/guide.md": []byte("actual")}},
			request: ContextRequest{
				TaskID: "task-1", Sources: []string{"docs/guide.md"}, ExpectedDigests: map[string]string{
					"docs/guide.md": digest([]byte("expected")),
				},
			},
			want: "source digest mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := NewBuilder(tt.reader, state, sequenceIDs{})
			_, err := builder.Build(context.Background(), tt.request)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Build() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestBuilderRejectsPathOutsideProjectAndDeniedOverlap(t *testing.T) {
	t.Parallel()

	builder := NewBuilder(stubReader{}, model.ProjectState{Goal: model.GoalVersion{Version: 1}}, sequenceIDs{})
	tests := []struct {
		name    string
		request ContextRequest
		want    string
	}{
		{
			name:    "parent directory source",
			request: ContextRequest{TaskID: "task-1", Sources: []string{"../outside.md"}},
			want:    "path is outside project scope",
		},
		{
			name:    "absolute source",
			request: ContextRequest{TaskID: "task-1", Sources: []string{"C:\\outside.md"}},
			want:    "path is outside project scope",
		},
		{
			name:    "allowed and denied overlap",
			request: ContextRequest{TaskID: "task-1", AllowedPaths: []string{"internal"}, DeniedPaths: []string{"internal"}},
			want:    "allowed path overlaps denied path",
		},
		{
			name:    "allowed and denied descendant overlap",
			request: ContextRequest{TaskID: "task-1", AllowedPaths: []string{"docs"}, DeniedPaths: []string{"docs/private"}},
			want:    "allowed path overlaps denied path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := builder.Build(context.Background(), tt.request)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Build() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestBuilderSupersedesWithoutMutatingPreviousSlice(t *testing.T) {
	t.Parallel()

	previous := model.ContextSlice{ID: "ctx-previous", TaskID: "task-1", GoalVersion: 4, Revision: 2, SliceHash: "previous-hash"}
	state := model.ProjectState{
		Goal:     model.GoalVersion{Version: 4},
		Contexts: map[string]model.ContextSlice{previous.ID: previous},
	}
	builder := NewBuilder(stubReader{files: map[string][]byte{"docs/guide.md": []byte("guide")}}, state, sequenceIDs{})

	slice, err := builder.Build(context.Background(), ContextRequest{
		TaskID: "task-1", SupersedesID: previous.ID, Reason: "revised scope", Sources: []string{"docs/guide.md"},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if slice.Revision != previous.Revision+1 {
		t.Fatalf("Revision = %d, want %d", slice.Revision, previous.Revision+1)
	}
	if slice.SupersedesID != previous.ID {
		t.Fatalf("SupersedesID = %q, want %q", slice.SupersedesID, previous.ID)
	}
	if got := state.Contexts[previous.ID]; !reflect.DeepEqual(got, previous) {
		t.Fatalf("previous context mutated: %#v, want %#v", got, previous)
	}
}

func TestBuilderEnforcesSourceScopeAndRootValidationBeforeOpen(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		request   ContextRequest
		openError error
		want      string
		wantOpens int
	}{
		{
			name:      "source is within denied descendant",
			request:   ContextRequest{TaskID: "task-1", Sources: []string{"docs/private/secret.md"}, DeniedPaths: []string{"docs/private"}},
			want:      "source is denied by project scope",
			wantOpens: 0,
		},
		{
			name:      "source overlaps denied descendant",
			request:   ContextRequest{TaskID: "task-1", Sources: []string{"docs"}, DeniedPaths: []string{"docs/private"}},
			want:      "source is denied by project scope",
			wantOpens: 0,
		},
		{
			name:      "source is outside allowed scope",
			request:   ContextRequest{TaskID: "task-1", Sources: []string{"internal/main.go"}, AllowedPaths: []string{"docs"}},
			want:      "source is outside allowed project scope",
			wantOpens: 0,
		},
		{
			name:      "root bound open rejects symlink escape",
			request:   ContextRequest{TaskID: "task-1", Sources: []string{"docs/linked.md"}},
			openError: fmt.Errorf("%w: resolved path escapes root through symlink", ErrSourceOutsideProjectRoot),
			want:      "source is outside project root",
			wantOpens: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := &recordingReader{
				files:     map[string][]byte{"docs/private/secret.md": []byte("secret"), "docs": []byte("directory"), "internal/main.go": []byte("main"), "docs/linked.md": []byte("linked")},
				openError: tt.openError,
			}
			builder := NewBuilder(reader, model.ProjectState{Goal: model.GoalVersion{Version: 1}}, sequenceIDs{})

			_, err := builder.Build(context.Background(), tt.request)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Build() error = %v, want substring %q", err, tt.want)
			}
			if reader.totalOpens() != tt.wantOpens {
				t.Fatalf("Open() calls = %d, want %d", reader.totalOpens(), tt.wantOpens)
			}
			if tt.openError != nil && reader.openCalls["docs/linked.md"] != 1 {
				t.Fatalf("Open(\"docs/linked.md\") calls = %d, want 1", reader.openCalls["docs/linked.md"])
			}
		})
	}
}

func TestBuilderRejectsUnselectedExpectedDigest(t *testing.T) {
	t.Parallel()

	reader := &recordingReader{files: map[string][]byte{"docs/guide.md": []byte("guide")}}
	builder := NewBuilder(reader, model.ProjectState{Goal: model.GoalVersion{Version: 1}}, sequenceIDs{})

	_, err := builder.Build(context.Background(), ContextRequest{
		TaskID:  "task-1",
		Sources: []string{"docs/guide.md"},
		ExpectedDigests: map[string]string{
			"docs/guide.md":  digest([]byte("guide")),
			"docs/unread.md": digest([]byte("unread")),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "source digest mismatch") {
		t.Fatalf("Build() error = %v, want source digest mismatch", err)
	}
	if reader.totalOpens() != 0 {
		t.Fatalf("Open() calls = %d, want 0", reader.totalOpens())
	}
}

func TestBuilderReadsEachNormalizedSourceOnce(t *testing.T) {
	t.Parallel()

	reader := &recordingReader{files: map[string][]byte{
		"docs/guide.md":    []byte("guide"),
		"internal/main.go": []byte("main"),
	}}
	builder := NewBuilder(reader, model.ProjectState{Goal: model.GoalVersion{Version: 1}}, sequenceIDs{})

	_, err := builder.Build(context.Background(), ContextRequest{
		TaskID:  "task-1",
		Sources: []string{"internal/main.go", "docs\\guide.md", "docs/guide.md"},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, ref := range []string{"docs/guide.md", "internal/main.go"} {
		if reader.openCalls[ref] != 1 {
			t.Fatalf("Open(%q) calls = %d, want 1", ref, reader.openCalls[ref])
		}
	}
}

func TestFileSourceReaderRejectsSymlinkOutsideProjectRoot(t *testing.T) {
	root := t.TempDir()
	insidePath := filepath.Join(root, "docs", "inside.md")
	if err := os.MkdirAll(filepath.Dir(insidePath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(insidePath, []byte("inside"), 0o600); err != nil {
		t.Fatalf("WriteFile(inside) error = %v", err)
	}

	reader, err := NewFileSourceReader(root)
	if err != nil {
		t.Fatalf("NewFileSourceReader() error = %v", err)
	}
	opened, err := reader.Open(context.Background(), "docs/inside.md")
	if err != nil {
		t.Fatalf("Open(inside) error = %v", err)
	}
	contents, readErr := io.ReadAll(opened)
	closeErr := opened.Close()
	if readErr != nil {
		t.Fatalf("ReadAll(inside) error = %v", readErr)
	}
	if closeErr != nil {
		t.Fatalf("Close(inside) error = %v", closeErr)
	}
	if string(contents) != "inside" {
		t.Fatalf("inside contents = %q, want %q", contents, "inside")
	}

	outsidePath := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatalf("WriteFile(outside) error = %v", err)
	}
	linkPath := filepath.Join(root, "docs", "outside.md")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Skipf("symlinks are unavailable on this platform: %v", err)
	}

	opened, err = reader.Open(context.Background(), "docs/outside.md")
	if opened != nil {
		_ = opened.Close()
		t.Fatal("Open(outside symlink) returned a handle")
	}
	if err == nil || !strings.Contains(err.Error(), "source is outside project root") {
		t.Fatalf("Open(outside symlink) error = %v, want source is outside project root", err)
	}
}

func TestFileSourceReaderRejectsOpenedFileIdentityMismatch(t *testing.T) {
	root := t.TempDir()
	insidePath := filepath.Join(root, "docs", "inside.md")
	otherPath := filepath.Join(root, "docs", "other.md")
	if err := os.MkdirAll(filepath.Dir(insidePath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(insidePath, []byte("inside"), 0o600); err != nil {
		t.Fatalf("WriteFile(inside) error = %v", err)
	}
	if err := os.WriteFile(otherPath, []byte("other"), 0o600); err != nil {
		t.Fatalf("WriteFile(other) error = %v", err)
	}
	opened, err := os.Open(otherPath)
	if err != nil {
		t.Fatalf("Open(other) error = %v", err)
	}
	defer opened.Close()

	err = validateOpenedSource(root, insidePath, "docs/inside.md", opened)
	if err == nil || !strings.Contains(err.Error(), "source changed while opening") {
		t.Fatalf("validateOpenedSource() error = %v, want identity mismatch", err)
	}
}

type stubReader struct {
	files map[string][]byte
	err   error
}

func (r stubReader) Open(_ context.Context, ref string) (io.ReadCloser, error) {
	if r.err != nil {
		return nil, r.err
	}
	contents, ok := r.files[ref]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(contents)), nil
}

type recordingReader struct {
	files     map[string][]byte
	openError error
	openCalls map[string]int
}

func (r *recordingReader) Open(_ context.Context, ref string) (io.ReadCloser, error) {
	if r.openCalls == nil {
		r.openCalls = make(map[string]int)
	}
	r.openCalls[ref]++
	if r.openError != nil {
		return nil, r.openError
	}
	contents, ok := r.files[ref]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(contents)), nil
}

func (r *recordingReader) totalOpens() int {
	total := 0
	for _, count := range r.openCalls {
		total += count
	}
	return total
}

type sequenceIDs struct{}

func (sequenceIDs) New(prefix string) (string, error) {
	return prefix + "-new", nil
}

func digest(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}
