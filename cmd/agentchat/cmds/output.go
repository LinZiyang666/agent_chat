package cmds

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/LinZiyang666/agentchat/internal/cliutil"
)

// outputMode decides whether to write JSON or a human-readable view.
// JSON is forced by --json or by a non-tty stdout (so a pipe to jq
// works without flags).
func outputJSON() bool {
	if flagJSON {
		return true
	}
	return !cliutil.IsTerminal(os.Stdout)
}

// writeJSON marshals v as indented JSON to stdout.
func writeJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// table writes a simple tab-aligned table to stdout. headers is the
// column titles; rows is one slice per row. Values are stringified
// with fmt.Sprint.
func table(headers []string, rows [][]any) error {
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, strings.Join(headers, "\t")); err != nil {
		return err
	}
	for _, r := range rows {
		fields := make([]string, len(r))
		for i, v := range r {
			fields[i] = fmt.Sprint(v)
		}
		if _, err := fmt.Fprintln(tw, strings.Join(fields, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// renderTime formats t for a human table column. UTC, second precision.
func renderTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

// renderTimePtr is like renderTime but treats nil as "-".
func renderTimePtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return renderTime(*t)
}

// stderrLine is a tiny helper for one-line stderr notices that should
// not interfere with stdout (which the caller may be parsing).
func stderrLine(w io.Writer, msg string) {
	if w == nil {
		w = os.Stderr
	}
	_, _ = fmt.Fprintln(w, msg)
}
