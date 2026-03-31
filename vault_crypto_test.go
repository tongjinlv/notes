package main

import (
	"bytes"
	"testing"
)

func TestWrapUnwrapRoundTrip(t *testing.T) {
	plain := []byte("---\nid: n_test\ntitle: hi\n---\n\nbody\n")
	pass := "test-passphrase-32-chars-long!!"
	out, err := wrapVaultBlob(plain, pass)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(out, plain) {
		t.Fatal("expected ciphertext")
	}
	back, err := unwrapVaultBlob(out, pass)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, plain) {
		t.Fatalf("got %q want %q", back, plain)
	}
	_, err = unwrapVaultBlob(out, "wrong")
	if err == nil {
		t.Fatal("expected error for wrong passphrase")
	}
}

func TestWrapEmptyPassphraseNoOp(t *testing.T) {
	plain := []byte("hello")
	out, err := wrapVaultBlob(plain, "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, plain) {
		t.Fatal("expected plaintext passthrough")
	}
}
