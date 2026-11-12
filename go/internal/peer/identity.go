package peer

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

// Identity is the long-lived peer identity: Ed25519 signing + X25519 exchange,
// persisted as PKCS8 PEM in identity.json (same format as the Python worker).
type Identity struct {
	Signing      ed25519.PrivateKey
	Exchange     *ecdh.PrivateKey
	SignPublicB64 string
	XPublicB64   string
	PeerID       string
}

func LoadIdentity(dataDir string) (*Identity, error) {
	path := filepath.Join(dataDir, "identity.json")
	raw, err := os.ReadFile(path)
	if err == nil {
		var saved struct {
			Version  int    `json:"version"`
			Signing  string `json:"signing"`
			Exchange string `json:"exchange"`
		}
		if json.Unmarshal(raw, &saved) != nil {
			return nil, fmt.Errorf("直连身份文件损坏：%s", path)
		}
		signing, err := parsePKCS8PEM(saved.Signing)
		if err != nil {
			return nil, fmt.Errorf("直连身份文件损坏：%s", path)
		}
		exchange, err := parsePKCS8PEM(saved.Exchange)
		if err != nil {
			return nil, fmt.Errorf("直连身份文件损坏：%s", path)
		}
		signPriv, ok := signing.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("直连身份文件损坏：%s", path)
		}
		xPriv, ok := exchange.(*ecdh.PrivateKey)
		if !ok || xPriv.Curve() != ecdh.X25519() {
			return nil, fmt.Errorf("直连身份文件损坏：%s", path)
		}
		return newIdentity(signPriv, xPriv), nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	_, signPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	xPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	id := newIdentity(signPriv, xPriv)
	signPEM, err := marshalPKCS8PEM(signPriv)
	if err != nil {
		return nil, err
	}
	xPEM, err := marshalPKCS8PEM(xPriv)
	if err != nil {
		return nil, err
	}
	if err := atomicJSON(path, map[string]any{
		"version": 1, "signing": signPEM, "exchange": xPEM,
	}, true); err != nil {
		return nil, err
	}
	return id, nil
}

func newIdentity(signing ed25519.PrivateKey, exchange *ecdh.PrivateKey) *Identity {
	signPub := signing.Public().(ed25519.PublicKey)
	sum := sha256.Sum256(signPub)
	return &Identity{
		Signing:       signing,
		Exchange:      exchange,
		SignPublicB64: b64(signPub),
		XPublicB64:    b64(exchange.PublicKey().Bytes()),
		PeerID:        hex.EncodeToString(sum[:])[:32],
	}
}

func (id *Identity) Sign(payload map[string]any) (string, error) {
	raw, err := Canonical(payload)
	if err != nil {
		return "", err
	}
	return b64(ed25519.Sign(id.Signing, raw)), nil
}

func VerifySignature(publicB64 string, payload map[string]any, signatureB64 string) bool {
	pubRaw, err := unb64(publicB64)
	if err != nil || len(pubRaw) != ed25519.PublicKeySize {
		return false
	}
	sig, err := unb64(signatureB64)
	if err != nil {
		return false
	}
	raw, err := Canonical(payload)
	if err != nil {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pubRaw), raw, sig)
}

// SessionKey derives the AES-256 key exactly like Python:
// HKDF-SHA256(salt=sha256(initiator_nonce||target_nonce), info="lanbus-direct-v1").
func (id *Identity) SessionKey(peerXPublicB64, initiatorNonceB64, targetNonceB64 string) ([]byte, error) {
	peerRaw, err := unb64(peerXPublicB64)
	if err != nil {
		return nil, fmt.Errorf("invalid exchange public")
	}
	peerPub, err := ecdh.X25519().NewPublicKey(peerRaw)
	if err != nil {
		return nil, err
	}
	shared, err := id.Exchange.ECDH(peerPub)
	if err != nil {
		return nil, err
	}
	initRaw, err := unb64(initiatorNonceB64)
	if err != nil {
		return nil, err
	}
	targetRaw, err := unb64(targetNonceB64)
	if err != nil {
		return nil, err
	}
	salt := sha256.Sum256(append(append([]byte{}, initRaw...), targetRaw...))
	prk := hmacSHA256(salt[:], shared)
	mac := hmac.New(sha256.New, prk)
	mac.Write([]byte("lanbus-direct-v1"))
	mac.Write([]byte{0x01})
	return mac.Sum(nil)[:32], nil
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func sha256Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func parsePKCS8PEM(text string) (any, error) {
	block, _ := pem.Decode([]byte(text))
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, fmt.Errorf("not a PRIVATE KEY PEM")
	}
	return x509.ParsePKCS8PrivateKey(block.Bytes)
}

func marshalPKCS8PEM(priv any) (string, error) {
	var der []byte
	switch k := priv.(type) {
	case ed25519.PrivateKey:
		d, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			return "", err
		}
		der = d
	case *ecdh.PrivateKey:
		d, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			// Fallback: hand-rolled RFC 8410 PKCS8 for X25519.
			d, err = marshalX25519PKCS8(k.Bytes())
			if err != nil {
				return "", err
			}
		}
		der = d
	default:
		return "", fmt.Errorf("unsupported key type %T", priv)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

func marshalX25519PKCS8(scalar []byte) ([]byte, error) {
	inner, err := asn1.Marshal(scalar) // OCTET STRING wrapping the 32-byte key
	if err != nil {
		return nil, err
	}
	type pkixAlgorithm struct {
		Algorithm asn1.ObjectIdentifier
	}
	type pkcs8 struct {
		Version    int
		Algo       pkixAlgorithm
		PrivateKey []byte
	}
	return asn1.Marshal(pkcs8{0, pkixAlgorithm{asn1.ObjectIdentifier{1, 3, 101, 110}}, inner})
}

func atomicJSON(path string, value any, private bool) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(parent, ".peer-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if private && os.PathSeparator == '/' {
		os.Chmod(tmpName, 0o600)
	}
	return os.Rename(tmpName, path)
}