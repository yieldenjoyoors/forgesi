// Package packline implements git pkt-line framing and the packfile
// container — the two wire formats smart-HTTP speaks.
package packline

import (
	"bytes"
	"fmt"
	"io"
)

// PktLine prefixes: flush ends a section, delim separates, response-end
// closes a v2 command.
const (
	Flush = "0000"
	Delim = "0001"
)

// EncodePkt frames b as one pkt-line (4 hex length + payload).
func EncodePkt(b []byte) []byte {
	n := len(b) + 4
	return append([]byte(fmt.Sprintf("%04x", n)), b...)
}

// Pkt returns the framed form of s (no trailing newline added).
func Pkt(s string) []byte { return EncodePkt([]byte(s)) }

// FlushPkt is a flush packet.
func FlushPkt() []byte { return []byte(Flush) }

// DelimPkt is a delimiter packet.
func DelimPkt() []byte { return []byte(Delim) }

// Reader decodes a pkt-line stream.
type Reader struct {
	r io.Reader
}

// NewReader wraps a pkt-line stream.
func NewReader(r io.Reader) *Reader { return &Reader{r: r} }

// Next returns the next packet's payload. ok=false at flush/delim/EOF;
// the kind says which.
func (pr *Reader) Next() (payload []byte, kind string, err error) {
	var lenBuf [4]byte
	if _, err := io.ReadFull(pr.r, lenBuf[:]); err != nil {
		return nil, "", err
	}
	n := 0
	for _, c := range lenBuf {
		var v int
		switch {
		case c >= '0' && c <= '9':
			v = int(c - '0')
		case c >= 'a' && c <= 'f':
			v = int(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v = int(c-'A') + 10
		default:
			return nil, "", fmt.Errorf("packline: bad length %q", string(lenBuf[:]))
		}
		n = n*16 + v
	}
	switch string(lenBuf[:]) {
	case Flush:
		return nil, "flush", nil
	case Delim:
		return nil, "delim", nil
	}
	if n < 4 {
		return nil, "", fmt.Errorf("packline: bad length %d", n)
	}
	buf := make([]byte, n-4)
	if _, err := io.ReadFull(pr.r, buf); err != nil {
		return nil, "", err
	}
	return buf, "data", nil
}

// ReadAll collects data packets until flush/EOF, dropping trailing newlines.
func (pr *Reader) ReadAll() ([]string, error) {
	var lines []string
	for {
		payload, kind, err := pr.Next()
		if err == io.EOF {
			return lines, nil
		}
		if err != nil {
			return nil, err
		}
		if kind != "data" {
			return lines, nil
		}
		lines = append(lines, string(bytes.TrimRight(payload, "\n")))
	}
}
