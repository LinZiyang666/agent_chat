package cmds

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/pkg/client"
)

var adminAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Inspect the administrative audit log.",
}

var (
	flagAuditAccount string
	flagAuditSince   string
	flagAuditLimit   int
)

var adminAuditListCmd = &cobra.Command{
	Use:   "list",
	Short: "List audit-log entries (newest first).",
	RunE: func(cmd *cobra.Command, _ []string) error {
		opts := client.ListAuditOptions{
			AccountID: flagAuditAccount,
			Limit:     flagAuditLimit,
		}
		if flagAuditSince != "" {
			t, err := time.Parse(time.RFC3339, flagAuditSince)
			if err != nil {
				return errcode.Wrap(err, errcode.InvalidArgument,
					"--since must be RFC3339, e.g. 2026-05-13T00:00:00Z")
			}
			opts.Since = &t
		}
		c := newClient()
		entries, err := c.ListAudit(cmd.Context(), opts)
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(entries)
		}
		rows := make([][]any, 0, len(entries))
		for _, e := range entries {
			rows = append(rows, []any{renderTime(e.CreatedAt), e.AccountID, e.Action, e.Target})
		}
		return table([]string{"WHEN", "ACTOR", "ACTION", "TARGET"}, rows)
	},
}

func init() {
	adminAuditListCmd.Flags().StringVar(&flagAuditAccount, "account", "", "filter by acting account ID")
	adminAuditListCmd.Flags().StringVar(&flagAuditSince, "since", "", "RFC3339 lower bound (e.g. 2026-05-13T00:00:00Z)")
	adminAuditListCmd.Flags().IntVar(&flagAuditLimit, "limit", 0, "max rows to return (0 = server default)")

	adminAuditCmd.AddCommand(adminAuditListCmd)
	adminCmd.AddCommand(adminAuditCmd)
}
