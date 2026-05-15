package cmds

import (
	"fmt"
	"os"

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
		if err := table([]string{"WHEN", "ID", "AUTHOR", "PRIORITY", "CONTENT"}, rows); err != nil {
			return err
		}
		// M7: render the attachment index below each message body so
		// agents/humans can `xdg-open <local_path>` directly.
		for _, m := range msgs {
			if len(m.Attachments) == 0 {
				continue
			}
			for _, a := range m.Attachments {
				loc := a.LocalPath
				if loc == "" {
					loc = "(pending download)"
				}
				fmt.Fprintf(os.Stdout, "  [ATTACHMENT] msg=%s name=%q size=%d mime=%s -> %s\n",
					m.ID, a.Filename, a.Size, a.MIME, loc)
			}
		}
		return nil
	},
}

func init() {
	historyCmd.Flags().StringVar(&flagHistoryBefore, "before", "",
		"return only messages strictly older than this message id (paging cursor)")
	historyCmd.Flags().IntVar(&flagHistoryLimit, "limit", 0,
		"max messages to return (default 50, max 200)")
	rootCmd.AddCommand(historyCmd)
}
