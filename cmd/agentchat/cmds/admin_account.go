package cmds

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/LinZiyang666/agentchat/pkg/client"
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

var flagDiscordBotToken string

var adminAccountSetDiscordCmd = &cobra.Command{
	Use:   "set-discord <id>",
	Short: "Store a Discord bot token on an account.",
	Long: `Store the given Discord bot token on the account. The token is
AES-GCM encrypted with the daemon's master key before being persisted;
the plaintext is not echoed back. Bringing the account online (with
'admin account online') will use this token to open the Discord
session.

The daemon probes the token against Discord's REST GET /users/@me
before saving and adopts Discord's view of the bot's identity:
account.name is snapped to whatever Discord reports as the bot's
username (Discord is the authority). If the resulting name collides
with another agentchat account, this call returns CONFLICT — pick a
different bot, or rename the conflicting account first.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		a, err := c.SetDiscord(cmd.Context(), args[0], flagDiscordBotToken,
			client.SetDiscordOptions{})
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(a)
		}
		fmt.Fprintf(os.Stdout, "Discord bot token set for %s.\n", a.Name)
		return nil
	},
}

var adminAccountOnlineCmd = &cobra.Command{
	Use:   "online <id>",
	Short: "Bring the account's Discord Provider online.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		s, err := c.Online(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(s)
		}
		fmt.Fprintf(os.Stdout, "%s -> %s (%s)\n",
			s.Account.Name, s.Account.LifecycleState, s.ProviderStatus)
		return nil
	},
}

var adminAccountOfflineCmd = &cobra.Command{
	Use:   "offline <id>",
	Short: "Disconnect the account's Discord Provider.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		s, err := c.Offline(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(s)
		}
		fmt.Fprintf(os.Stdout, "%s -> %s\n", s.Account.Name, s.Account.LifecycleState)
		return nil
	},
}

var adminAccountStatusCmd = &cobra.Command{
	Use:   "status <id>",
	Short: "Show the account's lifecycle and Discord connection status.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		s, err := c.Status(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(s)
		}
		fmt.Fprintf(os.Stdout, "Account:  %s (%s)\n", s.Account.Name, s.Account.ID)
		fmt.Fprintf(os.Stdout, "Role:     %s\n", s.Account.Role)
		fmt.Fprintf(os.Stdout, "State:    %s\n", s.Account.LifecycleState)
		fmt.Fprintf(os.Stdout, "Token:    %v\n", s.HasBotToken)
		fmt.Fprintf(os.Stdout, "Provider: %s\n", s.ProviderStatus)
		if s.Identity != nil {
			fmt.Fprintf(os.Stdout, "Discord:  %s (%s)\n", s.Identity.Username, s.Identity.UserID)
		}
		return nil
	},
}

func init() {
	adminAccountCreateCmd.Flags().StringVar(&flagAccountName, "name", "", "account name (required)")
	adminAccountCreateCmd.Flags().StringVar(&flagAccountRole, "role", "user", "account role: admin or user")
	_ = adminAccountCreateCmd.MarkFlagRequired("name") // fix for M2-P3-007

	adminAccountRenameCmd.Flags().StringVar(&flagRenameNewName, "name", "", "new account name (required)")
	_ = adminAccountRenameCmd.MarkFlagRequired("name") // fix for M2-P3-007

	adminAccountSetDiscordCmd.Flags().StringVar(&flagDiscordBotToken, "bot-token", "",
		"Discord bot token (required; obtain from the Developer Portal)")
	_ = adminAccountSetDiscordCmd.MarkFlagRequired("bot-token")

	adminAccountCmd.AddCommand(
		adminAccountCreateCmd,
		adminAccountListCmd,
		adminAccountShowCmd,
		adminAccountDeleteCmd,
		adminAccountSetRoleCmd,
		adminAccountRenameCmd,
		adminAccountSetDiscordCmd,
		adminAccountOnlineCmd,
		adminAccountOfflineCmd,
		adminAccountStatusCmd,
	)
	adminCmd.AddCommand(adminAccountCmd)
}
