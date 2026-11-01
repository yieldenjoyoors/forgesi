package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/yieldenjoyoors/forgesi/internal/atauth"
)

func main() {
	rawPath := os.Getenv("KEYFILE")
	if rawPath == "" {
		rawPath = "/tmp/knotkey.txt"
	}
	raw, _ := os.ReadFile(rawPath)
	seed := strings.TrimSpace(string(raw))
	// main.go loadMintKey: 32-byte scalar hex (sha256 of the file content
	// when the file isn't already a 64-char hex scalar)
	var keyBytes []byte
	if len(seed) == 64 {
		keyBytes, _ = hex.DecodeString(seed)
	} else {
		h := sha256.Sum256([]byte(seed))
		keyBytes = h[:]
	}
	key, _ := btcec.PrivKeyFromBytes(keyBytes)
	iss := os.Args[1]
	aud := os.Args[2]
	lxm := ""
	if len(os.Args) > 3 {
		lxm = os.Args[3]
	}
	tok, err := atauth.Mint(key, iss, aud, lxm, 30*time.Minute, time.Now())
	if err != nil {
		panic(err)
	}
	fmt.Print(tok)
}
