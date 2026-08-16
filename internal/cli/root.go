package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/haochase/haowork/internal/core"
	"github.com/haochase/haowork/internal/teamapi"
	"github.com/spf13/cobra"
)

const (
	ExitOK       = 0
	ExitFailure  = 1
	ExitUsage    = 2
	ExitConflict = 3
	ExitGate     = 4
	ExitApproval = 5
	ExitOffline  = 6
)

type CommandFactory func(*Dependencies) *cobra.Command

type GlobalOptions struct {
	Project string
	JSON    bool
}

type Dependencies struct {
	Stdout           io.Writer
	Stderr           io.Writer
	Options          *GlobalOptions
	Commands         []CommandFactory
	TeamTokens       teamapi.TokenSource
	Transfer         *core.TransferConfig
	TransferProvider TransferConfigProvider
}

type CodedError struct {
	Code int
	Err  error
}

type errorOutput struct {
	Code  int    `json:"code"`
	Error string `json:"error"`
}

func (e *CodedError) Error() string { return e.Err.Error() }
func (e *CodedError) Unwrap() error { return e.Err }

func NewRoot(deps Dependencies) *cobra.Command {
	if deps.Options == nil {
		deps.Options = &GlobalOptions{}
	}
	cmd := &cobra.Command{
		Use:   "haowork",
		Short: "External control plane for long-running coding work",
		Long:  "Haowork is an external control plane for goal, task, evidence, and history governance.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if deps.Options.JSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(struct{}{})
			}
			return cmd.Help()
		},
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	defaultHelp := cmd.HelpFunc()
	cmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if deps.Options.JSON {
			_ = json.NewEncoder(cmd.OutOrStdout()).Encode(struct{}{})
			return
		}
		defaultHelp(cmd, args)
	})
	cmd.SetOut(deps.Stdout)
	cmd.SetErr(deps.Stderr)
	cmd.PersistentFlags().StringVar(&deps.Options.Project, "project", ".", "project path")
	cmd.PersistentFlags().BoolVar(&deps.Options.JSON, "json", false, "write machine-readable JSON")
	for _, factory := range deps.Commands {
		cmd.AddCommand(factory(&deps))
	}
	return cmd
}

func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return ExecuteWithDependencies(ctx, args, stdout, stderr, Dependencies{TransferProvider: LocalTransferConfigProvider{}})
}

// ExecuteWithDependencies is the controlled embedding entry point. Hosts
// that own transfer capabilities inject a provider; the standard binary uses
// LocalTransferConfigProvider and therefore fails closed when unavailable.
func ExecuteWithDependencies(ctx context.Context, args []string, stdout, stderr io.Writer, deps Dependencies) int {
	options := &GlobalOptions{}
	var commandOutput bytes.Buffer
	deps.Stdout = &commandOutput
	deps.Stderr = stderr
	deps.Options = options
	if deps.Commands == nil {
		deps.Commands = DefaultCommands()
	}
	cmd := NewRoot(deps)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(ctx); err != nil {
		code := ExitUsage
		message := err.Error()
		var coded *CodedError
		if errors.As(err, &coded) {
			code = coded.Code
			message = coded.Err.Error()
		}
		if options.JSON {
			if encodeErr := json.NewEncoder(stdout).Encode(errorOutput{Code: code, Error: message}); encodeErr != nil {
				fmt.Fprintln(stderr, encodeErr)
				return ExitFailure
			}
			return code
		}
		fmt.Fprintln(stderr, message)
		return code
	}
	if commandOutput.Len() > 0 {
		if _, err := stdout.Write(commandOutput.Bytes()); err != nil {
			fmt.Fprintln(stderr, err)
			return ExitFailure
		}
	}
	return ExitOK
}
