package secrets

import (
	"bytes"
	"encoding/base64"
	"testing"
)

func TestBox(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeySize)
	b, err := NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	nonce, ct, err := b.Seal("db_password", []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("hunter2")) {
		t.Error("ciphertext contains the plaintext")
	}
	if got, err := b.Open("db_password", nonce, ct); err != nil || string(got) != "hunter2" {
		t.Errorf("Open = %q, %v", got, err)
	}
	if _, err := b.Open("other_name", nonce, ct); err == nil {
		t.Error("a ciphertext opened under another secret's name")
	}
	other, _ := NewBox(bytes.Repeat([]byte{8}, KeySize))
	if _, err := other.Open("db_password", nonce, ct); err == nil {
		t.Error("a ciphertext opened with another key")
	}
}

func TestParseKey(t *testing.T) {
	if _, err := ParseKey(base64.StdEncoding.EncodeToString(make([]byte, KeySize))); err != nil {
		t.Errorf("valid key rejected: %v", err)
	}
	for _, bad := range []string{"not base64!", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, err := ParseKey(bad); err == nil {
			t.Errorf("ParseKey(%q) accepted", bad)
		}
	}
}
