// Package packfile builds and parses git packfiles (version 2), including
// ofs-delta and ref-delta application, with no external git dependency.
package packfile

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
)

// Pack object types.
const (
	TypeCommit = 1
	TypeTree   = 2
	TypeBlob   = 3
	TypeTag    = 4
	TypeOfsDel = 6
	TypeRefDel = 7
)

// Object is one decoded pack entry; OID is the git object id of the full
// (delta-applied) content.
type Object struct {
	Type int
	Data []byte
	OID  string
}

// TypeName maps a pack type to its git name.
func TypeName(t int) string {
	switch t {
	case TypeCommit:
		return "commit"
	case TypeTree:
		return "tree"
	case TypeBlob:
		return "blob"
	case TypeTag:
		return "tag"
	}
	return "?"
}

// TypeOf maps a git name to its pack type.
func TypeOf(name string) int {
	switch name {
	case "commit":
		return TypeCommit
	case "tree":
		return TypeTree
	case "blob":
		return TypeBlob
	case "tag":
		return TypeTag
	}
	return 0
}

// GitObject frames content as a git object with its type header.
func GitObject(typ string, content []byte) []byte {
	header := typ + " " + strconv.Itoa(len(content)) + "\x00"
	return append([]byte(header), content...)
}

// OID computes the git object id of framed content.
func OID(framed []byte) string {
	sum := sha1.Sum(framed)
	return fmt.Sprintf("%x", sum)
}

// Encode builds a version-2 packfile of full objects (no deltas — the
// format permits all-full-object packs).
func Encode(objs []Object) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("PACK")
	_ = binary.Write(&buf, binary.BigEndian, uint32(2))
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(objs)))
	for _, o := range objs {
		writeTypeSize(&buf, o.Type, uint64(len(o.Data)))
		zw := zlib.NewWriter(&buf)
		if _, err := zw.Write(o.Data); err != nil {
			return nil, err
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
	}
	sum := sha1.Sum(buf.Bytes())
	buf.Write(sum[:])
	return buf.Bytes(), nil
}

func writeTypeSize(buf *bytes.Buffer, typ int, size uint64) {
	first := byte((typ&7)<<4) | byte(size&0x0f)
	size >>= 4
	if size > 0 {
		first |= 0x80
	}
	buf.WriteByte(first)
	for size > 0 {
		b := byte(size & 0x7f)
		size >>= 7
		if size > 0 {
			b |= 0x80
		}
		buf.WriteByte(b)
	}
}

// Decode parses a packfile, applying ofs-deltas (bases within the pack)
// and ref-deltas (bases earlier in the pack); returns objects in stream
// order with computed oids.
func Decode(r io.Reader) ([]Object, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(raw) < 12 || string(raw[:4]) != "PACK" {
		return nil, fmt.Errorf("packfile: bad magic")
	}
	count := int(binary.BigEndian.Uint32(raw[8:12]))
	out := make([]Object, 0, count)
	byOffset := make(map[int]Object, count)
	byOID := make(map[string]Object, count)
	pos := 12
	for i := 0; i < count; i++ {
		objOffset := pos
		typ, _, adv, err := readTypeSize(raw, pos)
		if err != nil {
			return nil, err
		}
		pos += adv
		var content []byte
		switch typ {
		case TypeOfsDel:
			rel, adv2 := readOffsetVarint(raw, pos)
			pos += adv2
			base, ok := byOffset[objOffset-rel]
			if !ok {
				return nil, fmt.Errorf("packfile: ofs-delta base missing at %d", objOffset-rel)
			}
			delta, consumed, err := inflate(raw, pos)
			if err != nil {
				return nil, err
			}
			pos += consumed
			if content, err = applyDelta(base.Data, delta); err != nil {
				return nil, err
			}
			typ = base.Type
		case TypeRefDel:
			if pos+20 > len(raw) {
				return nil, io.ErrUnexpectedEOF
			}
			baseOID := fmt.Sprintf("%x", raw[pos:pos+20])
			pos += 20
			base, ok := byOID[baseOID]
			if !ok {
				return nil, fmt.Errorf("packfile: ref-delta base %s not in pack", baseOID)
			}
			delta, consumed, err := inflate(raw, pos)
			if err != nil {
				return nil, err
			}
			pos += consumed
			if content, err = applyDelta(base.Data, delta); err != nil {
				return nil, err
			}
			typ = base.Type
		default:
			var consumed int
			var err error
			if content, consumed, err = inflate(raw, pos); err != nil {
				return nil, err
			}
			pos += consumed
		}
		framed := GitObject(TypeName(typ), content)
		o := Object{Type: typ, Data: content, OID: OID(framed)}
		out = append(out, o)
		byOffset[objOffset] = o
		byOID[o.OID] = o
	}
	return out, nil
}

// inflate decompresses one zlib stream at data[pos]. bytes.Reader is an
// io.ByteReader, so flate consumes byte-by-byte with no read-ahead — the
// remaining length gives the EXACT consumed size (a plain io.Reader would
// get a 4KB bufio wrapper and swallow the next object's header).
func inflate(data []byte, pos int) (content []byte, consumed int, err error) {
	r := bytes.NewReader(data[pos:])
	zr, err := zlib.NewReader(r)
	if err != nil {
		return nil, 0, err
	}
	content, err = io.ReadAll(zr)
	if err != nil {
		return nil, 0, err
	}
	zc, _ := zr.(interface{ Close() error })
	_ = zc.Close()
	return content, (len(data) - pos) - r.Len(), nil
}

func readTypeSize(data []byte, pos int) (typ int, size uint64, adv int, err error) {
	if pos >= len(data) {
		return 0, 0, 0, io.ErrUnexpectedEOF
	}
	b := data[pos]
	adv++
	typ = int((b >> 4) & 7)
	size = uint64(b & 0x0f)
	shift := uint(4)
	for b&0x80 != 0 {
		if pos+adv >= len(data) {
			return 0, 0, 0, io.ErrUnexpectedEOF
		}
		b = data[pos+adv]
		adv++
		size |= uint64(b&0x7f) << shift
		shift += 7
	}
	return typ, size, adv, nil
}

func readOffsetVarint(data []byte, pos int) (off int, adv int) {
	b := data[pos]
	adv = 1
	off = int(b & 0x7f)
	for b&0x80 != 0 {
		b = data[pos+adv]
		adv++
		off = ((off + 1) << 7) | int(b&0x7f)
	}
	return off, adv
}

func applyDelta(base, delta []byte) ([]byte, error) {
	pos := 0
	readVarint := func() uint64 {
		var v uint64
		var shift uint
		for {
			b := delta[pos]
			pos++
			v |= uint64(b&0x7f) << shift
			shift += 7
			if b&0x80 == 0 {
				break
			}
		}
		return v
	}
	baseSize := readVarint()
	if baseSize != uint64(len(base)) {
		return nil, fmt.Errorf("packfile: delta base size mismatch")
	}
	resultSize := readVarint()
	out := make([]byte, 0, resultSize)
	for pos < len(delta) {
		op := delta[pos]
		pos++
		if op&0x80 != 0 {
			var cpOff, cpSize uint64
			for i := 0; i < 4; i++ {
				if op&(1<<uint(i)) != 0 {
					cpOff |= uint64(delta[pos]) << uint(8*i)
					pos++
				}
			}
			for i := 0; i < 3; i++ {
				if op&(0x10<<uint(i)) != 0 {
					cpSize |= uint64(delta[pos]) << uint(8*i)
					pos++
				}
			}
			if cpSize == 0 {
				cpSize = 0x10000
			}
			if cpOff+cpSize > uint64(len(base)) {
				return nil, fmt.Errorf("packfile: delta copy out of base bounds")
			}
			out = append(out, base[cpOff:cpOff+cpSize]...)
		} else if op > 0 {
			out = append(out, delta[pos:pos+int(op)]...)
			pos += int(op)
		} else {
			return nil, fmt.Errorf("packfile: delta opcode 0")
		}
	}
	if uint64(len(out)) != resultSize {
		return nil, fmt.Errorf("packfile: delta result size mismatch")
	}
	return out, nil
}
