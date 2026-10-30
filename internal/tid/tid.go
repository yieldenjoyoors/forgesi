// Package tid mints atproto timestamp identifiers: 13-char base32-sortable
// strings encoding 53 bits of Unix microseconds plus a 10-bit clock id.
// Record rkeys in the Tangled knot flow use these.
package tid

import (
	"fmt"
	"sync"
	"time"
)

const charset = "234567abcdefghijklmnopqrstuvwxyz" // base32-sortable (0-9a-v)

var (
	mu      sync.Mutex
	last    int64  // last micros issued
	counter uint32 // advances within the same microsecond (clock id space)
)

// TID is a minted timestamp identifier.
type TID string

// Now mints a new TID: monotonically increasing within the process.
func Now() TID {
	mu.Lock()
	defer mu.Unlock()
	micros := time.Now().UnixMicro()
	if micros <= last {
		// Same (or regressed) clock tick: bump the counter instead of
		// rewinding — sort order must never go backwards.
		counter++
		if counter > 0x3ff {
			counter = 0
			micros = last + 1 // clock id exhausted: advance a microsecond
		}
		if micros < last {
			micros = last
		}
		micros = (micros &^ 0x3ff) | int64(counter)
		if micros <= last {
			micros = last + 1
		}
	} else {
		counter = 0
	}
	last = micros
	return encode(uint64(micros)<<10 | uint64(counter))
}

func encode(v uint64) TID {
	var out [13]byte
	for i := 12; i >= 0; i-- {
		out[i] = charset[v&0x1f]
		v >>= 5
	}
	return TID(out[:])
}

// String returns the 13-char form.
func (t TID) String() string { return string(t) }

// Parse validates a TID string.
func Parse(s string) (TID, error) {
	if len(s) != 13 {
		return "", fmt.Errorf("tid: %q is not 13 chars", s)
	}
	var v uint64
	for i := 0; i < 13; i++ {
		d := index(charset, s[i])
		if d < 0 {
			return "", fmt.Errorf("tid: %q has non-sortable-base32 char %q", s, s[i])
		}
		v = v<<5 | uint64(d)
	}
	return TID(s), nil
}

func index(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// Valid reports whether s is a well-formed TID.
func Valid(s string) bool {
	_, err := Parse(s)
	return err == nil
}
