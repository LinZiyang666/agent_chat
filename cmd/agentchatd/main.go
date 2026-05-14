// Command agentchatd is the long-running daemon. It holds Discord bot
// connections, owns the SQLite store, and serves CLI requests over a
// Unix-domain socket. Command wiring lives in the cmds subpackage.
package main

import (
	"github.com/LinZiyang666/agentchat/cmd/agentchatd/cmds"
	"github.com/LinZiyang666/agentchat/internal/cliutil"
)

func main() {
	cliutil.PrintAndExit(cmds.Execute())
}
