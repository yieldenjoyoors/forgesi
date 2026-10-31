// Package atauth verifies atproto service-auth tokens: compact ES256K JWTs
// minted by a PDS via com.atproto.server.getServiceAuth. iss is the caller's
// DID, aud the intended audience (repo DID for record writes, knot DID for
// repo management), lxm an optional method scope.
package atauth

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	btcecsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// DIDDoc is the minimal DID document shape verification needs.
type DIDDoc struct {
	Keys []DIDKey
}

// DIDKey is one verificationMethod entry.
type DIDKey struct {
	ID        string
	Type      string
	Multibase string // "z..." base58btc multibase, compressed secp256k1
}

// Verifier checks service-auth JWTs. ResolveDID supplies DID documents
// (did:web and did:plc fetchers live in the caller — wasm or otherwise).
type Verifier struct {
	ResolveDID func(did string) (*DIDDoc, error)
	Now        func() time.Time
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type jwtClaims struct {
	Iss string `json:"iss"`
	Aud string `json:"aud"`
	Exp int64  `json:"exp"`
	Lxm string `json:"lxm"`
}

// Verify checks the bearer token for the given audience and (optional)
// method scope, returning the issuer DID.
func (v Verifier) Verify(token, wantAud, wantLxm string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("atauth: not a compact jws")
	}
	var hdr jwtHeader
	if err := json.Unmarshal(mustB64(parts[0]), &hdr); err != nil {
		return "", fmt.Errorf("atauth: bad header: %w", err)
	}
	if hdr.Alg != "ES256K" {
		return "", fmt.Errorf("atauth: alg %q not allowed", hdr.Alg)
	}
	var cl jwtClaims
	if err := json.Unmarshal(mustB64(parts[1]), &cl); err != nil {
		return "", fmt.Errorf("atauth: bad claims: %w", err)
	}
	now := v.now()
	if cl.Exp > 0 && now.Unix() > cl.Exp {
		return "", fmt.Errorf("atauth: token expired")
	}
	if cl.Iss == "" || cl.Aud != wantAud {
		return "", fmt.Errorf("atauth: audience mismatch")
	}
	if wantLxm != "" && cl.Lxm != "" && cl.Lxm != wantLxm {
		return "", fmt.Errorf("atauth: lxm mismatch")
	}
	doc, err := v.ResolveDID(cl.Iss)
	if err != nil {
		return "", fmt.Errorf("atauth: resolve %s: %w", cl.Iss, err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return "", fmt.Errorf("atauth: bad signature encoding")
	}
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	for _, key := range doc.Keys {
		if key.Type != "" && key.Type != "EcdsaSecp256k1VerificationKey2019" {
			continue
		}
		raw, err := decodeMultibaseKey(key.Multibase)
		if err != nil {
			continue
		}
		pub, err := btcec.ParsePubKey(raw)
		if err != nil {
			continue
		}
		if ecdsa.Verify(pub.ToECDSA(), digest[:], r, s) {
			return cl.Iss, nil
		}
	}
	return "", fmt.Errorf("atauth: no key in did doc signed this token")
}

func (v Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func mustB64(s string) []byte {
	b, _ := base64.RawURLEncoding.DecodeString(s)
	return b
}

func decodeMultibaseKey(mb string) ([]byte, error) {
	if mb == "" || mb[0] != 'z' {
		return nil, fmt.Errorf("atauth: unsupported multibase %q", first(mb, 1))
	}
	return base58Decode(mb[1:])
}

func first(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

const b58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// Base58Encode encodes with the bitcoin base58 alphabet (exposed for
// tests and key minting).
func Base58Encode(b []byte) string {
	var out []byte
	zeros := 0
	for zeros < len(b) && b[zeros] == 0 {
		zeros++
	}
	n := new(big.Int).SetBytes(b)
	base := big.NewInt(58)
	mod := new(big.Int)
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		out = append(out, b58Alphabet[mod.Int64()])
	}
	for i := 0; i < zeros; i++ {
		out = append(out, '1')
	}
	// reverse
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

func base58Decode(s string) ([]byte, error) {
	n := new(big.Int)
	base := big.NewInt(58)
	for i := 0; i < len(s); i++ {
		idx := strings.IndexByte(b58Alphabet, s[i])
		if idx < 0 {
			return nil, fmt.Errorf("atauth: bad base58 char %q", s[i])
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(idx)))
	}
	body := n.Bytes()
	zeros := 0
	for zeros < len(s) && s[zeros] == '1' {
		zeros++
	}
	return append(make([]byte, zeros), body...), nil
}

// didDocJSON mirrors the DID document fields verification needs.
type didDocJSON struct {
	VerificationMethod []struct {
		ID                 string `json:"id"`
		Type               string `json:"type"`
		PublicKeyMultibase string `json:"publicKeyMultibase"`
	} `json:"verificationMethod"`
}

// ParseDIDDoc extracts verification keys from a DID document body.
func ParseDIDDoc(body []byte) (*DIDDoc, error) {
	var doc didDocJSON
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	out := &DIDDoc{}
	for _, m := range doc.VerificationMethod {
		out.Keys = append(out.Keys, DIDKey{ID: m.ID, Type: m.Type, Multibase: m.PublicKeyMultibase})
	}
	return out, nil
}

// Mint signs a service-auth JWT with the given secp256k1 private key —
// the knot mints short-lived push tokens for the repos it hosts
// (org.tangled.repo.mintToken): iss = repo DID, aud = knot DID,
// lxm = the git push scope.
func Mint(priv *btcec.PrivateKey, iss, aud, lxm string, ttl time.Duration, now time.Time) (string, error) {
	header := `{"typ":"JWT","alg":"ES256K"}`
	claims := fmt.Sprintf(`{"iss":%q,"aud":%q,"exp":%d,"lxm":%q}`, iss, aud, now.Add(ttl).Unix(), lxm)
	signing := b64url([]byte(header)) + "." + b64url([]byte(claims))
	digest := sha256.Sum256([]byte(signing))
	sig := btcecsa.Sign(priv, digest[:])
	var raw [64]byte
	r, s := sig.R(), sig.S()
	rb, sb := r.Bytes(), s.Bytes()
	copy(raw[:32], rb[:])
	copy(raw[32:], sb[:])
	return signing + "." + b64url(raw[:]), nil
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
