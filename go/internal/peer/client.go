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

// Request sends one legacy encrypted direct request under an approved grant.
func (c *Client) Request(grant map[string]any, message, scope, cwd string, timeout float64) (map[string]any, error) {
	return c.request(grant, message, scope, cwd, timeout, 0, false)
}

// RequestV2 uses per-request approval when the target advertises request_v2.
func (c *Client) RequestV2(grant map[string]any, message, scope, cwd string, timeout, statusTimeout float64) (map[string]any, error) {
	return c.request(grant, message, scope, cwd, timeout, statusTimeout, true)
}

func (c *Client) request(grant map[string]any, message, scope, cwd string, timeout, statusTimeout float64, negotiate bool) (map[string]any, error) {
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
	body := map[string]any{
		"grant_id": asString(grant["grant_id"]), "request_id": requestID, "packet": packet,
	}
	if negotiate {
		health, err := JSONRequest(c.URL+"/peer/health", nil, nil, 10)
		if err != nil {
			return nil, err
		}
		if asString(health["request_policy"]) == "each" {
			if features, ok := health["approval_features"].([]any); ok {
				for _, feature := range features {
					if asString(feature) == "request_v2" {
						body["request_version"] = PyFloat(2)
					}
				}
			}
		}
	}
	if statusTimeout <= 0 {
		statusTimeout = 600
	}
	deadline := time.Now().Add(time.Duration(statusTimeout * float64(time.Second)))
	waiting := false
	for {
		response, _, err := jsonRequestStatus(c.URL+"/peer/request", body, nil, timeout+30)
		if err != nil {
			return nil, err
		}
		status := asString(response["status"])
		if status == "approval_required" {
			if !waiting {
				fmt.Printf("等待目标机器批准本次请求：%s\n", requestID)
				waiting = true
			}
			state, err := PairingStatus(c.URL, requestID, c.Identity.PeerID)
			if err != nil {
				return nil, err
			}
			switch asString(state["status"]) {
			case "denied":
				return nil, fmt.Errorf("目标机器已拒绝本次执行")
			case "expired":
				return nil, fmt.Errorf("目标机器的本次执行审批已过期")
			case "failed":
				return nil, fmt.Errorf("目标机器执行失败")
			}
		} else if status != "execution_running" {
			respPacket, _ := response["packet"].(map[string]any)
			if respPacket == nil {
				return nil, fmt.Errorf("目标返回了无法识别的直连响应")
			}
			return DecryptPacket(key, respPacket, aad)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等待目标机器批准或执行本次请求超时")
		}
		time.Sleep(300 * time.Millisecond)
	}
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
	response, _, err := jsonRequestStatus(url, payload, headers, timeout)
	return response, err
}

func jsonRequestStatus(url string, payload map[string]any, headers map[string]string, timeout float64) (map[string]any, int, error) {
	var body io.Reader
	method := "GET"
	if payload != nil {
		raw, err := Canonical(payload)
		if err != nil {
			return nil, 0, err
		}
		body = bytes.NewReader(raw)
		method = "POST"
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	client := &http.Client{Timeout: time.Duration(timeout * float64(time.Second))}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
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
		return nil, resp.StatusCode, fmt.Errorf("%s", msg)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, resp.StatusCode, nil
	}
	value, err := ParseObject(raw)
	return value, resp.StatusCode, err
}

var _ = json.Number("")
