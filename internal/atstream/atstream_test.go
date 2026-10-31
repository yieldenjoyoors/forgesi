package atstream

import (
	"testing"

	"github.com/yieldenjoyoors/forgesi/internal/dagcbor"
)

// Frames must decode with atproto DAG-CBOR conventions (header then body)
// and the CAR must be structurally valid.

func TestBuildCommitFrame(t *testing.T) {
	rec := map[string]any{"$type": "org.tangled.track.ticket", "state": "open", "createdAt": "2026-10-31T12:00:00.000Z"}
	frame, err := BuildCommit(CommitEvent{
		Repo: "did:web:forgesi.org:repo:demo",
		Rev:  "3mz4yjub7xc22",
		Seq:  7,
		Time: "2026-10-31T12:00:01.000Z",
	}, rec)
	if err != nil {
		t.Fatal(err)
	}
	hdrAny, rest, err := dagcbor.Decode(frame)
	if err != nil {
		t.Fatal(err)
	}
	hdr := hdrAny.(map[string]any)
	if hdr["op"] != uint64(1) || hdr["t"] != "#commit" {
		t.Fatalf("header = %v", hdr)
	}
	bodyAny, tail, err := dagcbor.Decode(rest)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 0 {
		t.Fatalf("trailing bytes after body: %d", len(tail))
	}
	b := bodyAny.(map[string]any)
	if b["did"] != "did:web:forgesi.org:repo:demo" {
		t.Errorf("did = %v", b["did"])
	}
	if b["rev"] != "3mz4yjub7xc22" || b["seq"] != uint64(7) {
		t.Errorf("rev/seq = %v/%v", b["rev"], b["seq"])
	}
	data, ok := b["data"].([]byte)
	if !ok || len(data) < 30 {
		t.Fatalf("data not a CAR buffer: %T", b["data"])
	}
	// The CAR's first varint-prefixed section is the dag-json header.
	n, adv := uvarint(data, 0)
	if adv <= 0 || int(n) > len(data)-adv {
		t.Fatalf("bad CAR header length")
	}
	headerJSON := string(data[adv : adv+int(n)])
	if !contains(headerJSON, `"version":1`) || !contains(headerJSON, b["commit"].(string)) {
		t.Errorf("CAR header = %q", headerJSON)
	}
}

func TestCARStructure(t *testing.T) {
	rec := map[string]any{"$type": "x.y.z"}
	recBytes, err := dagcbor.Encode(rec)
	if err != nil {
		t.Fatal(err)
	}
	recCID, _ := dagcbor.CIDString(recBytes)
	commit := map[string]any{"rev": "3mz4yjub7xc22", "prev": "", "data": []any{recCID}}
	commitBytes, _ := dagcbor.Encode(commit)
	commitCID, _ := dagcbor.CIDString(commitBytes)
	car, err := BuildCAR([]string{commitCID}, map[string][]byte{commitCID: commitBytes, recCID: recBytes})
	if err != nil {
		t.Fatal(err)
	}
	n, adv := uvarint(car, 0)
	rest := car[adv+int(n):]
	n2, adv2 := uvarint(rest, 0)
	entry := rest[adv2 : adv2+int(n2)]
	// cid bytes: version(1) + codec(0x71) + multihash(0x12 0x20 + 32) = 36
	if len(entry) < 37 {
		t.Fatalf("entry too short: %d", len(entry))
	}
	blockBytes := entry[36:]
	want, _ := dagcbor.Encode(map[string]any{"rev": "3mz4yjub7xc22", "prev": "", "data": []any{recCID}})
	if string(blockBytes) != string(want) {
		t.Errorf("commit block drifted")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func uvarint(b []byte, at int) (uint64, int) {
	var x uint64
	var s uint
	for i := at; i < len(b); i++ {
		c := b[i]
		if c < 0x80 {
			return x | uint64(c)<<s, i + 1 - at
		}
		x |= uint64(c&0x7f) << s
		s += 7
	}
	return 0, 0
}
