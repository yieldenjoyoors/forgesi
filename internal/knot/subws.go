package knot

// subscribeRepos serves the atproto firehose over WebSocket. The wasm build
// upgrades and streams; other builds (tests) get a 501 stub so the route
// table stays identical everywhere.
func (k *Knot) wsRoutes() {
	k.mux.HandleFunc("GET /xrpc/com.atproto.sync.subscribeRepos", k.handleSubscribeRepos)
}
