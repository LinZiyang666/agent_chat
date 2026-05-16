package cmds

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var versionJSON bool

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the agentchatd daemon version.",
	Run: func(cmd *cobra.Command, _ []string) {
		if versionJSON {
			_ = json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{
				"binary":  "agentchatd",
				"version": Version,
			})
			return
		}
		fmt.Fprintf(cmd.OutOrStdout(), "agentchatd %s\n", Version)
	},
}

func init() {
	versionCmd.Flags().BoolVar(&versionJSON, "json", false, "emit version as a JSON object")
	rootCmd.AddCommand(versionCmd)
}
