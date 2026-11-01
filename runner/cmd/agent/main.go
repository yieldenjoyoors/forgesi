// Command agent is the ForgeSI fleet runner CLI: pull a queued run and
// execute it (dev harness; the deployed runner is the wasm worker).
package main

import (
	"os"

	agent "github.com/yieldenjoyoors/forgesi/runner/agent"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		agent.Serve()
		return
	}
	agent.Pull()
}
