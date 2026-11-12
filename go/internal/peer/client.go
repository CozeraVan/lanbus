package peer

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client is the initiator side of the direct protocol (wire-compatible with
// Python DirectClient, including the credentials.json cache layout).
type Client struct {
	URL             string
	DataDir         string
	Identity        *Identity
	credentialsPath string
	http            *http.Client
}

func NewClient(url, dataDir string) (*Client, error) {
	id, err := LoadIdentity(dataDir)
	if err != nil {
		return nil, err
	}
	return &Client{
		URL:             strings.TrimRight(url, "/"),
		DataDir:         dataDir,
		Identity:        id,
		credentialsPath: filepath.Join(dataDir, "credentials.json"),
		http:            &http.Client{Timeout: 960 * time.Second},
	}, nil
}

func (c *Client) loadCredentials() map[string]any {
	raw, err := os.ReadFile(c.credentialsPath)
	if err != nil {
		return map[string]any{}
	}
	obj, err := ParseObject(raw)
	if err != nil {
		return map[string]any{}
	}
	return obj
}

func (c *Client) SaveGrant(grant map[string]any) error {
	saved := c.loadCredentials()
	saved[asString(grant["target_peer_id"])] = grant
	return atomicJSON(c.credentialsPath, saved, true)
}

func (c *Client) FindGrant(targetPeerID, scope string) map[string]any {
	saved := c.loadCredentials()
	grant, _ := saved[targetPeerID].(map[string]any)
	if grant == nil {
		return nil
	}
	grantScope := asString(grant["scope"])
	if scope != "" && grantScope != scope && grantScope != "project" {
		return nil
	}
	return grant
}

func (c *Client) DropGrant(targetPeerID string) {
	saved := c.loadCredentials()
	if _, ok := saved[targetPeerID]; !ok {
		return
	}
	delete(saved, targetPeerID)
	atomicJSON(c.credentialsPath, saved, true)
}

// Request sends one encrypted direct request under an approved grant.
func (c *Client) Request(grant map[string]any, message, scope, cwd string, timeout float64) (map[string]any, error) {
	signPublic := asString(grant["target_sign_public"])
	pubRaw, err := unb64(signPublic)
	if err != nil || sha256Sum(pubRaw)[:32] != asString(grant["target_peer_id"]) ||
		!VerifySignature(signPublic, stripKey(grant, "target_signature"), asString(grant["target_signature"])) {
		return nil, fmt.Errorf("目标授权签名或身份无效")
	}
	key, err := c.Identity.SessionKey(asString(grant["target_x_public"]),
		asString(grant["initiator_nonce"]), asString(grant["target_nonce"]))
	if err != nil {
		return nil, err
	}
	requestID := randomHex(16)
	aad := asString(grant["grant_id"]) + ":" + requestID
	packet, err := EncryptPacket(key, map[string]any{
		"message": message, "scope": scope, "cwd": cwd, "timeout": PyFloat(timeout),
	}, aad)
	if err != nil {
		return nil, err
	}
	response, err := JSONRequest(c.URL+"/peer/request", map[string]any{
		"grant_id": asString(grant["grant_id"]), "request_id": requestID, "packet": packet,
	}, nil, timeout+30)
	if err != nil {
		return nil, err
	}
	respPacket, _ := response["packet"].(map[string]any)
	if respPacket == nil {
		respPacket = map[string]any{}
	}
	return DecryptPacket(key, respPacket, aad)
}

// RequestPairing mirrors peer.request_pairing.
func RequestPairing(url string, id *Identity, message, scope, cwd string) (map[string]any, error) {
	nonce := make([]byte, 24)
	rand.Read(nonce)
	payload := map[string]any{
		"source": map[string]any{
			"peer_id": id.PeerID, "hostname": hostname(), "agent": "lanbus-cli",
			"sign_public": id.SignPublicB64, "x_public": id.XPublicB64,
		},
		"message": message, "scope": scope, "cwd": cwd,
		"created_at": PyFloat(now()), "expires_at": PyFloat(now() + PendingTTLS),
		"nonce": b64(nonce),
	}
	signature, err := id.Sign(payload)
	if err != nil {
		return nil, err
	}
	payload["signature"] = signature
	return JSONRequest(strings.TrimRight(url, "/")+"/peer/pair", payload, nil, 30)
}

// PairingStatus mirrors peer.pairing_status.
func PairingStatus(url, requestID, peerID string) (map[string]any, error) {
	return JSONRequest(strings.TrimRight(url, "/")+"/peer/pair/status?id="+
		requestID+"&peer_id="+peerID, nil, nil, 30)
}

// JSONRequest posts canonical JSON (or GET when payload nil); non-2xx answers
// become errors carrying the server's "error" text, like Python json_request.
func JSONRequest(url string, payload map[string]any, headers map[string]string, timeout float64) (map[string]any, error) {
	var body io.Reader
	method := "GET"
	if payload != nil {
		raw, err := Canonical(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
		method = "POST"
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: time.Duration(timeout * float64(time.Second))}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail := map[string]any{}
		if obj, err := ParseObject(raw); err == nil {
			detail = obj
		}
		msg := asString(detail["error"])
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("%s", msg)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	return ParseObject(raw)
}

var _ = json.Number("")