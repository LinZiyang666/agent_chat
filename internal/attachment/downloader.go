// Package attachment manages downloads of remote attachments into a
// local cache and the on-disk index keyed by room and message. See
// docs/02-requirements-final.md §3.4.
package attachment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/LinZiyang666/agentchat/internal/errcode"
	"github.com/LinZiyang666/agentchat/internal/store"
)

// Downloader is the long-running worker that watches the
// `attachments` table for pending rows (downloaded_at IS NULL),
// fetches the bytes over HTTP, and commits local_path + sha256.
//
// Lifecycle:
//
//	d := New(bundle, cacheRoot, log)
//	d.Start(ctx)
//	defer d.Shutdown()
//
// The downloader polls — there is no event channel from the
// ingester. Polling is appropriate because (a) Discord CDN URLs are
// time-limited but multi-minute, well within the poll cadence, and
// (b) the polling cost is one SELECT per cycle, which the
// single-writer SQLite handles trivially. If a future milestone
// wants tighter latency, a notification channel from
// `ingester.ingestNew` is the obvious add.
//
// Failed rows back off (M7-P3-003 fix): per row we remember
// `{attempts, lastFailedAt}` in process memory and skip the row
// until `lastFailedAt + backoff(attempts)` has elapsed. The backoff
// schedule doubles (2s, 4s, 8s, … up to 2 min) so the log doesn't
// spam every poll when a CDN URL is permanently expired. State
// resets on daemon restart, which is acceptable — first attempt
// after restart counts as attempt 1.
type Downloader struct {
	bundle    store.Bundle
	cacheRoot string
	http      *http.Client
	log       *slog.Logger

	// poll cadence + per-fetch budget (tunable for tests).
	pollInterval time.Duration
	fetchTimeout time.Duration
	maxBytes     int64

	// backoff state — per-attachment failure tracking. Guarded by mu
	// because processOnce runs from the polling goroutine but tests
	// may inspect via reset paths.
	mu     sync.Mutex
	failed map[string]*failState
	nowFn  func() time.Time // overridable for tests

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// failState is the per-row backoff bookkeeping.
type failState struct {
	attempts     int
	lastFailedAt time.Time
}

// Options configures Downloader knobs. Zero values fall back to
// production defaults.
type Options struct {
	PollInterval time.Duration // default 2s
	FetchTimeout time.Duration // default 60s
	// MaxBytes is the per-attachment size cap, in bytes. A file
	// larger than this is skipped (downloaded_at stays NULL). 0
	// means default (50 MiB — Discord's own limit is 25 MB; we go
	// slightly higher to be safe against off-by-units).
	MaxBytes   int64
	HTTPClient *http.Client
}

// New constructs a Downloader. cacheRoot is the directory the
// per-attachment subfolders live under (typically
// `<data-root>/attachments`).
func New(bundle store.Bundle, cacheRoot string, log *slog.Logger, opts Options) *Downloader {
	if log == nil {
		log = slog.Default()
	}
	d := &Downloader{
		bundle:       bundle,
		cacheRoot:    cacheRoot,
		log:          log,
		pollInterval: opts.PollInterval,
		fetchTimeout: opts.FetchTimeout,
		maxBytes:     opts.MaxBytes,
		http:         opts.HTTPClient,
		failed:       map[string]*failState{},
		nowFn:        func() time.Time { return time.Now().UTC() },
	}
	if d.pollInterval <= 0 {
		d.pollInterval = 2 * time.Second
	}
	if d.fetchTimeout <= 0 {
		d.fetchTimeout = 60 * time.Second
	}
	if d.maxBytes <= 0 {
		d.maxBytes = 50 * 1024 * 1024
	}
	if d.http == nil {
		d.http = &http.Client{Timeout: d.fetchTimeout}
	}
	return d
}

// Start launches the polling goroutine. The first poll runs
// immediately; subsequent polls run every pollInterval. Call
// Shutdown to stop the loop and wait for it to drain.
func (d *Downloader) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	d.cancel = cancel
	d.wg.Add(1)
	go d.run(ctx)
}

// Shutdown signals the downloader to stop and blocks until the
// goroutine returns. Safe to call exactly once after Start.
func (d *Downloader) Shutdown() {
	if d.cancel != nil {
		d.cancel()
	}
	d.wg.Wait()
}

func (d *Downloader) run(ctx context.Context) {
	defer d.wg.Done()
	// First tick happens immediately so any pending rows from a
	// previous daemon process get picked up at boot.
	if err := d.processOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		d.log.Warn("downloader cycle failed", "err", err.Error())
	}
	t := time.NewTicker(d.pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.processOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				d.log.Warn("downloader cycle failed", "err", err.Error())
			}
		}
	}
}

// processOnce drains the pending queue once. Each row is fetched
// independently; one failure does not stop subsequent rows. Rows in
// their backoff window are skipped silently (no log line — the
// failure was already logged when it last ran).
func (d *Downloader) processOnce(ctx context.Context) error {
	pending, err := d.bundle.Attachments.ListPendingDownloads(ctx, 50)
	if err != nil {
		return err
	}
	now := d.nowFn()
	for _, a := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.inBackoff(a.ID, now) {
			continue
		}
		if err := d.fetchOne(ctx, a); err != nil {
			d.recordFailure(a.ID, now)
			st := d.snapshotFail(a.ID)
			d.log.Warn("attachment fetch failed",
				"attachment_id", a.ID,
				"url", a.DiscordURL,
				"attempts", st.attempts,
				"next_retry_in", backoffFor(st.attempts).String(),
				"err", err.Error())
			// Continue with the next pending row. The row stays
			// downloaded_at = NULL and will be retried next cycle —
			// once the backoff window expires.
		} else {
			d.clearFailure(a.ID)
		}
	}
	return nil
}

// inBackoff reports whether the per-row backoff window for id is
// still in effect at `now`. Rows with no prior failure return false.
func (d *Downloader) inBackoff(id string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, ok := d.failed[id]
	if !ok {
		return false
	}
	wait := backoffFor(st.attempts)
	return now.Before(st.lastFailedAt.Add(wait))
}

func (d *Downloader) recordFailure(id string, now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	st, ok := d.failed[id]
	if !ok {
		st = &failState{}
		d.failed[id] = st
	}
	st.attempts++
	st.lastFailedAt = now
}

func (d *Downloader) clearFailure(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.failed, id)
}

func (d *Downloader) snapshotFail(id string) failState {
	d.mu.Lock()
	defer d.mu.Unlock()
	if st, ok := d.failed[id]; ok {
		return *st
	}
	return failState{}
}

// backoffFor returns the next retry delay given the attempt count.
// Schedule: 2s, 4s, 8s, 16s, 32s, 64s, 120s, then capped at 120s.
// Match the requirement "失败重试 + 退避" without overengineering;
// a fully expired Discord URL eventually settles at ≤30 retries per
// hour rather than 1800.
//
// M7-P3-004 fix: cap the attempt count BEFORE the shift. `1 << 100`
// on a signed `1` shifts past int's bit width and produces 0 (Go
// spec §Shift), which then evaluates `0 > cap` as false and returns
// 0 — collapsing the backoff to "retry immediately" exactly when
// the row is most likely permanently broken. Bound attempts at 7
// first; that's also the point where the schedule reaches the cap
// (2^7 s = 128 s, clamped down to 120 s).
//
// M8-Q-P1-003: renamed local `cap` to `maxBackoff` so the builtin
// `cap()` stays callable and `go vet -shadow` is happy.
func backoffFor(attempts int) time.Duration {
	if attempts <= 0 {
		return 0
	}
	const maxBackoff = 120 * time.Second
	if attempts >= 7 {
		return maxBackoff
	}
	base := time.Second * time.Duration(1<<uint(attempts))
	if base > maxBackoff {
		return maxBackoff
	}
	return base
}

// fetchOne downloads a single attachment to the local cache.
//
// Path layout: <cacheRoot>/<message-id>/<attachment-id>/<filename>
//
// We key the leaf directory on the *attachment* id (UUIDv7) rather
// than the filename to keep multi-file messages and same-name files
// disambiguated. The CLI surfaces local_path to the user, so the
// extra prefix is harmless.
func (d *Downloader) fetchOne(ctx context.Context, a *store.Attachment) error {
	if a.DiscordURL == "" {
		return errcode.New(errcode.InvalidArgument, "attachment %s has no discord_url", a.ID)
	}
	if d.maxBytes > 0 && a.Size > d.maxBytes {
		return errcode.New(errcode.InvalidArgument,
			"attachment %s exceeds max size (%d > %d)", a.ID, a.Size, d.maxBytes)
	}

	dir := filepath.Join(d.cacheRoot, a.MessageID, a.ID)
	// 0o700: keep attachments inside the same trust boundary as the
	// data-root. Defense-in-depth — the data-root is already 0o700, so
	// loosening the leaf dir would only matter if the data-root were
	// later opened up. Fix for M8-S-P2-002.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errcode.Wrap(err, errcode.Internal, "create attachment dir %s", dir)
	}
	localPath := filepath.Join(dir, safeFilename(a.Filename, a.ID))

	// Use a per-fetch timeout child context so a stuck download
	// doesn't tie up the cycle indefinitely.
	fctx, cancel := context.WithTimeout(ctx, d.fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(fctx, http.MethodGet, a.DiscordURL, nil)
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "build download request")
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return errcode.Wrap(err, errcode.Unavailable, "fetch attachment %s", a.DiscordURL)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return errcode.New(errcode.Unavailable,
			"attachment fetch returned HTTP %d", resp.StatusCode)
	}

	// Stream to a temp file in the same dir, then rename — keeps the
	// final path atomic from the reader's POV (no half-written file
	// lingering if the daemon crashes mid-download).
	tmp, err := os.CreateTemp(dir, ".dl-*")
	if err != nil {
		return errcode.Wrap(err, errcode.Internal, "create temp file")
	}
	tmpPath := tmp.Name()
	defer func() {
		// Best-effort cleanup if rename never happened.
		_ = os.Remove(tmpPath)
	}()

	limit := d.maxBytes + 1 // +1 so io.Copy actually reports oversize
	hasher := sha256.New()
	w := io.MultiWriter(tmp, hasher)
	n, copyErr := io.Copy(w, io.LimitReader(resp.Body, limit))
	// fsync the bytes to disk before rename (M8-Q-P1-004). Without
	// this, a power loss after rename but before fdatasync can leave
	// a zero-byte file at localPath and a sha256 row that doesn't
	// match — the downloader later reads back garbage and the
	// `MarkDownloaded` row masks the corruption.
	if copyErr == nil {
		if serr := tmp.Sync(); serr != nil {
			copyErr = serr
		}
	}
	if cerr := tmp.Close(); cerr != nil && copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		return errcode.Wrap(copyErr, errcode.Internal, "stream attachment body")
	}
	if n > d.maxBytes {
		return errcode.New(errcode.InvalidArgument,
			"attachment body exceeds max size cap (%d > %d)", n, d.maxBytes)
	}
	// M8-S-P2-003: verify the byte count we got matches the gateway's
	// claim. A CDN that lies about Content-Length (or a Discord-side
	// bug producing inconsistent sizes — M7 phase2 calls this out)
	// would otherwise leave us with a row whose `Size` doesn't match
	// the bytes on disk. Skip the check when the gateway didn't supply
	// a size (a.Size == 0).
	if a.Size > 0 && n != a.Size {
		return errcode.New(errcode.Unavailable,
			"attachment %s: gateway promised %d bytes, fetched %d",
			a.ID, a.Size, n)
	}

	if err := os.Rename(tmpPath, localPath); err != nil {
		return errcode.Wrap(err, errcode.Internal, "rename %s -> %s", tmpPath, localPath)
	}
	// CreateTemp's 0o600 default is preserved by Rename on POSIX, but
	// be explicit so a future code path that swaps to non-tempfile
	// can't quietly widen perms. (M8-S-P2-002 hardening pair.)
	if err := os.Chmod(localPath, 0o600); err != nil {
		return errcode.Wrap(err, errcode.Internal, "chmod %s", localPath)
	}
	sum := hex.EncodeToString(hasher.Sum(nil))
	if err := d.bundle.Attachments.MarkDownloaded(ctx, a.ID, localPath, sum, time.Now().UTC()); err != nil {
		return err
	}
	d.log.Info("attachment downloaded",
		"attachment_id", a.ID, "bytes", n, "local_path", localPath)
	return nil
}

// safeFilename sanitises a gateway-supplied filename for use as a
// leaf path component. The result is always non-empty, contains only
// `[A-Za-z0-9._-]`, has at most 80 bytes after sanitisation, and has
// no leading dot (so the file isn't hidden by accident).
//
// Anything not in the whitelist is replaced with `_`. If the result
// after stripping is empty (or pathologically short), we fall back to
// `att-<attachment-id>`. Mirrors the M8-S-P2-006 / M8-Q-P1-005 fixes
// — the prior version called `filepath.Base` which on POSIX would
// happily emit `..\\evil` (backslashes are filename bytes on Linux)
// or control characters from a malicious gateway.
func safeFilename(name, fallbackID string) string {
	if name == "" || name == "." || name == ".." {
		return "att-" + fallbackID
	}
	// Strip any directory parts the gateway might have lied about.
	base := filepath.Base(name)
	var b []byte
	for i := 0; i < len(base); i++ {
		c := base[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '.', c == '_', c == '-':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	// Strip leading dots so we don't accidentally hide the file.
	for len(b) > 0 && b[0] == '.' {
		b = b[1:]
	}
	// Cap length so a malicious 4 KB filename can't fill a directory
	// listing or trip POSIX NAME_MAX (255 on most ext4 / xfs).
	const maxLen = 80
	if len(b) > maxLen {
		b = b[:maxLen]
	}
	if len(b) == 0 {
		return "att-" + fallbackID
	}
	return string(b)
}

