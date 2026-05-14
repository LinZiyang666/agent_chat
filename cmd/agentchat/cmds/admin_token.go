package cmds

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var adminTokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage API tokens.",
}

var adminTokenCreateCmd = &cobra.Command{
	Use:   "create <account-id>",
	Short: "Issue a new API token for an account.",
	Long: `Issue a fresh API token for the given account. The plaintext
token is printed exactly once on the 'raw' field; the daemon never
exposes it again. Capture it immediately.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		out, err := c.CreateToken(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(out)
		}
		fmt.Fprintf(os.Stdout, "Token ID:   %s\n", out.Token.ID)
		fmt.Fprintf(os.Stdout, "Account:    %s\n", out.Token.AccountID)
		fmt.Fprintf(os.Stdout, "Created:    %s\n", renderTime(out.Token.CreatedAt))
		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout, "Raw token (save now, will not be shown again):")
		fmt.Fprintf(os.Stdout, "  %s\n", out.Raw)
		return nil
	},
}

var adminTokenListCmd = &cobra.Command{
	Use:   "list <account-id>",
	Short: "List API tokens for an account.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		ts, err := c.ListTokens(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(ts)
		}
		rows := make([][]any, 0, len(ts))
		for _, t := range ts {
			revoked := "-"
			if t.RevokedAt != nil {
				revoked = renderTime(*t.RevokedAt)
			}
			rows = append(rows, []any{t.ID, renderTime(t.CreatedAt), revoked, renderTimePtr(t.LastUsedAt)})
		}
		return table([]string{"ID", "CREATED", "REVOKED", "LAST USED"}, rows)
	},
}

var adminTokenRevokeCmd = &cobra.Command{
	Use:   "revoke <token-id>",
	Short: "Revoke an API token.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		if err := c.RevokeToken(cmd.Context(), args[0]); err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(map[string]string{"revoked": args[0]})
		}
		fmt.Fprintf(os.Stdout, "Revoked token %s\n", args[0])
		return nil
	},
}

func init() {
	adminTokenCmd.AddCommand(adminTokenCreateCmd, adminTokenListCmd, adminTokenRevokeCmd)
	adminCmd.AddCommand(adminTokenCmd)
}
