package cmds

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// debugCmd is the parent for ad-hoc diagnostic commands. M3 ships
// `debug send` and `debug events` for verifying the Discord adapter;
// M4 will retire them in favor of the rooms / state subsystems.
var debugCmd = &cobra.Command{
	Use:   "debug",
	Short: "Diagnostic commands (M3+; intended for operators).",
	Long: `'debug' groups low-level commands for verifying daemon
behavior end to end. They bypass the rooms and state subsystems and
talk directly to the Connector's live Providers, which makes them
useful for verifying a fresh Discord setup but inappropriate for
agent workflows.`,
}

var (
	flagDebugSendAccount string
	flagDebugSendChannel string
	flagDebugSendText    string
)

var debugSendCmd = &cobra.Command{
	Use:   "send",
	Short: "Send a message via the named account's live Provider.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		c := newClient()
		resp, err := c.DebugSend(cmd.Context(),
			flagDebugSendAccount, flagDebugSendChannel, flagDebugSendText)
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(resp)
		}
		fmt.Fprintf(os.Stdout, "Sent %s to channel %s\n", resp.Message.ID, resp.Message.ChannelID)
		return nil
	},
}

var flagDebugEventsAccount string

var debugEventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Stream live events from an account's Provider (NDJSON).",
	Long: `Stream events as NDJSON: one JSON object per line, no
heartbeat, the stream ends when the daemon-side Provider disconnects
or this command is interrupted (Ctrl-C).

Note: Discord's gateway does not echo a bot's own outbound messages
back to itself, so '--account X' will NOT see message_new events for
messages sent BY account X. Trigger from a different account (another
bot in the same guild, or a human in the Discord client) to see
message_new frames here.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c := newClient()
		evCh, err := c.StreamEvents(cmd.Context(), flagDebugEventsAccount)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		for ev := range evCh {
			if err := enc.Encode(ev); err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
		}
		return nil
	},
}

func init() {
	debugSendCmd.Flags().StringVar(&flagDebugSendAccount, "account", "", "account ID of the live Provider (required)")
	debugSendCmd.Flags().StringVar(&flagDebugSendChannel, "channel", "", "Discord channel ID (required)")
	debugSendCmd.Flags().StringVar(&flagDebugSendText, "text", "", "message body (required)")
	_ = debugSendCmd.MarkFlagRequired("account")
	_ = debugSendCmd.MarkFlagRequired("channel")
	_ = debugSendCmd.MarkFlagRequired("text")

	debugEventsCmd.Flags().StringVar(&flagDebugEventsAccount, "account", "", "account ID to stream events for (required)")
	_ = debugEventsCmd.MarkFlagRequired("account")

	debugCmd.AddCommand(debugSendCmd, debugEventsCmd)
	rootCmd.AddCommand(debugCmd)
}
