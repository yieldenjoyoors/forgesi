package dagcbor

import (
	"encoding/hex"
	"testing"
)

// Vectors generated with the reference @ipld/dag-cbor + multiformats JS
// (scratch/cbor-vec). Our encoding must be byte-identical — CIDs flow into
// strongRefs and swapRecord guards, so any drift breaks interop.
func TestEncodeTicketVector(t *testing.T) {
	rec := map[string]any{
		"$type":     "org.tangled.track.ticket",
		"title":     map[string]any{"$type": "org.tangled.markup.markdown#inline", "text": "fix the login loop"},
		"state":     "open",
		"createdAt": "2026-10-31T12:00:00.000Z",
	}
	b, err := Encode(rec)
	if err != nil {
		t.Fatal(err)
	}
	want := "a465247479706578186f72672e74616e676c65642e747261636b2e7469636b6574657374617465646f70656e657469746c65a264746578747266697820746865206c6f67696e206c6f6f7065247479706578226f72672e74616e676c65642e6d61726b75702e6d61726b646f776e23696e6c696e65696372656174656441747818323032362d31302d33315431323a30303a30302e3030305a"
	if got := hex.EncodeToString(b); got != want {
		t.Errorf("ticket bytes mismatch:\n got %s\nwant %s", got, want)
	}
	cid, err := CIDString(b)
	if err != nil {
		t.Fatal(err)
	}
	if cid != "bafyreihvobkx3wdfi4xfcmq6mw4xljuqxawd5q5qy4kvudcz4vyyj6diti" {
		t.Errorf("cid = %s", cid)
	}
}

func TestEncodeKeyOrderingVector(t *testing.T) {
	rec := map[string]any{
		"$type": "x",
		"b":     int64(1),
		"aa":    int64(2),
		"a":     int64(3),
		"":      int64(4),
		"z":     []any{int64(1), "two", true, nil},
		"n":     map[string]any{"deep": map[string]any{"deeper": 3.5}},
	}
	b, err := Encode(rec)
	if err != nil {
		t.Fatal(err)
	}
	want := "a76004616103616201616ea16464656570a166646565706572fb400c000000000000617a84016374776ff5f6626161026524747970656178"
	if got := hex.EncodeToString(b); got != want {
		t.Errorf("ordering bytes mismatch:\n got %s\nwant %s", got, want)
	}
	cid, err := CIDString(b)
	if err != nil {
		t.Fatal(err)
	}
	if cid != "bafyreiadrhgmblgb26lpwycefkqgvxtly5xjstxsajqcgyrurqp5rcuteq" {
		t.Errorf("cid = %s", cid)
	}
}
