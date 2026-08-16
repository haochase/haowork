package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"testing"

	"github.com/haochase/haowork/internal/app"
	"github.com/haochase/haowork/internal/eventstore"
	"github.com/haochase/haowork/internal/team"
	"github.com/haochase/haowork/internal/teamapi"
	"github.com/haochase/haowork/internal/teamsync"
)

func TestMapErrorClassifiesFilesystemFailuresAsOperational(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "link", err: &os.LinkError{Op: "rename", Old: "old", New: "new", Err: errors.New("disk failure")}},
		{name: "syscall", err: &os.SyscallError{Syscall: "write", Err: errors.New("disk failure")}},
		{name: "bare errno", err: syscall.ENOSPC},
		{name: "unexpected end of runtime input", err: io.EOF},
		{name: "application operational sentinel", err: fmt.Errorf("read state: %w", app.ErrOperational)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapped := mapError(test.err)
			var coded *CodedError
			if !errors.As(mapped, &coded) || coded.Code != ExitFailure {
				t.Fatalf("mapError() = %T %v, want ExitFailure", mapped, mapped)
			}
		})
	}
}

func TestMapErrorClassifiesTeamOutcomes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"offline", teamsync.ErrOffline, ExitOffline},
		{"not writable", team.ErrNotWritable, ExitOffline},
		{"stale", teamsync.ErrStaleCursor, ExitConflict},
		{"conflict", &teamapi.ConflictError{}, ExitConflict},
		{"team unauthenticated", &teamapi.APIError{StatusCode: 401}, ExitOffline},
		{"team unavailable", &teamapi.APIError{StatusCode: 503}, ExitOffline},
		{"team approval", &teamapi.APIError{StatusCode: 403}, ExitApproval},
		{"team conflict response", &teamapi.APIError{StatusCode: 409}, ExitConflict},
		{"team gate", &teamapi.APIError{StatusCode: 422}, ExitGate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var coded *CodedError
			if mapped := mapError(test.err); !errors.As(mapped, &coded) || coded.Code != test.want {
				t.Fatalf("mapError(%v) = %v, want exit %d", test.err, mapped, test.want)
			}
		})
	}
}

func TestMapErrorPreservesUnderlyingEventStoreText(t *testing.T) {
	tests := []struct {
		name   string
		source error
	}{
		{
			name:   "missing event log",
			source: &os.PathError{Op: "open", Path: "events.jsonl", Err: os.ErrNotExist},
		},
		{
			name:   "corrupt event log",
			source: fmt.Errorf("%w: decode sequence 1: unexpected EOF", eventstore.ErrHistoryCorrupt),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapped := mapError(fmt.Errorf("%w: %w", app.ErrOperational, test.source))

			var coded *CodedError
			if !errors.As(mapped, &coded) || coded.Code != ExitFailure {
				t.Fatalf("mapError() = %T %v, want ExitFailure", mapped, mapped)
			}
			if got := coded.Err.Error(); got != test.source.Error() {
				t.Fatalf("mapError() text = %q, want %q", got, test.source.Error())
			}
		})
	}
}

func TestMapErrorLeavesUsageValidationUntyped(t *testing.T) {
	validation := errors.New("actor id is required")

	mapped := mapError(validation)

	var coded *CodedError
	if errors.As(mapped, &coded) {
		t.Fatalf("mapError() = %#v, want untyped usage validation", mapped)
	}
	if !errors.Is(mapped, validation) {
		t.Fatalf("mapError() = %v, want original validation error", mapped)
	}
}
