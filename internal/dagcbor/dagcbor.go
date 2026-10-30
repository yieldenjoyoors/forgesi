// Package dagcbor implements the small slice of DAG-CBOR that atproto
// records need: canonical encoding of JSON-shaped values (maps, arrays,
// strings, ints, floats, bools, null) and CIDv1 (dag-cbor codec, sha2-256)
// strings. Byte-identical to the reference IPLD implementations — vectors in
// dagcbor_test.go are generated with @ipld/dag-cbor.
package dagcbor

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
)

// Encode canonically encodes v. Supported: nil, bool, string, integers,
// float64, []any, map[string]any (keys sorted length-first, then bytewise —
// RFC 7049 §3.9 canonical form, which DAG-CBOR mandates). JSON-decoded
// numbers arrive as float64 and are narrowed to integers when integral.
func Encode(v any) ([]byte, error) {
	var out []byte
	if err := encode(&out, v); err != nil {
		return nil, err
	}
	return out, nil
}

func encode(out *[]byte, v any) error {
	switch x := v.(type) {
	case nil:
		*out = append(*out, 0xf6)
	case bool:
		if x {
			*out = append(*out, 0xf5)
		} else {
			*out = append(*out, 0xf4)
		}
	case string:
		encodeHead(out, 0x60, uint64(len(x)))
		*out = append(*out, x...)
	case int:
		encodeInt(out, int64(x))
	case int64:
		encodeInt(out, x)
	case uint64:
		encodeHead(out, 0x00, x)
	case float64:
		// Integral values narrow to CBOR integers (matching the JS
		// reference: 3 stays 0x03, only true fractions go float64).
		if x == math.Trunc(x) && !math.IsInf(x, 0) &&
			x >= -9.007199254740992e15 && x <= 9.007199254740992e15 {
			encodeInt(out, int64(x))
			return nil
		}
		// DAG-CBOR always encodes floats as 8-byte big-endian.
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], math.Float64bits(x))
		*out = append(*out, 0xfb)
		*out = append(*out, b[:]...)
	case []any:
		encodeHead(out, 0x80, uint64(len(x)))
		for _, item := range x {
			if err := encode(out, item); err != nil {
				return err
			}
		}
	case map[string]any:
		encodeHead(out, 0xa0, uint64(len(x)))
		for _, k := range sortedKeys(x) {
			encodeHead(out, 0x60, uint64(len(k)))
			*out = append(*out, k...)
			if err := encode(out, x[k]); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("dagcbor: unsupported type %T", v)
	}
	return nil
}

// sortedKeys orders map keys length-first, then bytewise (canonical CBOR).
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i]) != len(keys[j]) {
			return len(keys[i]) < len(keys[j])
		}
		return keys[i] < keys[j]
	})
	return keys
}

func encodeInt(out *[]byte, n int64) {
	switch {
	case n >= 0:
		encodeHead(out, 0x00, uint64(n))
	default:
		encodeHead(out, 0x20, uint64(-1-n))
	}
}

// encodeHead writes a CBOR type/length head with minimal-width argument
// encoding (uint < 24 inline, then u8/u16/u32/u64).
func encodeHead(out *[]byte, major byte, n uint64) {
	switch {
	case n < 24:
		*out = append(*out, major|byte(n))
	case n <= 0xff:
		*out = append(*out, major|24, byte(n))
	case n <= 0xffff:
		*out = append(*out, major|25, byte(n>>8), byte(n))
	case n <= 0xffffffff:
		*out = append(*out, major|26, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	default:
		*out = append(*out, major|27, byte(n>>56), byte(n>>48), byte(n>>40), byte(n>>32), byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
}

// CIDString returns the CIDv1 (codec 0x71 dag-cbor, sha2-256) multibase
// base32-lowercase string for the encoded bytes — the form atproto uses in
// strongRefs and record CIDs.
func CIDString(b []byte) (string, error) {
	sum := sha256.Sum256(b)
	var cid []byte
	cid = append(cid, 0x01) // version 1
	cid = append(cid, 0x71) // dag-cbor
	cid = append(cid, 0x12, 0x20)
	cid = append(cid, sum[:]...)
	enc := base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)
	return "b" + enc.EncodeToString(cid), nil
}
