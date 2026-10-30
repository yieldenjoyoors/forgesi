package cob

import (
	"encoding/binary"
	"fmt"
	"math"
)

// decodeCBOR decodes the DAG-CBOR subset Encode produces (maps, arrays,
// strings, bools, null, uint/int, float64) — enough to materialize stored
// record bodies back into JSON-shaped maps.
func decodeCBOR(b []byte) (any, []byte, error) {
	if len(b) < 1 {
		return nil, nil, fmt.Errorf("cbor: empty")
	}
	major := b[0] & 0xe0
	arg := b[0] & 0x1f
	val, rest, err := readArg(b[1:], arg)
	if err != nil {
		return nil, nil, err
	}
	switch major {
	case 0x00:
		return val, rest, nil
	case 0x20:
		n, ok := val.(uint64)
		if !ok {
			return nil, nil, fmt.Errorf("cbor: bad negative")
		}
		return -1 - int64(n), rest, nil
	case 0x60:
		n, ok := val.(uint64)
		if !ok || uint64(len(rest)) < n {
			return nil, nil, fmt.Errorf("cbor: bad string")
		}
		return string(rest[:n]), rest[n:], nil
	case 0x80:
		n, ok := val.(uint64)
		if !ok {
			return nil, nil, fmt.Errorf("cbor: bad array")
		}
		out := make([]any, 0, n)
		for i := uint64(0); i < n; i++ {
			var item any
			item, rest, err = decodeCBOR(rest)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, item)
		}
		return out, rest, nil
	case 0xa0:
		n, ok := val.(uint64)
		if !ok {
			return nil, nil, fmt.Errorf("cbor: bad map")
		}
		out := map[string]any{}
		for i := uint64(0); i < n; i++ {
			var k, v any
			k, rest, err = decodeCBOR(rest)
			if err != nil {
				return nil, nil, err
			}
			v, rest, err = decodeCBOR(rest)
			if err != nil {
				return nil, nil, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, nil, fmt.Errorf("cbor: non-string map key")
			}
			out[ks] = v
		}
		return out, rest, nil
	case 0xe0:
		switch arg {
		case 20:
			return false, rest, nil
		case 21:
			return true, rest, nil
		case 22:
			return nil, rest, nil
		}
		return nil, nil, fmt.Errorf("cbor: simple %d unsupported", arg)
	case 0xfb:
		if len(rest) < 8 {
			return nil, nil, fmt.Errorf("cbor: short float")
		}
		return math.Float64frombits(binary.BigEndian.Uint64(rest[:8])), rest[8:], nil
	}
	return nil, nil, fmt.Errorf("cbor: major %x unsupported", major)
}

func readArg(b []byte, arg byte) (any, []byte, error) {
	switch {
	case arg < 24:
		return uint64(arg), b, nil
	case arg == 24:
		if len(b) < 1 {
			return nil, nil, fmt.Errorf("cbor: short u8")
		}
		return uint64(b[0]), b[1:], nil
	case arg == 25:
		if len(b) < 2 {
			return nil, nil, fmt.Errorf("cbor: short u16")
		}
		return uint64(binary.BigEndian.Uint16(b[:2])), b[2:], nil
	case arg == 26:
		if len(b) < 4 {
			return nil, nil, fmt.Errorf("cbor: short u32")
		}
		return uint64(binary.BigEndian.Uint32(b[:4])), b[4:], nil
	case arg == 27:
		if len(b) < 8 {
			return nil, nil, fmt.Errorf("cbor: short u64")
		}
		return binary.BigEndian.Uint64(b[:8]), b[8:], nil
	}
	return nil, nil, fmt.Errorf("cbor: bad arg %d", arg)
}
