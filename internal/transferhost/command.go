package transferhost

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/haochase/haowork/internal/cli"
	"github.com/haochase/haowork/internal/model"
	"github.com/haochase/haowork/internal/physicalacceptance"
	"github.com/spf13/cobra"
)

func NewKeysCommand(deps *cli.Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "keys", Short: "Manage trusted Transfer Core signing keys"}
	var privatePath, publicPath string
	generate := &cobra.Command{
		Use:   "generate",
		Short: "Generate an owner-only Ed25519 signing key pair",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := GenerateKeyPair(privatePath, publicPath); err != nil {
				return err
			}
			result := struct {
				PrivatePath string `json:"private_path"`
				PublicPath  string `json:"public_path"`
			}{PrivatePath: privatePath, PublicPath: publicPath}
			if deps != nil && deps.Options != nil && deps.Options.JSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "generated Transfer Core key pair: %s, %s\n", privatePath, publicPath)
			return err
		},
	}
	generate.Flags().StringVar(&privatePath, "private", "", "owner-only PKCS#8 private key output path")
	generate.Flags().StringVar(&publicPath, "public", "", "owner-only PKIX public key output path")
	_ = generate.MarkFlagRequired("private")
	_ = generate.MarkFlagRequired("public")
	command.AddCommand(generate)
	return command
}

func NewBootstrapCommand(provider FileProvider) cli.CommandFactory {
	return func(deps *cli.Dependencies) *cobra.Command {
		var actorID string
		var confirmed bool
		command := &cobra.Command{
			Use:   "bootstrap",
			Short: "Register configured logical Agents and initial runtime bindings",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if !confirmed {
					return &cli.CodedError{Code: cli.ExitApproval, Err: fmt.Errorf("--confirm is required for trusted topology bootstrap")}
				}
				bindings, err := provider.BootstrapProject(cmd.Context(), deps.Options.Project, model.Actor{ID: actorID, Kind: model.ActorHuman, Role: model.RoleOwner})
				if err != nil {
					return err
				}
				result := struct {
					Bindings int `json:"bindings"`
				}{Bindings: len(bindings)}
				if deps.Options.JSON {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "bootstrapped %d trusted runtime binding(s)\n", len(bindings))
				return err
			},
		}
		command.Flags().StringVar(&actorID, "actor", "", "human Owner actor ID")
		command.Flags().BoolVar(&confirmed, "confirm", false, "confirm trusted topology bootstrap")
		_ = command.MarkFlagRequired("actor")
		return command
	}
}

func NewProtectCommand(deps *cli.Dependencies) *cobra.Command {
	var confirmed bool
	command := &cobra.Command{
		Use:   "protect FILE...",
		Short: "Restrict trusted Transfer Core files to the current owner",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, paths []string) error {
			if !confirmed {
				return &cli.CodedError{Code: cli.ExitApproval, Err: fmt.Errorf("--confirm is required to protect trusted files")}
			}
			for _, path := range paths {
				if err := ProtectOwnerOnlyFile(path); err != nil {
					return err
				}
			}
			result := struct {
				Protected int `json:"protected"`
			}{Protected: len(paths)}
			if deps != nil && deps.Options != nil && deps.Options.JSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "protected %d trusted file(s)\n", len(paths))
			return err
		},
	}
	command.Flags().BoolVar(&confirmed, "confirm", false, "confirm owner-only permission changes")
	return command
}

func NewPhysicalCommand(deps *cli.Dependencies) *cobra.Command {
	command := &cobra.Command{Use: "physical", Short: "Classify physical dual-zone evidence"}
	var input string
	status := &cobra.Command{
		Use:   "status",
		Short: "Read a redacted evidence summary without changing state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			file, err := os.Open(input)
			if err != nil {
				return err
			}
			defer file.Close()
			evidence, err := physicalacceptance.Load(io.LimitReader(file, 1<<20))
			if err != nil {
				return err
			}
			result := physicalacceptance.Evaluate(evidence)
			if deps != nil && deps.Options != nil && deps.Options.JSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			if len(result.Reasons) == 0 {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), result.Status)
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", result.Status, strings.Join(result.Reasons, "; "))
			return err
		},
	}
	status.Flags().StringVar(&input, "input", "", "redacted physical evidence JSON")
	_ = status.MarkFlagRequired("input")
	command.AddCommand(status)
	return command
}
