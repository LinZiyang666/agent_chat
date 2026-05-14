package cmds

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var stateCmd = &cobra.Command{
	Use:   "state",
	Short: "Show the current per-account state snapshot.",
	Long: `Print a one-shot Snapshot for the calling account (the 8 dimensions
from requirements §5.2: totals, per-room unread, mentions, pending
acks, priority feed, new rooms, recently active, health bar).

Output: JSON by default. Re-run via 'watch state' for a live NDJSON
stream that updates within ~200ms of any mutation.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c := newClient()
		s, err := c.GetState(cmd.Context())
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(s)
		}
		renderStateHuman(os.Stdout, s)
		return nil
	},
}

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Subscribe to live state updates.",
}

var watchStateCmd = &cobra.Command{
	Use:   "state",
	Short: "Stream the per-account state snapshot as NDJSON (one frame per change).",
	Long: `Open a long-lived connection that emits one JSON frame on connect
(the current snapshot) and one frame per debounced state change.
Idle connections emit no bytes — Ctrl-C to stop.

Frames are written to stdout line-by-line with no Go-side buffering,
but downstream tools may block-buffer their own output by default when
stdout is a pipe. If you're piping into jq or similar, ask it to flush
per line, e.g.:

    agentchat watch state | jq --unbuffered '.totals'
    agentchat watch state | stdbuf -oL jq '.totals'
    agentchat watch state | grep --line-buffered version

Without one of these, the consumer may sit on a full block-buffer's
worth of frames before printing anything (you'll see "no output" until
the stream closes). This is downstream behaviour, not an agentchat
buffering bug — direct file redirect (` + "`" + `> file` + "`" + `) shows the per-frame
flush is happening correctly.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		c := newClient()
		stream, err := c.WatchState(cmd.Context())
		if err != nil {
			return err
		}
		defer stream.Close()
		// Copy NDJSON straight to stdout. Output is always JSON;
		// human rendering is only available via the one-shot
		// `agentchat state` form.
		//
		// os.Stdout is an *os.File — writes are syscalls, not Go-
		// buffered, so each line reaches the kernel pipe immediately.
		// Downstream consumers that block-buffer (e.g., jq without
		// --unbuffered) may still appear to "hang"; see the Long
		// help above.
		br := bufio.NewReader(stream)
		for {
			line, err := br.ReadString('\n')
			if len(line) > 0 {
				if _, werr := os.Stdout.WriteString(line); werr != nil {
					return werr
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
		}
	},
}

func init() {
	watchCmd.AddCommand(watchStateCmd)
	rootCmd.AddCommand(stateCmd, watchCmd)
}

// renderStateHuman writes a compact TTY-friendly view of a Snapshot.
// JSON-callers should not see this — the daemon emits JSON natively.
func renderStateHuman(w io.Writer, raw any) {
	// We accept `any` so the CLI doesn't need to import the state
	// package's full type — pkg/client exposes a parsed type that
	// json-marshals back identically. Just round-trip through JSON
	// for a quick rendering.
	b, err := json.Marshal(raw)
	if err != nil {
		fmt.Fprintln(w, "<unrenderable state>")
		return
	}
	var s map[string]any
	if err := json.Unmarshal(b, &s); err != nil {
		fmt.Fprintln(w, "<unrenderable state>")
		return
	}

	fmt.Fprintf(w, "Account: %v\n", s["account_id"])
	fmt.Fprintf(w, "Version: %v   EmittedAt: %v\n", s["version"], s["emitted_at"])
	if t, ok := s["totals"].(map[string]any); ok {
		fmt.Fprintf(w, "Totals:  unread=%v  mentions=%v  pending=%v  priority=%v\n",
			t["unread"], t["mentions"], t["pending_acks"], t["priority"])
	}
	if h, ok := s["health"].(map[string]any); ok {
		fmt.Fprintf(w, "Health:  provider=%v  discord_reachable=%v\n",
			h["provider_status"], h["discord_reachable"])
	}

	if rooms, _ := s["rooms"].([]any); len(rooms) > 0 {
		fmt.Fprintln(w, "Rooms (subscribed, unread):")
		for _, x := range rooms {
			r, _ := x.(map[string]any)
			fmt.Fprintf(w, "  %v\t%v\t%v\n", r["unread"], r["name"], r["room_id"])
		}
	}
	renderList(w, "Mentions", s["mentions"])
	renderList(w, "PendingAcks", s["pending_acks"])
	renderList(w, "Priority", s["priority"])

	if newRooms, _ := s["new_rooms"].([]any); len(newRooms) > 0 {
		fmt.Fprintln(w, "New rooms:")
		for _, x := range newRooms {
			r, _ := x.(map[string]any)
			fmt.Fprintf(w, "  %v\t%v\tsubscribed=%v\n", r["name"], r["joined_at"], r["subscribed"])
		}
	}
	if recent, _ := s["recently_active"].([]any); len(recent) > 0 {
		fmt.Fprintln(w, "Recently active rooms:")
		for _, x := range recent {
			r, _ := x.(map[string]any)
			last := r["last_message_at"]
			if last == nil {
				last = "—"
			}
			fmt.Fprintf(w, "  %v\tlast=%v\n", r["name"], last)
		}
	}
}

func renderList(w io.Writer, label string, val any) {
	list, ok := val.([]any)
	if !ok || len(list) == 0 {
		return
	}
	fmt.Fprintf(w, "%s:\n", label)
	for _, x := range list {
		m, _ := x.(map[string]any)
		content := fmt.Sprint(m["content"])
		if len(content) > 80 {
			content = content[:77] + "..."
		}
		fmt.Fprintf(w, "  [%v] %s · %s\n",
			m["priority"], m["room_name"], strings.TrimSpace(content))
	}
}
