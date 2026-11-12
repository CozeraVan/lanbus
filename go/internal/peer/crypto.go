package peer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

func b64(data []byte) string { return base64.StdEncoding.EncodeToString(data) }

func unb64(value string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(value)
}

// EncryptPacket mirrors peer.encrypt: AES-256-GCM over canonical(payload),
// 12-byte random nonce, aad bound to grant_id:request_id.
func EncryptPacket(key []byte, payload map[string]any, aad string) (map[string]any, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	plain, err := Canonical(payload)
	if err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plain, []byte(aad))
	return map[string]any{"nonce": b64(nonce), "ciphertext": b64(sealed)}, nil
}

// DecryptPacket mirrors peer.decrypt.
func DecryptPacket(key []byte, packet map[string]any, aad string) (map[string]any, error) {
	nonce, err := unb64(asString(packet["nonce"]))
	if err != nil {
		return nil, fmt.Errorf("直连请求解密或认证失败")
	}
	ciphertext, err := unb64(asString(packet["ciphertext"]))
	if err != nil {
		return nil, fmt.Errorf("直连请求解密或认证失败")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("直连请求解密或认证失败")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("直连请求解密或认证失败")
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("直连请求解密或认证失败")
	}
	obj, err := ParseObject(plain)
	if err != nil {
		return nil, fmt.Errorf("直连请求解密或认证失败")
	}
	return obj, nil
}