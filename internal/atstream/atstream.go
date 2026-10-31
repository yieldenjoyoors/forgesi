// Package atstream builds atproto event-stream frames (#commit with CAR
// data) for the knot firehose: com.atproto.sync.subscribeRepos.
package atstream

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/yieldenjoyoors/forgesi/internal/dagcbor"
)

// Frame is one firehose message: a dag-cbor header followed by a dag-cbor
// body (the atproto event stream format).
type Frame struct {
	Op   int    // 1=#commit, 2=#identity, 3=#account, -1=#info
	Type string // "#commit" ...
	Body map[string]any
}

// Encode renders header+body concatenated (indefinite stream: consumers
// read header then body with a shared decoder).
func (f Frame) Encode() ([]byte, error) {
	header, err := dagcbor.Encode(map[string]any{"op": f.Op, "t": f.Type})
	if err != nil {
		return nil, err
	}
	body, err := dagcbor.Encode(f.Body)
	if err != nil {
		return nil, err
	}
	return append(header, body...), nil
}

// CommitEvent is the #commit body the knot emits for every record write.
type CommitEvent struct {
	Repo   string // repo DID
	Commit string // CID of the synthetic commit block (a CAR root)
	Rev    string // TID
	Prev   string // previous rev ('' for the first)
	Blocks []byte // CAR v1 bytes: root = the commit block, leaves = records
	Seq    int64
	Time   string // RFC3339
}

// BuildCommit produces the frame bytes: a CAR whose root is a synthetic
// commit block {rev, prev, data: [record cids]} and whose leaves are the
// record's dag-cbor block.
func BuildCommit(e CommitEvent, record map[string]any) ([]byte, error) {
	recBytes, err := dagcbor.Encode(record)
	if err != nil {
		return nil, err
	}
	recCID, err := dagcbor.CIDString(recBytes)
	if err != nil {
		return nil, err
	}
	data := []any{recCID}
	commitBlock := map[string]any{"rev": e.Rev, "prev": e.Prev, "data": data}
	commitBytes, err := dagcbor.Encode(commitBlock)
	if err != nil {
		return nil, err
	}
	commitCID, err := dagcbor.CIDString(commitBytes)
	if err != nil {
		return nil, err
	}
	car, err := BuildCAR([]string{commitCID}, map[string][]byte{
		commitCID: commitBytes,
		recCID:    recBytes,
	})
	if err != nil {
		return nil, err
	}
	f := Frame{Op: 1, Type: "#commit", Body: map[string]any{
		"did":    e.Repo,
		"commit": commitCID,
		"rev":    e.Rev,
		"prev":   e.Prev,
		"data":   car,
		"seq":    e.Seq,
		"time":   e.Time,
	}}
	return f.Encode()
}

// BuildCAR writes a CARv1: varint-length dag-json header then
// varint-prefixed (cid, block) pairs in the given root order plus map order.
func BuildCAR(roots []string, blocks map[string][]byte) ([]byte, error) {
	headerObj := map[string]any{"version": 1}
	if len(roots) > 0 {
		// dag-json links: {"/": "<cid>"} — dagcbor.Encode treats plain
		// strings, so emit the link shape manually via the map.
		links := make([]any, 0, len(roots))
		for _, r := range roots {
			links = append(links, map[string]any{"/": r})
		}
		headerObj["roots"] = links
	}
	headerJSON := fmt.Sprintf(`{"roots":[%s],"version":1}`, joinQuoted(roots))
	_ = headerObj
	var out bytes.Buffer
	// header: varint(len) + utf-8 dag-json
	writeVarintPrefixed(&out, []byte(headerJSON))
	// blocks: deterministic order = roots first, then the rest sorted
	emitted := map[string]bool{}
	emit := func(cid string) {
		if emitted[cid] {
			return
		}
		emitted[cid] = true
		block := blocks[cid]
		entry := append([]byte{}, decodeCID(cid)...)
		entry = append(entry, block...)
		writeVarintPrefixed(&out, entry)
	}
	for _, r := range roots {
		emit(r)
	}
	// remaining blocks, sorted by cid for determinism
	rest := make([]string, 0, len(blocks))
	for cid := range blocks {
		if !emitted[cid] {
			rest = append(rest, cid)
		}
	}
	for i := 0; i < len(rest); i++ {
		for j := i + 1; j < len(rest); j++ {
			if rest[j] < rest[i] {
				rest[i], rest[j] = rest[j], rest[i]
			}
		}
	}
	for _, cid := range rest {
		emit(cid)
	}
	return out.Bytes(), nil
}

func joinQuoted(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ","
		}
		out += `"` + s + `"`
	}
	return out
}

func writeVarintPrefixed(out *bytes.Buffer, payload []byte) {
	varint := make([]byte, binary.MaxVarintLen64)
	n := binary.PutUvarint(varint, uint64(len(payload)))
	out.Write(varint[:n])
	out.Write(payload)
}

// decodeCID converts a base32 multibase cid string to raw cid bytes.
func decodeCID(s string) []byte {
	if len(s) == 0 || s[0] != 'b' {
		return nil
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	var bitBuf uint64
	var bits uint
	var out []byte
	for _, c := range s[1:] {
		idx := -1
		for i := 0; i < len(alphabet); i++ {
			if alphabet[i] == byte(c) {
				idx = i
				break
			}
		}
		if idx < 0 {
			return out
		}
		bitBuf = bitBuf<<5 | uint64(idx)
		bits += 5
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(bitBuf>>bits))
		}
	}
	return out
}
