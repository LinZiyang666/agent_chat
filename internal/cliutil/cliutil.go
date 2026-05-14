// Package cliutil holds tiny helpers shared by both binaries
// (agentchat and agentchatd). Today: structured error printing for
// main() exit paths, and TTY detection.
package cliutil

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/mattn/go-isatty"

	"github.com/LinZiyang666/agentchat/internal/errcode"
)

// PrintError writes err to w in a stable CLI-friendly format. When err
// carries an errcode.Error, both the Code and Message are printed,
// followed by Details (if any) and the wrapped cause (if any). Plain
// errors print only their message.
//
// The wrapped-cause line was added as part of the M2-P3-011 follow-up:
// without it, diagnostic information such as the underlying OS error
// (e.g. "operation not permitted on Unix socket bind") was hidden
// behind the structured code, making sandbox/perm issues hard to
// debug.
//
// Format example:
//
//	Error [INTERNAL]: listen on /path/agentchatd.sock
//	Caused by: bind: operation not permitted
func PrintError(w io.Writer, err error) {
	if err == nil {
		return
	}
	if e, ok := errcode.As(err); ok {
		fmt.Fprintf(w, "Error [%s]: %s\n", e.Code, e.Message)
		if len(e.Details) > 0 {
			fmt.Fprintln(w, "Details:")
			for k, v := range e.Details {
				fmt.Fprintf(w, "  %s: %v\n", k, v)
			}
		}
		if cause := errors.Unwrap(e); cause != nil {
			fmt.Fprintf(w, "Caused by: %v\n", cause)
		}
		return
	}
	fmt.Fprintf(w, "Error: %v\n", err)
}

// PrintAndExit calls PrintError on os.Stderr and exits with the code
// mapped by errcode.ExitCode. Designed to be the only os.Exit call in
// each binary's main().
func PrintAndExit(err error) {
	if err == nil {
		os.Exit(0)
	}
	PrintError(os.Stderr, err)
	os.Exit(errcode.ExitCode(err))
}

// IsTerminal reports whether the given file descriptor is connected to
// a terminal. Used by the CLI to switch between human-readable and
// machine-readable output. Falls through go-isatty (cross-platform).
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	return isatty.IsTerminal(f.Fd())
}
