package cmds

import (
	"bytes"
	"strings"
	"testing"
)

func executeRootForTest(t *testing.T, args ...string) string {
	t.Helper()

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("root command returned error: %v", err)
	}

	return strings.TrimSpace(buf.String())
}

// TestVersionCommandPrintsVersion verifies that `agentchatd version`
// writes the package-level Version string to stdout.
func TestVersionCommandPrintsVersion(t *testing.T) {
	got := executeRootForTest(t, "version")
	want := "agentchatd " + Version
	if got != want {
		t.Errorf("version output mismatch:\n  got:  %q\n  want: %q", got, want)
	}
}

// TestRootVersionFlagPrintsVersion verifies cobra's built-in --version
// output, which is part of the M1 smoke-test contract.
func TestRootVersionFlagPrintsVersion(t *testing.T) {
	got := executeRootForTest(t, "--version")
	want := "agentchatd version " + Version
	if got != want {
		t.Errorf("--version output mismatch:\n  got:  %q\n  want: %q", got, want)
	}
}

// TestRootCommandHasVersionField guards against an accidental drop of the
// rootCmd.Version assignment, which would silently disable `--version`.
func TestRootCommandHasVersionField(t *testing.T) {
	if rootCmd.Version == "" {
		t.Fatal("rootCmd.Version is empty; --version flag would be disabled")
	}
}
