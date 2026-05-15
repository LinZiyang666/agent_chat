package cmds

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the agentchat client version.",
	Run: func(cmd *cobra.Command, _ []string) {
		// M8-C-P1-002: honor --json. The cobra "Version" template
		// path emits the bare version, but a `version` subcommand
		// invocation with --json should produce a structured object
		// so an agent can branch on the field.
		if flagJSON {
			_ = json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{
				"binary":  "agentchat",
				"version": Version,
			})
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "agentchat %s\n", Version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
