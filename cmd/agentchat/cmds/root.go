// Package cmds wires up the agentchat CLI command tree. Each
// subcommand lives in its own file and registers itself onto rootCmd
// via init(). The package also resolves the token + socket + output
// mode that every subcommand needs.
package cmds

import (
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/LinZiyang666/agentchat/pkg/client"
)

// Version is the build-time version string. Override via -ldflags:
//
//	-X github.com/LinZiyang666/agentchat/cmd/agentchat/cmds.Version=<ver>
var Version = "dev"

// flagToken / flagSocket / flagJSON are bound to persistent flags on
// rootCmd, accessible to every subcommand. (M8-C-P1-001 dropped the
// inert --no-color flag — it was registered but never consulted by
// any renderer; reintroduce only when colorized output exists.)
var (
	flagToken  string
	flagSocket string
	flagJSON   bool
)

const (
	envToken  = "AGENTCHAT_TOKEN"
	envSocket = "AGENTCHAT_SOCKET"
	envHome   = "AGENTCHAT_HOME"
)

var rootCmd = &cobra.Command{
	Use:   "agentchat",
	Short: "Command-line chat client for agents and humans (client side).",
	Long: `agentchat is the CLI that talks to a local agentchatd daemon
and lets agents (or humans) send and receive messages, manage rooms,
and watch state. Discord is the underlying transport, but the CLI
surface is platform-agnostic.

Authentication: the CLI looks for a bearer token in the following
order (highest precedence first):

  1. --token <s>           command-line flag
  2. $AGENTCHAT_TOKEN      environment variable (preferred for agents)
  3. <data-root>/cli.toml  token = "..." entry (data-root defaults to
                           ~/.agentchat; override via $AGENTCHAT_HOME)`,
	SilenceUsage:  true,
	SilenceErrors: true, // main() prints errors centrally via internal/cliutil
}

func init() {
	rootCmd.Version = Version
	rootCmd.PersistentFlags().StringVar(&flagToken, "token", "",
		"API token (overrides $AGENTCHAT_TOKEN)")
	rootCmd.PersistentFlags().StringVar(&flagSocket, "socket", "",
		"daemon Unix socket path (default <data-root>/agentchatd.sock)")
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false,
		"force JSON output even on a terminal")
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

// resolveToken returns the bearer token to use. Precedence:
//
//  1. --token <s>
//  2. $AGENTCHAT_TOKEN
//  3. <data-root>/cli.toml file with a top-level `token = "..."` line
//
// A missing or malformed cli.toml is silently treated as "no token",
// not as a fatal error — the daemon-side AUTH_MISSING / AUTH_INVALID
// path will surface the real issue.
//
// Fix for M2-P3-008: the cli.toml source is now actually implemented.
func resolveToken() string {
	if flagToken != "" {
		return flagToken
	}
	if v := os.Getenv(envToken); v != "" {
		return v
	}
	return tokenFromConfigFile(resolveDataRoot())
}

// resolveDataRoot returns the CLI's view of the data root: either
// $AGENTCHAT_HOME or, failing that, ~/.agentchat. Used to locate
// cli.toml and (indirectly via resolveSocket) the daemon socket.
func resolveDataRoot() string {
	if v := os.Getenv(envHome); v != "" {
		return v
	}
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return ".agentchat"
	}
	return filepath.Join(h, ".agentchat")
}

// cliConfig is the on-disk shape of the optional CLI config file. We
// keep it minimal in M2; future fields (default socket override, etc.)
// can be added without breaking compatibility.
type cliConfig struct {
	Token string `toml:"token"`
}

// tokenFromConfigFile reads <dataRoot>/cli.toml and returns the
// `token` field, or "" on any error (missing file, parse error). A
// malformed file does not fail the command; the daemon's auth layer
// will surface AUTH_MISSING and the user can debug from there.
func tokenFromConfigFile(dataRoot string) string {
	if dataRoot == "" {
		return ""
	}
	path := filepath.Join(dataRoot, "cli.toml")
	var cfg cliConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return ""
	}
	return cfg.Token
}

// resolveSocket returns the socket path to dial, honoring the
// precedence flag > $AGENTCHAT_SOCKET > <data-root>/agentchatd.sock.
func resolveSocket() string {
	if flagSocket != "" {
		return flagSocket
	}
	if v := os.Getenv(envSocket); v != "" {
		return v
	}
	return filepath.Join(resolveDataRoot(), "agentchatd.sock")
}

// newClient builds a *client.Client using the resolved socket and
// token. It deliberately does not validate the token's presence — that
// responsibility belongs to each subcommand, since /v1/healthz works
// without one.
func newClient() *client.Client {
	return client.New(resolveSocket(), resolveToken())
}
