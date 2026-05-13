// Package cmds wires up the agentchatd daemon command tree. Each
// subcommand lives in its own file and registers itself via init().
package cmds

import "github.com/spf13/cobra"

// Version is the build-time version string. Override via -ldflags:
//
//	-X github.com/LinZiyang666/agentchat/cmd/agentchatd/cmds.Version=<ver>
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:   "agentchatd",
	Short: "Long-running daemon that bridges agents to Discord.",
	Long: `agentchatd is the local daemon process. It maintains Discord
bot connections, owns the SQLite store, runs the state aggregation
engine, and serves agentchat CLI commands over a Unix-domain socket.`,
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
