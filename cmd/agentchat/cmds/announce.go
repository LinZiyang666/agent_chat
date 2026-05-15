package cmds

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// M6 — room announcements ("room announce ...") and ack
// ("ack-announcement <id>").

var roomAnnounceCmd = &cobra.Command{
	Use:   "announce <room-id> <content>",
	Short: "Post a new room announcement (M6). Bumps version, resets unread for every member.",
	Long: `Post a new announcement to the room. Each post creates a fresh
row with version = previous+1; the GET endpoint always returns the
latest version. Every current member of the room is forced unread on
the new version regardless of subscription state (announcements are
mandatory-read per requirements §6).

Any member of the room may post (admins bypass the membership gate).`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		a, err := c.CreateAnnouncement(cmd.Context(), args[0], args[1])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(a)
		}
		fmt.Fprintf(os.Stdout, "Announcement %s (v%d) posted in room %s at %s\n",
			a.ID, a.Version, a.RoomID, renderTime(a.CreatedAt))
		return nil
	},
}

var roomAnnounceShowCmd = &cobra.Command{
	Use:   "announce-show <room-id>",
	Short: "Show the latest announcement for a room.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		a, err := c.GetAnnouncement(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(a)
		}
		fmt.Fprintf(os.Stdout, "ID:       %s\n", a.ID)
		fmt.Fprintf(os.Stdout, "Room:     %s\n", a.RoomID)
		fmt.Fprintf(os.Stdout, "Version:  %d\n", a.Version)
		fmt.Fprintf(os.Stdout, "By:       %s\n", a.CreatedBy)
		fmt.Fprintf(os.Stdout, "Created:  %s\n", renderTime(a.CreatedAt))
		fmt.Fprintf(os.Stdout, "Read:     %v\n", a.Read)
		fmt.Fprintf(os.Stdout, "----\n%s\n", a.Content)
		return nil
	},
}

var ackAnnouncementCmd = &cobra.Command{
	Use:   "ack-announcement <announcement-id>",
	Short: "Acknowledge (mark as read) a room announcement (M6).",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		r, err := c.AckAnnouncement(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(r)
		}
		fmt.Fprintf(os.Stdout, "ACK announcement %s by %s at %s\n",
			r.AnnouncementID, r.AccountID, renderTime(r.ReadAt))
		return nil
	},
}

// M6 — system announcements ("admin system-announce ...",
// "system-announcements", "ack-system <id>").

var systemAnnouncementsCmd = &cobra.Command{
	Use:   "system-announcements",
	Short: "List system announcements with their read state for the caller.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		c := newClient()
		anns, err := c.ListSystemAnnouncements(cmd.Context())
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(anns)
		}
		rows := make([][]any, 0, len(anns))
		for _, a := range anns {
			summary := a.Content
			if len(summary) > 60 {
				summary = summary[:57] + "..."
			}
			rows = append(rows, []any{a.ID, renderTime(a.CreatedAt), a.Read, summary})
		}
		return table([]string{"ID", "CREATED", "READ", "CONTENT"}, rows)
	},
}

var ackSystemCmd = &cobra.Command{
	Use:   "ack-system <sys-ann-id>",
	Short: "Acknowledge a system announcement.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		r, err := c.AckSystemAnnouncement(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(r)
		}
		fmt.Fprintf(os.Stdout, "ACK system announcement %s by %s at %s\n",
			r.SysAnnID, r.AccountID, renderTime(r.ReadAt))
		return nil
	},
}

var adminSystemAnnounceCmd = &cobra.Command{
	Use:   "system-announce <content>",
	Short: "Post a new global system announcement (admin only, M6).",
	Long: `Post a system announcement. Every account is implicitly unread
until they ACK via 'agentchat ack-system <id>'. Surfaces in the M5
state UI under the system_announcements dimension.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		a, err := c.CreateSystemAnnouncement(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(a)
		}
		fmt.Fprintf(os.Stdout, "System announcement %s posted at %s\n",
			a.ID, renderTime(a.CreatedAt))
		return nil
	},
}

func init() {
	roomCmd.AddCommand(roomAnnounceCmd, roomAnnounceShowCmd)
	rootCmd.AddCommand(ackAnnouncementCmd, systemAnnouncementsCmd, ackSystemCmd)
	adminCmd.AddCommand(adminSystemAnnounceCmd)
}
