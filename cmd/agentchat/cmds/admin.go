package cmds

import "github.com/spf13/cobra"

// adminCmd is the parent for all admin-only operations. Subcommands
// register under it from sibling files.
var adminCmd = &cobra.Command{
	Use:   "admin",
	Short: "Administrative operations (admin role required).",
	Long: `The 'admin' command group contains operations that require
the admin role: creating accounts, managing tokens, reading the
audit log, and so on.`,
}

func init() { rootCmd.AddCommand(adminCmd) }
