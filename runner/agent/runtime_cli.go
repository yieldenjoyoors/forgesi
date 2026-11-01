//go:build !js

package agent

import "os"

func init() {
	getEnv = os.Getenv
}
