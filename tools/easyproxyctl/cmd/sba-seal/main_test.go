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
	for _, message := range []string{
		"fixture-secret",
		"含中文的密钥 with spaces",
		"multiline\nsecret\r\n\twith-tab",
		strings.Repeat("x", 16384),
	} {
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
	cases := []string{"", "{}", "[]", valid + "{}", valid + " null", strings.Repeat(" ", 131073),
		strings.Replace(valid, `"plaintext":`, `"extra":1,"plaintext":`, 1),
		strings.Replace(valid, `"plaintext":`, `"plaintext":"duplicate","plaintext":`, 1),
		strings.Replace(valid, "fixture-secret", "", 1),
		strings.Replace(valid, "fixture-secret", "verification-not-applicable", 1),
		strings.Replace(valid, "fixture-secret", "repair-not-applicable", 1),
		strings.Replace(valid, "fixture-secret", strings.Repeat("x", 16385), 1),
		strings.Replace(valid, "fixture-secret", `nul\u0000byte`, 1),
		strings.Replace(valid, "fixture-secret", `vtab\u000bbyte`, 1),
		strings.Replace(valid, "fixture-secret", `ff\u000cbyte`, 1),
		strings.Replace(valid, "fixture-secret", `esc\u001bbyte`, 1),
		strings.Replace(valid, "fixture-secret", `del\u007fbyte`, 1),
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
