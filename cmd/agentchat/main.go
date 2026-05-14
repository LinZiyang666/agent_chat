// Command agentchat is the short-lived CLI client that agents (and
// humans) use to interact with a local agentchatd daemon. The command
// tree is declared in the cmds subpackage; this file is intentionally
// minimal.
package main

import (
	"github.com/LinZiyang666/agentchat/cmd/agentchat/cmds"
	"github.com/LinZiyang666/agentchat/internal/cliutil"
)

func main() {
	cliutil.PrintAndExit(cmds.Execute())
}
