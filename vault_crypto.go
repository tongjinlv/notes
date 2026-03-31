package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// 磁盘上的 note.md、图片等待选为密文（推送到 Git 后无口令无法解读）。
// 格式：魔数 + 版本 + salt + nonce + AES-GCM 密文（含 tag）。

const (
	noteEncMagic    = "NOTESENC1\n"
	noteEncVersion  = byte(1)
	noteEncSaltLen  = 16
	noteEncNonceLen = 12
	pbkdf2Iter      = 200000
	pbkdf2KeyLen    = 32
)

var errEncryptedNoPassphrase = errors.New("vault: 文件已加密但未配置 vaultPassphrase 或 NOTES_VAULT_PASSPHRASE")

func isEncryptedVaultBlob(raw []byte) bool {
	return len(raw) >= len(noteEncMagic) && string(raw[:len(noteEncMagic)]) == noteEncMagic
}

// wrapVaultBlob 在 passphrase 非空时将任意内容加密写入磁盘；空口令则原样返回。
func wrapVaultBlob(plain []byte, passphrase string) ([]byte, error) {
	if strings.TrimSpace(passphrase) == "" {
		return plain, nil
	}
	salt := make([]byte, noteEncSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	nonce := make([]byte, noteEncNonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	key := pbkdf2.Key([]byte(passphrase), salt, pbkdf2Iter, pbkdf2KeyLen, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plain, nil)
	var b bytes.Buffer
	b.WriteString(noteEncMagic)
	b.WriteByte(noteEncVersion)
	b.Write(salt)
	b.Write(nonce)
	b.Write(sealed)
	return b.Bytes(), nil
}

// unwrapVaultBlob 若为密文则解密；明文或空口令则原样返回（兼容未加密旧文件）。
func unwrapVaultBlob(raw []byte, passphrase string) ([]byte, error) {
	if !isEncryptedVaultBlob(raw) {
		return raw, nil
	}
	if strings.TrimSpace(passphrase) == "" {
		return nil, errEncryptedNoPassphrase
	}
	rest := raw[len(noteEncMagic):]
	if len(rest) < 1+noteEncSaltLen+noteEncNonceLen+gcmMinOverhead {
		return nil, errors.New("vault: 损坏的加密文件")
	}
	if rest[0] != noteEncVersion {
		return nil, errors.New("vault: 不支持的加密版本")
	}
	rest = rest[1:]
	salt := rest[:noteEncSaltLen]
	nonce := rest[noteEncSaltLen : noteEncSaltLen+noteEncNonceLen]
	sealed := rest[noteEncSaltLen+noteEncNonceLen:]
	key := pbkdf2.Key([]byte(passphrase), salt, pbkdf2Iter, pbkdf2KeyLen, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, errors.New("vault: 无法解密文件，口令可能错误或未配置加密")
	}
	return plain, nil
}

// GCM tag is 16 bytes; minimum ciphertext is 16 for empty plaintext in practice Seal adds tag
const gcmMinOverhead = 16
