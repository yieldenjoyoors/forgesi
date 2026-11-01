package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/yieldenjoyoors/forgesi/internal/atauth"
)

func main() {
	tok, _ := io.ReadAll(os.Stdin)
	tok = []byte(strings.TrimSpace(string(tok)))
	did := os.Args[1]
	url := os.Args[2]
	res, err := http.Get(url)
	if err != nil {
		panic(err)
	}
	body, _ := io.ReadAll(res.Body)
	var raw struct {
		VerificationMethod []struct {
			ID                 string `json:"id"`
			Type               string `json:"type"`
			PublicKeyMultibase string `json:"publicKeyMultibase"`
		} `json:"verificationMethod"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		panic(string(body))
	}
	doc := &atauth.DIDDoc{}
	for _, m := range raw.VerificationMethod {
		doc.Keys = append(doc.Keys, atauth.DIDKey{ID: m.ID, Type: m.Type, Multibase: m.PublicKeyMultibase})
	}
	v := atauth.Verifier{ResolveDID: func(d string) (*atauth.DIDDoc, error) {
		if d != did {
			return nil, fmt.Errorf("unexpected did %s", d)
		}
		return doc, nil
	}}
	editor, err := v.Verify(string(tok), did, "")
	if err != nil {
		fmt.Println("VERIFY FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("VERIFY OK:", editor)
}
