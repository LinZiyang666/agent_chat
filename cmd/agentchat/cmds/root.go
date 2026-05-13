// Package cmds wires up the agentchat CLI command tree. Each subcommand
// lives in its own file and registers itself onto rootCmd via init().
package cmds

import "github.com/spf13/cobra"

// Version is the build-time version string. Override via -ldflags:
//
//	-X github.com/LinZiyang666/agentchat/cmd/agentchat/cmds.Version=<ver>
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:   "agentchat",
	Short: "Command-line chat client for agents and humans (client side).",
	Long: `agentchat is a CLI that talks to a local agentchatd daemon and lets
agents (or humans) send and receive messages, manage rooms, and watch
state. Discord is the underlying transport, but the CLI surface is
platform-agnostic.`,
	SilenceUsage:  true,
	SilenceErrors: false,
}

func init() {
	rootCmd.Version = Version
}

// Execute runs the root command. Errors are returned so main() can map
// them to a non-zero exit code; user-facing printing is handled by cobra.
func Execute() error {
	return rootCmd.Execute()
}
