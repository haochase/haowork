package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/cli"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/testkit"
)

func TestExecuteFailsBeforeServingWhenTrustedConfigIsMissing(t *testing.T) {
	root := t.TempDir()
	_, err := app.InitializeProject(context.Background(), app.InitializeProjectInput{
		Root: root, Name: "trusted-host", ProjectID: "PRJ-TRUSTED", Goal: "signed transfer", CompletionCriteria: []string{"signed archive"},
		Actor: model.Actor{ID: "USR-OWNER", Kind: model.ActorHuman, Role: model.RoleOwner},
	}, &testkit.IDs{}, testkit.Clock{Value: time.Date(2026, time.September, 3, 0, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := execute(context.Background(), []string{"serve", "--project", root}, &stdout, &stderr, "")
	if code == cli.ExitOK || !strings.Contains(stderr.String(), "trusted transfer Core config path is required") {
		t.Fatalf("execute code=%d stderr=%q", code, stderr.String())
	}
	if _, err := filepath.Abs(root); err != nil {
		t.Fatal(err)
	}
}
