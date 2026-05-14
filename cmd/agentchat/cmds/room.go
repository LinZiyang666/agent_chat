package cmds

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/LinZiyang666/agentchat/pkg/client"
)

var (
	flagRoomName            string
	flagRoomNewName         string
	flagRoomIncludeArchived bool
	flagInviteSubscribed    bool
)

var roomCmd = &cobra.Command{
	Use:   "room",
	Short: "Manage rooms (1:1 with Discord channels).",
}

var roomCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new room (admin only).",
	RunE: func(cmd *cobra.Command, _ []string) error {
		c := newClient()
		r, err := c.CreateRoom(cmd.Context(), flagRoomName)
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(r)
		}
		fmt.Fprintf(os.Stdout, "Created room %s (%s, channel=%s)\n",
			r.Name, r.ID, r.DiscordChannelID)
		return nil
	},
}

var roomListCmd = &cobra.Command{
	Use:   "list",
	Short: "List rooms visible to the caller.",
	RunE: func(cmd *cobra.Command, _ []string) error {
		c := newClient()
		rooms, err := c.ListRooms(cmd.Context(), client.ListRoomsOptions{
			IncludeArchived: flagRoomIncludeArchived,
		})
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(rooms)
		}
		rows := make([][]any, 0, len(rooms))
		for _, r := range rooms {
			rows = append(rows, []any{r.ID, r.Name, r.DiscordChannelID, r.Archived, renderTime(r.CreatedAt)})
		}
		return table([]string{"ID", "NAME", "CHANNEL", "ARCHIVED", "CREATED"}, rows)
	},
}

var roomShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show one room.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		r, err := c.GetRoom(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(r)
		}
		fmt.Fprintf(os.Stdout, "ID:       %s\n", r.ID)
		fmt.Fprintf(os.Stdout, "Name:     %s\n", r.Name)
		fmt.Fprintf(os.Stdout, "Channel:  %s\n", r.DiscordChannelID)
		fmt.Fprintf(os.Stdout, "Archived: %v\n", r.Archived)
		fmt.Fprintf(os.Stdout, "Created:  %s\n", renderTime(r.CreatedAt))
		fmt.Fprintf(os.Stdout, "Updated:  %s\n", renderTime(r.UpdatedAt))
		return nil
	},
}

var roomRenameCmd = &cobra.Command{
	Use:   "rename <id>",
	Short: "Rename a room (admin only).",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		r, err := c.RenameRoom(cmd.Context(), args[0], flagRoomNewName)
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(r)
		}
		fmt.Fprintf(os.Stdout, "Renamed room %s -> %s\n", args[0], r.Name)
		return nil
	},
}

var roomArchiveCmd = &cobra.Command{
	Use:   "archive <id>",
	Short: "Archive a room (admin only). History survives.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		r, err := c.ArchiveRoom(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(r)
		}
		fmt.Fprintf(os.Stdout, "Archived room %s\n", r.ID)
		return nil
	},
}

var roomDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a room (admin only). Also deletes the Discord channel.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		if err := c.DeleteRoom(cmd.Context(), args[0]); err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(map[string]string{"deleted": args[0]})
		}
		fmt.Fprintf(os.Stdout, "Deleted room %s\n", args[0])
		return nil
	},
}

var roomInviteCmd = &cobra.Command{
	Use:   "invite <room-id> <account-id>",
	Short: "Add an account to a room (admin only).",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		m, err := c.InviteMember(cmd.Context(), args[0], args[1], flagInviteSubscribed)
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(m)
		}
		fmt.Fprintf(os.Stdout, "Added %s to room %s (subscribed=%v)\n",
			m.AccountID, m.RoomID, m.Subscribed)
		return nil
	},
}

var roomKickCmd = &cobra.Command{
	Use:   "kick <room-id> <account-id>",
	Short: "Remove an account from a room (admin only).",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		if err := c.KickMember(cmd.Context(), args[0], args[1]); err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(map[string]string{
				"room_id": args[0], "account_id": args[1],
			})
		}
		fmt.Fprintf(os.Stdout, "Removed %s from room %s\n", args[1], args[0])
		return nil
	},
}

var roomMembersCmd = &cobra.Command{
	Use:   "members <room-id>",
	Short: "List members of a room.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		members, err := c.ListMembers(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(members)
		}
		rows := make([][]any, 0, len(members))
		for _, m := range members {
			rows = append(rows, []any{m.AccountID, m.Subscribed, renderTime(m.JoinedAt)})
		}
		return table([]string{"ACCOUNT", "SUBSCRIBED", "JOINED"}, rows)
	},
}

var roomSubscribeCmd = &cobra.Command{
	Use:   "subscribe <room-id>",
	Short: "Subscribe to a room you already belong to.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		m, err := c.SetSubscribed(cmd.Context(), args[0], true)
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(m)
		}
		fmt.Fprintf(os.Stdout, "Subscribed to room %s\n", m.RoomID)
		return nil
	},
}

var roomUnsubscribeCmd = &cobra.Command{
	Use:   "unsubscribe <room-id>",
	Short: "Unsubscribe from a room you belong to (stay a member, drop notifications).",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		c := newClient()
		m, err := c.SetSubscribed(cmd.Context(), args[0], false)
		if err != nil {
			return err
		}
		if outputJSON() {
			return writeJSON(m)
		}
		fmt.Fprintf(os.Stdout, "Unsubscribed from room %s\n", m.RoomID)
		return nil
	},
}

func init() {
	roomCreateCmd.Flags().StringVar(&flagRoomName, "name", "", "room name (required)")
	_ = roomCreateCmd.MarkFlagRequired("name")

	roomListCmd.Flags().BoolVar(&flagRoomIncludeArchived, "include-archived", false,
		"include archived rooms in the listing")

	roomRenameCmd.Flags().StringVar(&flagRoomNewName, "name", "", "new room name (required)")
	_ = roomRenameCmd.MarkFlagRequired("name")

	roomInviteCmd.Flags().BoolVar(&flagInviteSubscribed, "subscribe", false,
		"also mark the new member as subscribed")

	roomCmd.AddCommand(
		roomCreateCmd,
		roomListCmd,
		roomShowCmd,
		roomRenameCmd,
		roomArchiveCmd,
		roomDeleteCmd,
		roomInviteCmd,
		roomKickCmd,
		roomMembersCmd,
		roomSubscribeCmd,
		roomUnsubscribeCmd,
	)
	rootCmd.AddCommand(roomCmd)
}
