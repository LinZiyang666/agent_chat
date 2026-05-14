package cmds

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var readCmd = &cobra.Command{
	Use:   "read <message-id>",
	Short: "Mark a message as read.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		s, err := c.MarkMessageRead(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(s)
		}
		fmt.Fprintf(os.Stdout, "Marked %s read at %s\n", s.MessageID, renderTime(*s.ReadAt))
		return nil
	},
}

var replyAckCmd = &cobra.Command{
	Use:   "reply-ack <message-id>",
	Short: "Acknowledge that you've replied to a message (clears requires_ack).",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		s, err := c.ReplyAckMessage(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(s)
		}
		fmt.Fprintf(os.Stdout, "Acked %s at %s\n", s.MessageID, renderTime(*s.RepliedAt))
		return nil
	},
}

func init() {
	rootCmd.AddCommand(readCmd, replyAckCmd)
}
