package cmds

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/LinZiyang666/agentchat/pkg/client"
)

var (
	flagReadBefore string
	flagReadLimit  int
)

// readCmd is the M9 Phase 2 unified "open a room" verb. Default mode
// returns every unread message plus the last N (default 10) read
// messages as context, AND marks each unread row as read in the same
// transaction. Adding --before switches to pure-query history paging
// (no side effects).
var readCmd = &cobra.Command{
	Use:   "read <room-id>",
	Short: "Read a room: show unread + recent context and mark unread as read.",
	Long: `Open a room. Without --before:
  - returns the room's unread messages (cap 200) plus the last
    --limit (default 10) already-read messages as context
  - marks every unread row as read in a single transaction
  - the next 'watch state' frame will reflect the reduced
    totals.unread / mentions / priority counters

With --before <msg-id>:
  - pure history paging — no read state is changed
  - useful for scrolling back through older messages`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		resp, err := c.ReadRoom(cmd.Context(), args[0], client.ReadRoomOptions{
			Before: flagReadBefore,
			Limit:  flagReadLimit,
		})
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(resp)
		}

		// Human-readable TTY rendering. Split the messages slice
		// back into UNREAD / CONTEXT bands using marked_read as the
		// ground truth — the daemon already ordered them oldest →
		// newest with read history first.
		markedSet := make(map[string]struct{}, len(resp.MarkedRead))
		for _, id := range resp.MarkedRead {
			markedSet[id] = struct{}{}
		}
		var unread, ctxBand []int
		for i, m := range resp.Messages {
			if _, hit := markedSet[m.ID]; hit {
				unread = append(unread, i)
			} else {
				ctxBand = append(ctxBand, i)
			}
		}

		if flagReadBefore != "" {
			fmt.Fprintf(os.Stdout, "=== %s (history before %s, %d msgs) ===\n",
				resp.Room.Name, flagReadBefore, len(resp.Messages))
		} else {
			fmt.Fprintf(os.Stdout, "=== %s ===\n", resp.Room.Name)
		}

		printRow := func(idx int) {
			m := resp.Messages[idx]
			// Prefer the M9 Phase 2 hydrated fields: human-readable
			// account name + content with `<@id>` rewritten to
			// `@<name>`. Fall back to the raw fields when ReadRoom
			// couldn't hydrate (external Discord author with no
			// agentchat account, or a non-ReadRoom code path that
			// reuses MessageResponse).
			author := m.AuthorName
			if author == "" {
				author = shortID(m.AuthorAccountID)
			}
			content := m.DisplayContent
			if content == "" {
				content = m.Content
			}
			fmt.Fprintf(os.Stdout, "[%s] %s %s\n",
				renderTime(m.CreatedAt), author, content)
			for _, a := range m.Attachments {
				loc := a.LocalPath
				if loc == "" {
					loc = "(pending download)"
				}
				fmt.Fprintf(os.Stdout, "  [ATTACHMENT] msg=%s name=%q size=%d mime=%s -> %s\n",
					m.ID, a.Filename, a.Size, a.MIME, loc)
			}
		}

		if flagReadBefore == "" && len(ctxBand) > 0 {
			fmt.Fprintf(os.Stdout, "-- context (%d) --\n", len(ctxBand))
			for _, i := range ctxBand {
				printRow(i)
			}
		}
		if flagReadBefore == "" && len(unread) > 0 {
			fmt.Fprintf(os.Stdout, "-- unread (%d) --\n", len(unread))
			for _, i := range unread {
				printRow(i)
			}
		}
		if flagReadBefore != "" {
			for i := range resp.Messages {
				printRow(i)
			}
		}

		if resp.More {
			fmt.Fprintln(os.Stdout, "... more messages available (use --before to paginate)")
		}
		if len(resp.MarkedRead) > 0 {
			fmt.Fprintf(os.Stdout, "✔ marked %d as read\n", len(resp.MarkedRead))
		}
		return nil
	},
}

// shortID renders the first 8 hex chars of a UUIDv7 for terminal
// output (the full id is in the JSON path). Empty / shorter strings
// pass through unchanged.
func shortID(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8]
}

func init() {
	readCmd.Flags().StringVar(&flagReadBefore, "before", "",
		"history paging: return only messages strictly older than this message id; does NOT mark anything as read")
	readCmd.Flags().IntVar(&flagReadLimit, "limit", 0,
		"max history messages to return. Default 10 in normal mode (context band); default 50 with --before (history paging). Max 200. Unread messages in normal mode are not capped.")
	rootCmd.AddCommand(readCmd)
}
