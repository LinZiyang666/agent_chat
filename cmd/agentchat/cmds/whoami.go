package cmds

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Print the currently authenticated account.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		c := newClient()
		w, err := c.Whoami(cmd.Context())
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(w)
		}
		fmt.Fprintf(os.Stdout, "ID:    %s\n", w.Account.ID)
		fmt.Fprintf(os.Stdout, "Name:  %s\n", w.Account.Name)
		fmt.Fprintf(os.Stdout, "Role:  %s\n", w.Account.Role)
		fmt.Fprintf(os.Stdout, "State: %s\n", w.Account.LifecycleState)
		fmt.Fprintf(os.Stdout, "Token: %s\n", w.TokenID)
		return nil
	},
}

func init() { rootCmd.AddCommand(whoamiCmd) }
