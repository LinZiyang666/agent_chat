// Command agentchatd is the long-running daemon. It holds Discord bot
// connections, owns the SQLite store, and serves CLI requests over a
// Unix-domain socket. Like the CLI binary, command wiring lives in the
// cmds subpackage; this file is intentionally minimal.
package main

import (
	"fmt"
	"os"

	"github.com/LinZiyang666/agentchat/cmd/agentchatd/cmds"
)

func main() {
	if err := cmds.Execute(); err != nil {
		fmt.Fprintln(os.Stderr)
		os.Exit(1)
	}
}
