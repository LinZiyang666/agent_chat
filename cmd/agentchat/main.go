// Command agentchat is the short-lived CLI client that agents (and humans)
// use to interact with a local agentchatd daemon. The command tree is
// declared in the cmds subpackage; this file is intentionally minimal.
package main

import (
	"fmt"
	"os"

	"github.com/LinZiyang666/agentchat/cmd/agentchat/cmds"
)

func main() {
	if err := cmds.Execute(); err != nil {
		// Cobra prints the error itself when SilenceErrors is false, but
		// we still surface a non-zero exit so scripts can branch on it.
		fmt.Fprintln(os.Stderr)
		os.Exit(1)
	}
}
