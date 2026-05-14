package cmds

import (
	"github.com/spf13/cobra"

	"github.com/LinZiyang666/agentchat/pkg/client"
)

var (
	flagHistoryBefore string
	flagHistoryLimit  int
)

var historyCmd = &cobra.Command{
	Use:   "history <room-id>",
	Short: "List recent messages in a room (newest first).",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		msgs, err := c.ListMessages(cmd.Context(), args[0], client.ListMessagesOptions{
			Before: flagHistoryBefore,
			Limit:  flagHistoryLimit,
		})
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(msgs)
		}
		rows := make([][]any, 0, len(msgs))
		for _, m := range msgs {
			rows = append(rows, []any{
				renderTime(m.CreatedAt),
				m.ID,
				m.AuthorAccountID,
				m.Priority,
				m.Content,
			})
		}
		return table([]string{"WHEN", "ID", "AUTHOR", "PRIORITY", "CONTENT"}, rows)
	},
}

func init() {
	historyCmd.Flags().StringVar(&flagHistoryBefore, "before", "",
		"return only messages strictly older than this message id (paging cursor)")
	historyCmd.Flags().IntVar(&flagHistoryLimit, "limit", 0,
		"max messages to return (default 50, max 200)")
	rootCmd.AddCommand(historyCmd)
}
