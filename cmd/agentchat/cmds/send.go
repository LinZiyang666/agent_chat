package cmds

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/pkg/client"
)

var (
	flagSendReplyTo     string
	flagSendRequiresAck bool
	flagSendPriority    string
	flagSendFromFile    string
	flagSendMentionAll  bool
	flagSendAttach      []string
)

var sendCmd = &cobra.Command{
	Use:   "send <room-id> [text]",
	Short: "Send a message to a room.",
	Long: `Send a message to a room (the caller's account must be a member and
online). The text may be passed as the second positional argument, or
read from stdin via "--file -".`,
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		roomID := args[0]
		var content string
		switch {
		case len(args) == 2:
			content = args[1]
		case flagSendFromFile == "-":
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			content = string(b)
		case flagSendFromFile != "":
			b, err := os.ReadFile(flagSendFromFile)
			if err != nil {
				return err
			}
			content = string(b)
		case len(flagSendAttach) > 0:
			// Attachment-only message: empty content is fine, Discord
			// renders the upload as the visible body.
			content = ""
		default:
			return errcode.New(errcode.InvalidArgument,
				"provide message text as the second argument, --file, or at least one --attach")
		}
		if content == "" && len(flagSendAttach) == 0 {
			return errcode.New(errcode.InvalidArgument, "content is empty")
		}

		atts := make([]client.SendAttachment, 0, len(flagSendAttach))
		for _, p := range flagSendAttach {
			atts = append(atts, client.SendAttachment{Path: p})
		}

		c := newClient()
		m, err := c.SendMessage(cmd.Context(), roomID, content, client.SendMessageOptions{
			ReplyToID:   flagSendReplyTo,
			RequiresAck: flagSendRequiresAck,
			Priority:    flagSendPriority,
			MentionAll:  flagSendMentionAll,
			Attachments: atts,
		})
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(m)
		}
		fmt.Fprintf(os.Stdout, "Sent message %s (discord=%s) at %s\n",
			m.ID, m.DiscordMsgID, renderTime(m.CreatedAt))
		return nil
	},
}

func init() {
	sendCmd.Flags().StringVar(&flagSendReplyTo, "reply", "", "message id this is a reply to")
	sendCmd.Flags().BoolVar(&flagSendRequiresAck, "requires-ack", false,
		"flag the message as requiring a reply-ack from recipients")
	sendCmd.Flags().StringVar(&flagSendPriority, "priority", "",
		"priority band: normal | urgent | system (default normal)")
	sendCmd.Flags().StringVar(&flagSendFromFile, "file", "",
		"read message content from this file; use '-' for stdin")
	sendCmd.Flags().BoolVar(&flagSendMentionAll, "all", false,
		"M6 @all: every member of the room sees this in their mentions feed")
	sendCmd.Flags().StringArrayVar(&flagSendAttach, "attach", nil,
		"M7: attach a local file (repeatable; per-file ≤ 10 MB on free Discord servers, lowered from 25 MB in 2024-09)")
	rootCmd.AddCommand(sendCmd)
}
