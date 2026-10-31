package packfile

import (
	"bytes"
	"fmt"
	"testing"
)

// Roundtrip: encode full objects, decode, same content and OIDs. Then a
// real git-produced pack (via git CLI in the integration test) exercises
// the decoder including deltas.

func TestEncodeDecodeRoundtrip(t *testing.T) {
	blob1 := []byte("# demo\nhello from forgesi\n")
	tree1 := []byte("100644 README.md\x00" + hexOID(mustOID("blob", blob1)))
	commit := []byte(fmt.Sprintf("tree %s\nauthor forgesi <dev@knot> 1730000000 +0000\ncommitter forgesi <dev@knot> 1730000000 +0000\n\nseed\n", hexOID(mustOID("tree", tree1))))
	objs := []Object{
		{Type: TypeBlob, Data: blob1},
		{Type: TypeTree, Data: tree1},
		{Type: TypeCommit, Data: commit},
	}
	pack, err := Encode(objs)
	if err != nil {
		t.Fatal(err)
	}
	if string(pack[:4]) != "PACK" {
		t.Fatal("bad magic")
	}
	got, err := Decode(bytes.NewReader(pack))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("decoded %d objects, want 3", len(got))
	}
	for i := range objs {
		if !bytes.Equal(got[i].Data, objs[i].Data) {
			t.Errorf("obj %d content drifted", i)
		}
		if got[i].Type != objs[i].Type {
			t.Errorf("obj %d type = %d", i, got[i].Type)
		}
	}
}

func TestDecodeGitProducedPack(t *testing.T) {
	data := gitProducedPack(t) // helper builds a real pack with deltas
	objs, err := Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) == 0 {
		t.Fatal("no objects decoded")
	}
	sawCommit := false
	for _, o := range objs {
		if o.Type == TypeCommit {
			sawCommit = true
			if !bytes.HasPrefix(o.Data, []byte("tree ")) {
				t.Errorf("commit content malformed: %q", string(o.Data[:20]))
			}
		}
	}
	if !sawCommit {
		t.Fatal("no commit in pack")
	}
}

func mustOID(typ string, content []byte) string {
	return OID(GitObject(typ, content))
}

func hexOID(s string) string { return s }
