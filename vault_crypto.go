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
	"sync"

	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/pbkdf2"
)

// 磁盘上的 note.md、图片等待选为密文（推送到 Git 后无口令无法解读）。
// v1：每文件 PBKDF2（慢，兼容旧数据）；v2：进程内单次 PBKDF2 得主密钥 + 每文件 HKDF（快）。

const (
	noteEncMagic     = "NOTESENC1\n"
	noteEncVersionV1 = byte(1)
	noteEncVersionV2 = byte(2)
	noteEncSaltLen   = 16
	noteEncNonceLen  = 12
	pbkdf2Iter       = 200000
	pbkdf2KeyLen     = 32
	// 主密钥派生盐（与每文件盐不同）；仅用于从口令得到 masterKey，固定写入二进制。
	masterKDFSalt = "local-notes-vault-master-kdf-v1"
)

var errEncryptedNoPassphrase = errors.New("vault: 文件已加密但未配置 vaultPassphrase 或 NOTES_VAULT_PASSPHRASE")

var vaultMasterKeyCache struct {
	mu         sync.Mutex
	passphrase string
	key        []byte
}

func masterKeyFromPassphrase(pass string) ([]byte, error) {
	pass = strings.TrimSpace(pass)
	if pass == "" {
		return nil, nil
	}
	vaultMasterKeyCache.mu.Lock()
	defer vaultMasterKeyCache.mu.Unlock()
	if vaultMasterKeyCache.passphrase == pass && vaultMasterKeyCache.key != nil {
		return vaultMasterKeyCache.key, nil
	}
	key := pbkdf2.Key([]byte(pass), []byte(masterKDFSalt), pbkdf2Iter, pbkdf2KeyLen, sha256.New)
	vaultMasterKeyCache.passphrase = pass
	vaultMasterKeyCache.key = key
	return key, nil
}

func deriveFileKeyV2(masterKey, fileSalt []byte) ([]byte, error) {
	r := hkdf.New(sha256.New, masterKey, fileSalt, []byte("local-notes-aes-gcm-file-v2"))
	k := make([]byte, 32)
	if _, err := io.ReadFull(r, k); err != nil {
		return nil, err
	}
	return k, nil
}

func isEncryptedVaultBlob(raw []byte) bool {
	return len(raw) >= len(noteEncMagic) && string(raw[:len(noteEncMagic)]) == noteEncMagic
}

func aesGCMSeal(key, nonce, plain []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nil, nonce, plain, nil), nil
}

func aesGCMSOpen(key, nonce, sealed []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, sealed, nil)
}

// wrapVaultBlob 在 passphrase 非空时将任意内容加密写入磁盘；空口令则原样返回。
func wrapVaultBlob(plain []byte, passphrase string) ([]byte, error) {
	if strings.TrimSpace(passphrase) == "" {
		return plain, nil
	}
	masterKey, err := masterKeyFromPassphrase(passphrase)
	if err != nil || masterKey == nil {
		return nil, err
	}
	salt := make([]byte, noteEncSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	nonce := make([]byte, noteEncNonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	fileKey, err := deriveFileKeyV2(masterKey, salt)
	if err != nil {
		return nil, err
	}
	sealed, err := aesGCMSeal(fileKey, nonce, plain)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(noteEncMagic)
	b.WriteByte(noteEncVersionV2)
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
	ver := rest[0]
	if ver != noteEncVersionV1 && ver != noteEncVersionV2 {
		return nil, errors.New("vault: 不支持的加密版本")
	}
	rest = rest[1:]
	salt := rest[:noteEncSaltLen]
	nonce := rest[noteEncSaltLen : noteEncSaltLen+noteEncNonceLen]
	sealed := rest[noteEncSaltLen+noteEncNonceLen:]

	var key []byte
	switch ver {
	case noteEncVersionV1:
		key = pbkdf2.Key([]byte(passphrase), salt, pbkdf2Iter, pbkdf2KeyLen, sha256.New)
	case noteEncVersionV2:
		masterKey, err := masterKeyFromPassphrase(passphrase)
		if err != nil {
			return nil, err
		}
		if masterKey == nil {
			return nil, errEncryptedNoPassphrase
		}
		key, err = deriveFileKeyV2(masterKey, salt)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("vault: 不支持的加密版本")
	}

	plain, err := aesGCMSOpen(key, nonce, sealed)
	if err != nil {
		return nil, errors.New("vault: 无法解密文件，口令可能错误或未配置加密")
	}
	return plain, nil
}

// GCM tag is 16 bytes; minimum ciphertext is 16 for empty plaintext in practice Seal adds tag
const gcmMinOverhead = 16
