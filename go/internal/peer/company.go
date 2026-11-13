package peer

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Company struct {
	ID      string `json:"company_id"`
	Name    string `json:"company_name"`
	Secret  string `json:"secret"`
	Members []any  `json:"members"`
}

func LoadCompany(dataDir string) (*Company, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "company.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	obj, err := ParseObject(raw)
	if err != nil {
		return nil, err
	}
	secret := asString(obj["secret"])
	if len(secret) != 64 {
		return nil, fmt.Errorf("公司凭证文件损坏")
	}
	if _, err := hex.DecodeString(secret); err != nil {
		return nil, err
	}
	return &Company{ID: asString(obj["company_id"]), Name: asString(obj["company_name"]), Secret: secret}, nil
}

func CreateCompany(dataDir, name string) (*Company, error) {
	if strings.TrimSpace(name) == "" {
		name = "Lanbus 公司"
	}
	company := &Company{ID: randomHex(8), Name: name, Secret: randomHex(32), Members: []any{}}
	return company, SaveCompany(dataDir, company)
}

func SaveCompany(dataDir string, company *Company) error {
	return atomicJSON(filepath.Join(dataDir, "company.json"), map[string]any{
		"company_id": company.ID, "company_name": company.Name, "secret": company.Secret,
		"members": company.Members,
	}, true)
}

func CompanyProof(secret, companyID, peerID, nonce string) string {
	key, _ := hex.DecodeString(secret)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(companyID + ":" + peerID + ":" + nonce))
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *Client) RequestCompany(company *Company, message, scope, cwd string) (map[string]any, error) {
	return c.requestPairing(company, false, message, scope, cwd)
}

func (c *Client) JoinCompany(message, scope, cwd string, statusTimeout float64) (*Company, error) {
	pending, err := c.requestPairing(nil, true, message, scope, cwd)
	if err != nil {
		return nil, err
	}
	requestID := asString(pending["request_id"])
	if statusTimeout <= 0 {
		statusTimeout = 600
	}
	deadline := time.Now().Add(time.Duration(statusTimeout * float64(time.Second)))
	for time.Now().Before(deadline) {
		result, err := c.pairingStatus(requestID)
		if err != nil {
			return nil, err
		}
		switch asString(result["status"]) {
		case "approved":
			grant, _ := result["grant"].(map[string]any)
			if err := c.SaveGrant(grant); err != nil {
				return nil, err
			}
			key, err := c.Identity.SessionKey(asString(grant["target_x_public"]), asString(grant["initiator_nonce"]), asString(grant["target_nonce"]))
			if err != nil {
				return nil, err
			}
			packet, _ := result["company_packet"].(map[string]any)
			value, err := DecryptPacket(key, packet, "company:"+asString(grant["grant_id"]))
			if err != nil {
				return nil, err
			}
			company := &Company{ID: asString(value["company_id"]), Name: asString(value["company_name"]), Secret: asString(value["secret"]), Members: []any{}}
			return company, SaveCompany(c.DataDir, company)
		case "denied", "expired":
			return nil, fmt.Errorf("目标已拒绝加入公司或申请已过期")
		}
		time.Sleep(300 * time.Millisecond)
	}
	return nil, fmt.Errorf("等待目标批准加入公司超时")
}

// pairingStatus mirrors DirectClient._pairing_status: the server registers a
// pending request before replying, so on slow machines the first status query
// can race ahead of registration; retry "not found" briefly.
func (c *Client) pairingStatus(requestID string) (map[string]any, error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		result, err := PairingStatus(c.URL, requestID, c.Identity.PeerID)
		if err == nil {
			return result, nil
		}
		if !strings.Contains(err.Error(), "不存在") || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (c *Client) requestPairing(company *Company, join bool, message, scope, cwd string) (map[string]any, error) {
	nonce := make([]byte, 24)
	rand.Read(nonce)
	payload := map[string]any{
		"source": map[string]any{"peer_id": c.Identity.PeerID, "hostname": hostname(), "agent": "lanbus-cli",
			"sign_public": c.Identity.SignPublicB64, "x_public": c.Identity.XPublicB64},
		"message": message, "scope": scope, "cwd": cwd,
		"created_at": PyFloat(now()), "expires_at": PyFloat(now() + PendingTTLS), "nonce": b64(nonce),
	}
	if company != nil {
		payload["company_id"] = company.ID
		payload["company_proof"] = CompanyProof(company.Secret, company.ID, c.Identity.PeerID, b64(nonce))
	}
	if join {
		payload["join_company"] = true
	}
	signature, err := c.Identity.Sign(payload)
	if err != nil {
		return nil, err
	}
	payload["signature"] = signature
	return JSONRequest(strings.TrimRight(c.URL, "/")+"/peer/pair", payload, nil, 30)
}
