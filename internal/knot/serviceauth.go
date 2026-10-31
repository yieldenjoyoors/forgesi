package knot

import (
	"net/http"
	"strings"

	"github.com/yieldenjoyoors/forgesi/internal/atauth"
)

// ServiceAuth verifies real atproto service-auth JWTs (ES256K, iss=PDS DID,
// aud=the record's repo DID or the knot DID). This is the production
// authorizer: what the Tangled web app and agent identities speak.
type ServiceAuth struct {
	V *atauth.Verifier
}

// Authorize implements Authorizer. aud is the audience the ROUTE expects:
// repo management passes the knot DID, record writes the repo DID.
func (s ServiceAuth) Authorize(r *http.Request, aud string) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	did, err := s.V.Verify(strings.TrimPrefix(h, "Bearer "), aud, "")
	if err != nil {
		return ""
	}
	return did
}
