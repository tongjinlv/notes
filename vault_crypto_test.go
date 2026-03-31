package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"testing"

	"golang.org/x/crypto/pbkdf2"
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

// v1 格式（每文件 PBKDF2）仍须能解密，供旧仓库兼容。
func TestV1LegacyBlobStillDecrypts(t *testing.T) {
	plain := []byte("legacy-v1-content")
	pass := "test-legacy-passphrase-32chars!!"
	salt := make([]byte, noteEncSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, noteEncNonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	key := pbkdf2.Key([]byte(pass), salt, pbkdf2Iter, pbkdf2KeyLen, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	sealed := gcm.Seal(nil, nonce, plain, nil)
	var b bytes.Buffer
	b.WriteString(noteEncMagic)
	b.WriteByte(noteEncVersionV1)
	b.Write(salt)
	b.Write(nonce)
	b.Write(sealed)

	back, err := unwrapVaultBlob(b.Bytes(), pass)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, plain) {
		t.Fatalf("v1 roundtrip: got %q want %q", back, plain)
	}
}

func BenchmarkUnwrapV2WarmMasterKey(b *testing.B) {
	plain := bytes.Repeat([]byte("x"), 1024)
	pass := "bench-passphrase-32-chars-long!!"
	blob, err := wrapVaultBlob(plain, pass)
	if err != nil {
		b.Fatal(err)
	}
	_, _ = unwrapVaultBlob(blob, pass)
	vaultMasterKeyCache.mu.Lock()
	vaultMasterKeyCache.key = nil
	vaultMasterKeyCache.passphrase = ""
	vaultMasterKeyCache.mu.Unlock()
	_, _ = masterKeyFromPassphrase(pass)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := unwrapVaultBlob(blob, pass)
		if err != nil {
			b.Fatal(err)
		}
	}
}
