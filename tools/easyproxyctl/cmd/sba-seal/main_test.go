package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/nacl/box"
)

func TestAnonymousRoundTrip(t *testing.T) {
	public, private, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"fixture-secret", "含中文的密钥 with spaces", strings.Repeat("x", 512)} {
		input, _ := json.Marshal(map[string]string{"publicKey": base64.StdEncoding.EncodeToString(public[:]), "plaintext": message})
		var outputs []string
		for i := 0; i < 2; i++ {
			var output bytes.Buffer
			if err := encrypt(bytes.NewReader(input), &output); err != nil {
				t.Fatal(err)
			}
			ciphertext, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(output.String()))
			if err != nil || len(ciphertext) != len(message)+box.AnonymousOverhead {
				t.Fatal("invalid sealed box size")
			}
			actual, ok := box.OpenAnonymous(nil, ciphertext, public, private)
			if !ok || string(actual) != message {
				t.Fatal("sealed box round trip failed")
			}
			outputs = append(outputs, output.String())
		}
		if outputs[0] == outputs[1] {
			t.Fatal("ephemeral randomness was reused")
		}
	}
}

func TestRejectsInvalidInputWithoutOutput(t *testing.T) {
	public, _, _ := box.GenerateKey(rand.Reader)
	key := base64.StdEncoding.EncodeToString(public[:])
	valid := `{"publicKey":"` + key + `","plaintext":"fixture-secret"}`
	cases := []string{"", "{}", "[]", valid + "{}", valid + " null", strings.Repeat(" ", 8193),
		strings.Replace(valid, `"plaintext":`, `"extra":1,"plaintext":`, 1),
		strings.Replace(valid, `"plaintext":`, `"plaintext":"duplicate","plaintext":`, 1),
		strings.Replace(valid, "fixture-secret", "", 1),
		strings.Replace(valid, "fixture-secret", strings.Repeat("x", 513), 1),
		strings.Replace(valid, "fixture-secret", `line\nbreak`, 1),
		strings.Replace(valid, "fixture-secret", `nul\u0000byte`, 1),
		strings.Replace(valid, key, base64.StdEncoding.EncodeToString(make([]byte, 32)), 1),
		strings.Replace(valid, key, "bad-key", 1),
		strings.Replace(valid, "fixture-secret", string([]byte{0xff}), 1)}
	for index, input := range cases {
		var output bytes.Buffer
		if err := encrypt(strings.NewReader(input), &output); err == nil || output.Len() != 0 {
			t.Fatalf("case %d accepted invalid input or exposed output", index)
		}
	}
}
