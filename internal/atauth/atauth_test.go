package atauth

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	becdsa "github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// atproto service auth: ES256K JWTs minted by a PDS
// (com.atproto.server.getServiceAuth), verified by the audience. iss is the
// caller's DID, aud is who the token is for (the repo DID for record
// writes, the knot DID for repo management), lxm optionally scopes to one
// method.

type fixture struct {
	did, keyMultibase string
	priv              *btcec.PrivateKey
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.PubKey().SerializeCompressed() // 33 bytes
	return fixture{
		did:          "did:plc:issuer",
		keyMultibase: "z" + Base58Encode(pub),
		priv:         priv,
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func mint(t *testing.T, f fixture, claims string, headerAlg string) string {
	t.Helper()
	header := `{"typ":"JWT","alg":"` + headerAlg + `"}`
	signing := b64([]byte(header)) + "." + b64([]byte(claims))
	digest := sha256.Sum256([]byte(signing))
	sig := becdsa.Sign(f.priv, digest[:])
	r, ss := sig.R(), sig.S()
	var raw [64]byte
	rb, sb := r.Bytes(), ss.Bytes() // value receivers can't call pointer Bytes(); take addresses
	copy(raw[:32], rb[:])
	copy(raw[32:], sb[:]) // atproto ES256K: 64-byte r||s
	return signing + "." + b64(raw[:])
}

func claimsFor(f fixture, aud string, exp time.Time, lxm string) string {
	lxmPart := ""
	if lxm != "" {
		lxmPart = `,"lxm":"` + lxm + `"`
	}
	return fmt.Sprintf(`{"iss":"%s","aud":"%s","exp":%d%s}`, f.did, aud, exp.Unix(), lxmPart)
}

func staticResolver(f fixture) func(string) (*DIDDoc, error) {
	return func(did string) (*DIDDoc, error) {
		return &DIDDoc{Keys: []DIDKey{{ID: did + "#key", Type: "EcdsaSecp256k1VerificationKey2019", Multibase: f.keyMultibase}}}, nil
	}
}

func TestVerifyHappyPath(t *testing.T) {
	f := newFixture(t)
	token := mint(t, f, claimsFor(f, "did:web:forgesi.org:repo:demo", time.Now().Add(time.Minute), "com.atproto.repo.createRecord"), "ES256K")
	v := Verifier{ResolveDID: staticResolver(f), Now: time.Now}
	got, err := v.Verify(token, "did:web:forgesi.org:repo:demo", "com.atproto.repo.createRecord")
	if err != nil {
		t.Fatal(err)
	}
	if got != f.did {
		t.Errorf("did = %s", got)
	}
}

func TestVerifyWrongAudience(t *testing.T) {
	f := newFixture(t)
	v := Verifier{ResolveDID: staticResolver(f), Now: time.Now}
	token := mint(t, f, claimsFor(f, "did:web:other", time.Now().Add(time.Minute), ""), "ES256K")
	if _, err := v.Verify(token, "did:web:forgesi.org:repo:demo", ""); err == nil {
		t.Fatal("wrong aud accepted")
	}
}

func TestVerifyExpired(t *testing.T) {
	f := newFixture(t)
	v := Verifier{ResolveDID: staticResolver(f), Now: time.Now}
	token := mint(t, f, claimsFor(f, "did:web:x", time.Now().Add(-time.Minute), ""), "ES256K")
	if _, err := v.Verify(token, "did:web:x", ""); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestVerifyTampered(t *testing.T) {
	f := newFixture(t)
	v := Verifier{ResolveDID: staticResolver(f), Now: time.Now}
	tok := mint(t, f, claimsFor(f, "did:web:x", time.Now().Add(time.Minute), ""), "ES256K")
	tampered := tok[:len(tok)-6] + "AAAAAA"
	if _, err := v.Verify(tampered, "did:web:x", ""); err == nil {
		t.Fatal("tampered signature accepted")
	}
}

func TestVerifyWrongKey(t *testing.T) {
	f, other := newFixture(t), newFixture(t)
	v := Verifier{ResolveDID: staticResolver(other), Now: time.Now} // doc has a DIFFERENT key
	token := mint(t, f, claimsFor(f, "did:web:x", time.Now().Add(time.Minute), ""), "ES256K")
	if _, err := v.Verify(token, "did:web:x", ""); err == nil {
		t.Fatal("signature by unknown key accepted")
	}
}

func TestVerifyLxmMismatch(t *testing.T) {
	f := newFixture(t)
	v := Verifier{ResolveDID: staticResolver(f), Now: time.Now}
	token := mint(t, f, claimsFor(f, "did:web:x", time.Now().Add(time.Minute), "com.atproto.repo.deleteRecord"), "ES256K")
	if _, err := v.Verify(token, "did:web:x", "com.atproto.repo.createRecord"); err == nil {
		t.Fatal("mismatched lxm accepted")
	}
}

func TestVerifyRejectsAlgNone(t *testing.T) {
	f := newFixture(t)
	v := Verifier{ResolveDID: staticResolver(f), Now: time.Now}
	token := mint(t, f, claimsFor(f, "did:web:x", time.Now().Add(time.Minute), ""), "none")
	if _, err := v.Verify(token, "did:web:x", ""); err == nil {
		t.Fatal("alg none accepted")
	}
}

func TestBase58Roundtrip(t *testing.T) {
	f := newFixture(t)
	raw, err := base58Decode(f.keyMultibase[1:])
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 33 {
		t.Fatalf("decoded key len = %d, want 33 (compressed secp256k1)", len(raw))
	}
	if "z"+Base58Encode(raw) != f.keyMultibase {
		t.Error("base58 roundtrip drifted")
	}
}

// A minted token must verify with the matching key and claims — the
// round-trip the agent fleet's push flow depends on.
func TestMintVerifyRoundtrip(t *testing.T) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	multibase := "z" + Base58Encode(priv.PubKey().SerializeCompressed())
	now := time.Now()
	token, err := Mint(priv, "did:web:forgesi.org:repo:demo", "did:web:forgesi.org",
		"org.tangled.git.receivePack", time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	v := Verifier{ResolveDID: func(did string) (*DIDDoc, error) {
		return &DIDDoc{Keys: []DIDKey{{ID: did + "#k", Type: "EcdsaSecp256k1VerificationKey2019", Multibase: multibase}}}, nil
	}, Now: func() time.Time { return now }}
	got, err := v.Verify(token, "did:web:forgesi.org", "org.tangled.git.receivePack")
	if err != nil {
		t.Fatal(err)
	}
	if got != "did:web:forgesi.org:repo:demo" {
		t.Errorf("iss = %s", got)
	}
}
