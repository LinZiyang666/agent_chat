package cmds

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// dataRootLockName is the filename inside DataRoot used for the
// single-instance exclusive lock. Distinct from the socket so that
// stale-socket cleanup never races against lock acquisition.
const dataRootLockName = "agentchatd.lock"

// dataRootLock is the value returned by acquireDataRootLock. The
// caller MUST hold the file open for the daemon's lifetime; closing
// the *os.File releases the underlying flock automatically (POSIX
// drops file locks when the FD is closed or the process exits).
type dataRootLock struct {
	f *os.File
}

// Release closes the lock file, dropping the lock.
func (l *dataRootLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Close()
}

// acquireDataRootLock takes an exclusive non-blocking flock on
// <DataRoot>/agentchatd.lock. A Conflict error is returned when
// another agentchatd already holds it — closing the second daemon
// before any state is touched.
//
// Fix for M2-P3-001: previously two daemons could attach to the same
// data root simultaneously, with undefined ownership over the socket
// and SQLite WAL files.
func acquireDataRootLock(path string) (*dataRootLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errcode.Wrap(err, errcode.Internal, "open lock file %s", path)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errcode.New(errcode.Conflict,
				"another agentchatd is running with this data root (lock file %s)", path)
		}
		return nil, errcode.Wrap(err, errcode.Internal, "flock lock file %s", path)
	}
	// Record our PID for operators who run `cat agentchatd.lock`.
	if err := f.Truncate(0); err == nil {
		_, _ = f.Seek(0, 0)
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return &dataRootLock{f: f}, nil
}
