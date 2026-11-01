//go:build !js

package knot

// Native build (tests, CLI): background goroutines just run.
var waitUntilHook = func(f func()) {
	go f()
}
