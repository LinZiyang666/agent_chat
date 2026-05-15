package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/LinZiyang666/agentchat/internal/store"
)

func TestPhase3DeletingAnnouncementCreatorDoesNotViolateFK(t *testing.T) {
	ctx := context.Background()
	s := newM4Store(t)
	bundle := s.Bundle()

	creator := mustCreateAccount(t, s, "announcement-owner")
	room := mustCreateRoom(t, s, "phase3-announcements", "phase3-channel")
	now := time.Now().UTC()

	if err := bundle.Announcements.Create(ctx, &store.Announcement{
		ID:        "ann_phase3_delete_creator",
		RoomID:    room.ID,
		Version:   1,
		Content:   "room policy",
		CreatedBy: creator.ID,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("create announcement: %v", err)
	}

	if err := bundle.SystemAnnouncements.Create(ctx, &store.SystemAnnouncement{
		ID:        "sys_phase3_delete_creator",
		Content:   "system policy",
		CreatedBy: creator.ID,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("create system announcement: %v", err)
	}

	if err := bundle.Accounts.Delete(ctx, creator.ID); err != nil {
		t.Fatalf("delete announcement creator should preserve announcements or explicitly restrict deletion; got: %v", err)
	}
}
