package cmds

import (
	"fmt"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the agentchat client version.",
	Run: func(cmd *cobra.Command, _ []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "agentchat %s\n", Version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
