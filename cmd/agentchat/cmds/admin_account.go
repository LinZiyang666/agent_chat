package cmds

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var adminAccountCmd = &cobra.Command{
	Use:   "account",
	Short: "Manage accounts (CRUD).",
}

var (
	flagAccountName string
	flagAccountRole string
)

var adminAccountCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new account.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		c := newClient()
		a, err := c.CreateAccount(cmd.Context(), flagAccountName, flagAccountRole)
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(a)
		}
		fmt.Fprintf(os.Stdout, "Created account %s (%s, %s)\n", a.Name, a.ID, a.Role)
		return nil
	},
}

var adminAccountListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all accounts.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		c := newClient()
		accs, err := c.ListAccounts(cmd.Context())
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(accs)
		}
		rows := make([][]any, 0, len(accs))
		for _, a := range accs {
			rows = append(rows, []any{a.ID, a.Name, a.Role, a.LifecycleState, renderTime(a.CreatedAt)})
		}
		return table([]string{"ID", "NAME", "ROLE", "STATE", "CREATED"}, rows)
	},
}

var adminAccountShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show one account.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		a, err := c.GetAccount(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(a)
		}
		fmt.Fprintf(os.Stdout, "ID:    %s\n", a.ID)
		fmt.Fprintf(os.Stdout, "Name:  %s\n", a.Name)
		fmt.Fprintf(os.Stdout, "Role:  %s\n", a.Role)
		fmt.Fprintf(os.Stdout, "State: %s\n", a.LifecycleState)
		fmt.Fprintf(os.Stdout, "Created: %s\n", renderTime(a.CreatedAt))
		fmt.Fprintf(os.Stdout, "Updated: %s\n", renderTime(a.UpdatedAt))
		return nil
	},
}

var adminAccountDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete an account (cascades to its tokens).",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		if err := c.DeleteAccount(cmd.Context(), args[0]); err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(map[string]string{"deleted": args[0]})
		}
		fmt.Fprintf(os.Stdout, "Deleted account %s\n", args[0])
		return nil
	},
}

var adminAccountSetRoleCmd = &cobra.Command{
	Use:   "set-role <id> <admin|user>",
	Short: "Change an account's role.",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		a, err := c.SetAccountRole(cmd.Context(), args[0], args[1])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(a)
		}
		fmt.Fprintf(os.Stdout, "%s is now %s\n", a.Name, a.Role)
		return nil
	},
}

var (
	flagRenameNewName string
)

var adminAccountRenameCmd = &cobra.Command{
	Use:   "rename <id>",
	Short: "Rename an account.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		a, err := c.RenameAccount(cmd.Context(), args[0], flagRenameNewName)
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(a)
		}
		fmt.Fprintf(os.Stdout, "Renamed to %s\n", a.Name)
		return nil
	},
}

func init() {
	adminAccountCreateCmd.Flags().StringVar(&flagAccountName, "name", "", "account name (required)")
	adminAccountCreateCmd.Flags().StringVar(&flagAccountRole, "role", "user", "account role: admin or user")
	_ = adminAccountCreateCmd.MarkFlagRequired("name") // fix for M2-P3-007

	adminAccountRenameCmd.Flags().StringVar(&flagRenameNewName, "name", "", "new account name (required)")
	_ = adminAccountRenameCmd.MarkFlagRequired("name") // fix for M2-P3-007

	adminAccountCmd.AddCommand(
		adminAccountCreateCmd,
		adminAccountListCmd,
		adminAccountShowCmd,
		adminAccountDeleteCmd,
		adminAccountSetRoleCmd,
		adminAccountRenameCmd,
	)
	adminCmd.AddCommand(adminAccountCmd)
}
