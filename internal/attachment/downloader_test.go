package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LinZiyang666/agentchat/internal/store"
)

// fakeAttachmentRepo is the minimum slice of store.AttachmentRepo the
// downloader exercises in tests. Other Bundle fields are unused so we
// can leave them nil — the downloader only touches Attachments.
type fakeAttachmentRepo struct {
	pending []*store.Attachment
	marked  map[string]markRecord
}

type markRecord struct {
	LocalPath string
	SHA256    string
	At        time.Time
}

func (f *fakeAttachmentRepo) Create(_ context.Context, _ *store.Attachment) error {
	return nil
}
func (f *fakeAttachmentRepo) Get(_ context.Context, _ string) (*store.Attachment, error) {
	return nil, nil
}
func (f *fakeAttachmentRepo) ListByMessage(_ context.Context, _ string) ([]*store.Attachment, error) {
	return nil, nil
}
func (f *fakeAttachmentRepo) ListByMessages(_ context.Context, _ []string) (map[string][]*store.Attachment, error) {
	return nil, nil
}
func (f *fakeAttachmentRepo) ListPendingDownloads(_ context.Context, _ int) ([]*store.Attachment, error) {
	// Return the pending list once, then empty so processOnce
	// converges. We mutate the slice so the second call sees no
	// pending rows.
	out := f.pending
	f.pending = nil
	return out, nil
}
func (f *fakeAttachmentRepo) MarkDownloaded(_ context.Context, id, localPath, sum string, at time.Time) error {
	if f.marked == nil {
		f.marked = map[string]markRecord{}
	}
	f.marked[id] = markRecord{LocalPath: localPath, SHA256: sum, At: at}
	return nil
}

func TestDownloaderFetchesPendingRow(t *testing.T) {
	payload := []byte("attachment bytes — hello downloader")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	repo := &fakeAttachmentRepo{
		pending: []*store.Attachment{
			{
				ID:         "att-123",
				MessageID:  "msg-1",
				Filename:   "hello.txt",
				Size:       int64(len(payload)),
				DiscordURL: srv.URL,
				CreatedAt:  time.Now().UTC(),
			},
		},
	}
	bundle := store.Bundle{Attachments: repo}
	cacheRoot := t.TempDir()
	d := New(bundle, cacheRoot, nil, Options{PollInterval: 10 * time.Millisecond})

	require.NoError(t, d.processOnce(context.Background()))

	require.Contains(t, repo.marked, "att-123", "MarkDownloaded should be called for the fetched row")
	rec := repo.marked["att-123"]
	expectedPath := filepath.Join(cacheRoot, "msg-1", "att-123", "hello.txt")
	assert.Equal(t, expectedPath, rec.LocalPath)

	// File contents match.
	got, err := os.ReadFile(rec.LocalPath)
	require.NoError(t, err)
	assert.Equal(t, payload, got)

	// SHA256 matches the bytes.
	h := sha256.Sum256(payload)
	assert.Equal(t, hex.EncodeToString(h[:]), rec.SHA256)
}

// M7-P3-003 regression: failed rows back off — they are NOT retried
// on every subsequent cycle while the backoff window is open. We
// drive the test clock forward to assert the window opens and closes
// as expected.
func TestDownloaderBacksOffFailedRow(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	repo := &fakeAttachmentRepo{
		pending: []*store.Attachment{
			{
				ID:         "att-flaky",
				MessageID:  "msg-1",
				Filename:   "x.bin",
				Size:       4,
				DiscordURL: srv.URL,
				CreatedAt:  time.Now().UTC(),
			},
		},
	}
	bundle := store.Bundle{Attachments: repo}
	d := New(bundle, t.TempDir(), nil, Options{PollInterval: 10 * time.Millisecond})
	// First call: attempts=1, hits=1, row stays pending.
	// Make ListPendingDownloads keep returning it across cycles so
	// the test can re-invoke processOnce without setup churn.
	row := repo.pending[0]
	repo.pending = []*store.Attachment{row}
	// Pin the downloader clock so the in-process backoff is
	// deterministic.
	base := time.Unix(1_700_000_000, 0).UTC()
	d.nowFn = func() time.Time { return base }
	require.NoError(t, d.processOnce(context.Background()))
	require.Equal(t, 1, hits, "first cycle should fetch once")

	// Same instant → still in backoff window (2s); HTTP must NOT be hit.
	repo.pending = []*store.Attachment{row}
	require.NoError(t, d.processOnce(context.Background()))
	require.Equal(t, 1, hits, "row in backoff window must not be re-fetched")

	// Advance past 2s → next attempt allowed.
	d.nowFn = func() time.Time { return base.Add(3 * time.Second) }
	repo.pending = []*store.Attachment{row}
	require.NoError(t, d.processOnce(context.Background()))
	require.Equal(t, 2, hits, "after backoff window expires, fetch retries")
}

func TestBackoffForCapsAtTwoMinutes(t *testing.T) {
	for _, attempts := range []int{7, 8, 30, 100} {
		require.Equal(t, 120*time.Second, backoffFor(attempts), "attempts=%d", attempts)
	}
}

func TestDownloaderSkipsOversizeRow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Stream large data, but should never be read since size
		// check fires first.
		_, _ = io.Copy(w, io.LimitReader(nil, 0))
	}))
	defer srv.Close()
	repo := &fakeAttachmentRepo{
		pending: []*store.Attachment{
			{
				ID:         "att-huge",
				MessageID:  "msg-1",
				Filename:   "huge.bin",
				Size:       1000000000, // 1 GB — oversize
				DiscordURL: srv.URL,
				CreatedAt:  time.Now().UTC(),
			},
		},
	}
	bundle := store.Bundle{Attachments: repo}
	d := New(bundle, t.TempDir(), nil, Options{
		PollInterval: 10 * time.Millisecond,
		MaxBytes:     50 * 1024 * 1024,
	})
	require.NoError(t, d.processOnce(context.Background()))
	assert.NotContains(t, repo.marked, "att-huge", "oversize attachment must NOT be marked downloaded")
}
