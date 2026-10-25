// sba-seal is an application-owned, stdin-only GitHub secret encryption helper.
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/curve25519"
	"golang.org/x/crypto/nacl/box"
)

var rejected = errors.New("EASYPROXY_SBA_SEAL_REJECTED")

func encrypt(input io.Reader, output io.Writer) error {
	data, err := io.ReadAll(io.LimitReader(input, 8193))
	if err != nil || len(data) > 8192 || !utf8.Valid(data) {
		return rejected
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return rejected
	}
	values := map[string]string{}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || (name != "publicKey" && name != "plaintext") {
			return rejected
		}
		if _, found := values[name]; found {
			return rejected
		}
		var value string
		if decoder.Decode(&value) != nil {
			return rejected
		}
		values[name] = value
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') || len(values) != 2 {
		return rejected
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return rejected
	}
	plaintext := values["plaintext"]
	if len(plaintext) == 0 || len(plaintext) > 512 || strings.ContainsAny(plaintext, "\r\n\x00") {
		return rejected
	}
	key, err := base64.StdEncoding.Strict().DecodeString(values["publicKey"])
	if err != nil || len(key) != 32 || base64.StdEncoding.EncodeToString(key) != values["publicKey"] {
		return rejected
	}
	// box.Precompute in the pinned dependency does not reject low-order points.
	if _, err := curve25519.X25519(make([]byte, 32), key); err != nil {
		return rejected
	}
	var recipient [32]byte
	copy(recipient[:], key)
	ciphertext, err := box.SealAnonymous(nil, []byte(plaintext), &recipient, rand.Reader)
	if err != nil {
		return rejected
	}
	_, err = fmt.Fprintln(output, base64.StdEncoding.EncodeToString(ciphertext))
	return err
}

func main() {
	if len(os.Args) != 1 || encrypt(os.Stdin, os.Stdout) != nil {
		fmt.Fprintln(os.Stderr, rejected.Error())
		os.Exit(1)
	}
}
