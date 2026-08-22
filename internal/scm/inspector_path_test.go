package scm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectorRegistersRepositoryThroughDirectoryAlias(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	aliasRoot := filepath.Join(base, "alias")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, realRoot, "init", "-b", "main")
	createDirectoryAlias(t, realRoot, aliasRoot)

	repository, err := NewInspector().Register(context.Background(), aliasRoot, "PRJ-ALIAS")
	if err != nil {
		t.Fatal(err)
	}
	if repository.ID == "" || repository.ProjectID != "PRJ-ALIAS" {
		t.Fatalf("repository = %#v", repository)
	}
}

func TestInspectorRejectsRepositorySubdirectory(t *testing.T) {
	root := newGitRepository(t)
	subdirectory := filepath.Join(root, "nested")
	if err := os.Mkdir(subdirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewInspector().Register(context.Background(), subdirectory, "PRJ-NESTED"); err == nil {
		t.Fatal("repository subdirectory was accepted as the Git top level")
	}
}
