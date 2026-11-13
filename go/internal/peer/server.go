package peer

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	MaxBody     = 64 * 1024
	PendingTTLS = 600
)

// ExecutorFunc runs one approved direct request and returns the result map
// ({ok, exit_code, reply, peer}). Injected by the worker; a shell fallback
// is used when nil.
type ExecutorFunc func(message, scope, cwd string, timeout float64) map[string]any

type ServerConfig struct {
	Name          string
	DisplayName   string
	InstanceLabel string
	RequestPolicy string
	Agent         string
	CWD           string
	Command       string
	DataDir       string
	Host          string
	Port          int
	Executor      ExecutorFunc
	Notifier      func(Notification)
}

type Server struct {
	cfg            ServerConfig
	Identity       *Identity
	Grants         *GrantStore
	Company        *Company
	approvalSecret string
	cwd            string
	bindHost       string
	Port           int
	URL            string

	mu                 sync.Mutex
	pending            map[string]map[string]any
	completed          map[string]map[string]any
	executionPending   map[string]map[string]any
	executionRunning   map[string]map[string]any
	executionCompleted map[string]map[string]any
	seenRequests       map[string]bool
	pairNonces         map[string]bool
	rate               map[string][]float64

	http     *http.Server
	listener net.Listener
}

func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.Host == "" {
		cfg.Host = "0.0.0.0"
	}
	if cfg.DisplayName == "" {
		cfg.DisplayName = cfg.Name
	}
	if cfg.RequestPolicy != "pair" && cfg.RequestPolicy != "each" && cfg.RequestPolicy != "company" {
		cfg.RequestPolicy = "company"
	}
	if cfg.DataDir == "" {
		home, _ := os.UserHomeDir()
		cfg.DataDir = filepath.Join(home, ".lanbus", "peers", cfg.Name)
	}
	cwd, err := realpath(cfg.CWD)
	if err != nil || cwd == "" {
		cwd, _ = os.Getwd()
	}
	id, err := LoadIdentity(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	grants, err := NewGrantStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	secret, err := loadOrCreateSecret(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	company, err := LoadCompany(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:                cfg,
		Identity:           id,
		Grants:             grants,
		Company:            company,
		approvalSecret:     secret,
		cwd:                cwd,
		bindHost:           cfg.Host,
		pending:            map[string]map[string]any{},
		completed:          map[string]map[string]any{},
		executionPending:   map[string]map[string]any{},
		executionRunning:   map[string]map[string]any{},
		executionCompleted: map[string]map[string]any{},
		seenRequests:       map[string]bool{},
		pairNonces:         map[string]bool{},
		rate:               map[string][]float64{},
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port)))
	if err != nil {
		return nil, err
	}
	s.listener = listener
	s.Port = listener.Addr().(*net.TCPAddr).Port
	s.URL = fmt.Sprintf("http://127.0.0.1:%d", s.Port)
	// Publish the live port so `lanbus peer-ui` can find this server.
	atomicJSON(filepath.Join(cfg.DataDir, "peer.json"), map[string]any{
		"name": cfg.Name, "port": s.Port, "url": s.URL, "peer_id": s.Identity.PeerID,
	}, false)
	s.http = &http.Server{Handler: http.HandlerFunc(s.handle)}
	return s, nil
}

func (s *Server) Start() { go s.http.Serve(s.listener) }

func (s *Server) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.http.Shutdown(ctx)
}

func loadOrCreateSecret(dataDir string) (string, error) {
	path := filepath.Join(dataDir, "approval.json")
	if raw, err := os.ReadFile(path); err == nil {
		obj, err := ParseObject(raw)
		if err != nil {
			return "", err
		}
		if secret := asString(obj["secret"]); secret != "" {
			return secret, nil
		}
	}
	secret := randomHex(32)
	if err := atomicJSON(path, map[string]any{"secret": secret}, true); err != nil {
		return "", err
	}
	return secret, nil
}

func randomHex(n int) string {
	buf := make([]byte, n)
	rand.Read(buf)
	return hex.EncodeToString(buf)
}

func now() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func realpath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

func primaryLANIP() string {
	conn, err := net.Dial("udp", "10.255.255.255:1")
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func (s *Server) publicPeer(hostHeader string) map[string]any {
	features := []string{}
	if s.cfg.RequestPolicy == "each" {
		features = append(features, "request_v2")
	}
	companyID, companyName := "", ""
	if s.Company != nil {
		features = append(features, "company_v1")
		companyID, companyName = s.Company.ID, s.Company.Name
	}
	return map[string]any{
		"name": s.cfg.Name, "agent": s.cfg.Agent, "hostname": hostname(),
		"peer_id": s.Identity.PeerID, "direct_pairing": true,
		"display_name": s.cfg.DisplayName, "instance_label": s.cfg.InstanceLabel,
		"cwd_display": s.cwd, "request_policy": s.cfg.RequestPolicy,
		"approval_features": features, "company_id": companyID, "company_name": companyName,
		"endpoint": s.advertisedEndpoint(hostHeader), "scopes": []string{"chat", "project"},
	}
}

func (s *Server) notify(requestID, kind, message string) {
	if s.cfg.Notifier == nil {
		return
	}
	defer func() { _ = recover() }()
	s.cfg.Notifier(Notification{RequestID: requestID, Kind: kind, Message: message,
		URL: s.URL + "/peer/ui", Secret: s.approvalSecret, Target: s.publicPeer("")})
}

func (s *Server) advertisedEndpoint(hostHeader string) string {
	if s.bindHost != "0.0.0.0" && s.bindHost != "::" && s.bindHost != "" {
		return s.URL
	}
	candidate := strings.TrimSpace(hostHeader)
	host := candidate
	if strings.HasPrefix(host, "[") {
		host = strings.TrimPrefix(strings.SplitN(host, "]", 2)[0], "[")
	} else if strings.Count(host, ":") == 1 {
		host = host[:strings.LastIndex(host, ":")]
	}
	host = strings.ToLower(host)
	if host != "" && host != "localhost" && !strings.HasPrefix(host, "127.") && host != "::1" {
		return "http://" + candidate
	}
	if ip := primaryLANIP(); ip != "" {
		return fmt.Sprintf("http://%s:%d", ip, s.Port)
	}
	return s.URL
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	var status int
	var value any
	switch {
	case r.Method == "GET" && path == "/peer/health":
		status, value = 200, s.publicPeer(r.Host)
	case r.Method == "GET" && path == "/peer/ui":
		remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !isLoopback(remoteIP) {
			status, value = 403, map[string]any{"error": "审批页面只能在本机打开"}
			break
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(200)
		w.Write([]byte(approvalHTML))
		return
	case r.Method == "GET" && path == "/peer/console":
		remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !isLoopback(remoteIP) {
			status, value = 403, map[string]any{"error": "指挥台只能在本机打开"}
			break
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(200)
		w.Write([]byte(consoleHTML))
		return
	case r.Method == "GET" && path == "/peer/console/locals":
		if !s.consoleAuth(r) {
			status, value = 403, map[string]any{"error": "只能在本机使用指挥台"}
			break
		}
		status, value = s.consoleLocals()
	case r.Method == "POST" && strings.HasPrefix(path, "/peer/console/"):
		if !s.consoleAuth(r) {
			status, value = 403, map[string]any{"error": "只能在本机使用指挥台"}
			break
		}
		body, err := readBody(r)
		if err != nil {
			status, value = 400, map[string]any{"error": err.Error()}
			break
		}
		switch path {
		case "/peer/console/discover":
			status, value = s.consoleDiscover(body)
		case "/peer/console/pair":
			status, value = s.consolePair(body)
		case "/peer/console/pair-status":
			status, value = s.consolePairStatus(body)
		case "/peer/console/send":
			status, value = s.consoleSend(body)
		case "/peer/console/history":
			status, value = s.consoleHistory(body)
		case "/peer/console/reset":
			status, value = s.consoleReset(body)
		default:
			status, value = 404, map[string]any{"error": "not found"}
		}
	case r.Method == "GET" && path == "/peer/pair/status":
		status, value = s.status(r.URL.Query())
	case r.Method == "GET" && path == "/peer/inbox":
		status, value = s.local(w, r, path, nil)
	case r.Method == "POST" && (path == "/peer/pair" || path == "/peer/request" ||
		path == "/peer/approve" || path == "/peer/deny" || path == "/peer/revoke"):
		body, err := readBody(r)
		if err != nil {
			status, value = 400, map[string]any{"error": err.Error()}
			break
		}
		switch path {
		case "/peer/pair":
			status, value = s.pair(body)
		case "/peer/request":
			status, value = s.directRequest(body)
		default:
			status, value = s.local(w, r, path, body)
		}
	default:
		status, value = 404, map[string]any{"error": "not found"}
	}
	raw, _ := Canonical(value)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(raw)
}

func readBody(r *http.Request) (map[string]any, error) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > MaxBody {
		return nil, fmt.Errorf("请求体为空或超过 64 KiB")
	}
	return ParseObject(raw)
}

func (s *Server) expireLocked() {
	cutoff := now()
	for id, item := range s.pending {
		if asFloat(item["expires_at"]) < cutoff {
			source, _ := item["source"].(map[string]any)
			s.completed[id] = map[string]any{"status": "expired", "peer_id": asString(source["peer_id"])}
			delete(s.pending, id)
		}
	}
	for id, item := range s.executionPending {
		if asFloat(item["expires_at"]) < cutoff {
			item["status"] = "expired"
			s.executionCompleted[id] = item
			delete(s.executionPending, id)
		}
	}
}

func (s *Server) makeGrant(pending map[string]any, mode, scope, cwd string) (map[string]any, error) {
	source, _ := pending["source"].(map[string]any)
	cwds := []any{}
	if scope == "project" {
		cwds = append(cwds, cwd)
	}
	targetNonce := make([]byte, 24)
	rand.Read(targetNonce)
	grant := map[string]any{
		"grant_id": randomHex(16), "peer_id": asString(source["peer_id"]),
		"target_peer_id": s.Identity.PeerID, "mode": mode, "scope": scope, "cwds": cwds,
		"sign_public": asString(source["sign_public"]), "x_public": asString(source["x_public"]),
		"target_sign_public": s.Identity.SignPublicB64, "target_x_public": s.Identity.XPublicB64,
		"source_hostname": asString(source["hostname"]), "source_agent": asString(source["agent"]),
		"initiator_nonce": asString(pending["nonce"]), "target_nonce": b64(targetNonce),
		"created_at": PyFloat(now()),
	}
	signature, err := s.Identity.Sign(stripKey(grant, "target_signature"))
	if err != nil {
		return nil, err
	}
	grant["target_signature"] = signature
	return grant, s.Grants.PutGrant(grant)
}

func (s *Server) pair(payload map[string]any) (int, any) {
	source, _ := payload["source"].(map[string]any)
	if source == nil {
		source = map[string]any{}
	}
	signPublic := asString(source["sign_public"])
	pubRaw, err := unb64(signPublic)
	expected := ""
	if err == nil {
		sum := sha256Sum(pubRaw)
		expected = sum[:32]
	}
	signed := stripKey(payload, "signature")
	if expected != asString(source["peer_id"]) ||
		!VerifySignature(signPublic, signed, asString(payload["signature"])) {
		return 400, map[string]any{"error": "无效的来源身份或签名"}
	}
	scope := asString(payload["scope"])
	if scope != "chat" && scope != "project" {
		return 400, map[string]any{"error": "scope 必须是 chat 或 project"}
	}
	if trimSpace(asString(payload["message"])) == "" {
		return 400, map[string]any{"error": "请求摘要不能为空"}
	}
	if asFloat(payload["expires_at"]) < now() || asFloat(payload["created_at"]) > now()+30 {
		return 400, map[string]any{"error": "配对申请已过期"}
	}
	if asBool(payload["join_company"], false) && s.Company == nil {
		return 400, map[string]any{"error": "目标尚未加入任何公司"}
	}
	if companyID := asString(payload["company_id"]); companyID != "" {
		if s.Company == nil || companyID != s.Company.ID {
			return 403, map[string]any{"error": "目标不属于该公司"}
		}
		expectedProof := CompanyProof(s.Company.Secret, s.Company.ID, asString(source["peer_id"]), asString(payload["nonce"]))
		if !hmac.Equal([]byte(expectedProof), []byte(asString(payload["company_proof"]))) {
			return 403, map[string]any{"error": "公司成员证明无效"}
		}
		if s.cfg.RequestPolicy == "company" {
			grant, err := s.makeGrant(signed, "company", scope, s.cwd)
			if err != nil {
				return 500, map[string]any{"error": err.Error()}
			}
			return 200, map[string]any{"status": "approved", "request_id": "company-" + randomHex(8), "grant": grant}
		}
	}
	if asBool(payload["join_company"], false) {
		signed["kind"] = "company"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	nonce := asString(payload["nonce"])
	if s.pairNonces[nonce] {
		return 409, map[string]any{"error": "重复配对申请"}
	}
	peerID := asString(source["peer_id"])
	stamp := now()
	recent := []float64{}
	for _, t := range s.rate[peerID] {
		if stamp-t < 10 {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 5 {
		return 429, map[string]any{"error": "配对申请频率过高，请稍后重试"}
	}
	recent = append(recent, stamp)
	s.rate[peerID] = recent
	sourcePending := 0
	for _, item := range s.pending {
		if src, _ := item["source"].(map[string]any); asString(src["peer_id"]) == peerID {
			sourcePending++
		}
	}
	if sourcePending >= 8 || len(s.pending) >= 64 {
		return 429, map[string]any{"error": "待确认请求过多，请稍后重试"}
	}
	s.pairNonces[nonce] = true
	if len(s.pairNonces) > 1024 {
		trimmed := map[string]bool{}
		count := 0
		for k := range s.pairNonces {
			trimmed[k] = true
			count++
			if count >= 512 {
				break
			}
		}
		s.pairNonces = trimmed
	}
	requestID := randomHex(8)
	s.pending[requestID] = signed
	fmt.Printf("[peer:%s] 待确认 %s: %s %s scope=%s message=%.120s\n",
		s.cfg.Name, requestID, asString(source["hostname"]), asString(source["agent"]),
		scope, asString(payload["message"]))
	s.notify(requestID, "pairing", asString(payload["message"]))
	return 202, map[string]any{
		"status": "pending", "request_id": requestID,
		"target": s.publicPeer(""), "expires_in": PendingTTLS,
	}
}

func (s *Server) status(query map[string][]string) (int, any) {
	requestID := first(query["id"])
	peerID := first(query["peer_id"])
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	if item, ok := s.pending[requestID]; ok {
		source, _ := item["source"].(map[string]any)
		if asString(source["peer_id"]) != peerID {
			return 200, map[string]any{"status": "pending"}
		}
		return 200, map[string]any{"status": "pending", "request_id": requestID}
	}
	if item, ok := s.executionPending[requestID]; ok {
		if asString(item["peer_id"]) != peerID {
			return 404, map[string]any{"error": "执行申请不存在"}
		}
		return 200, map[string]any{"status": "pending", "request_id": requestID}
	}
	if item, ok := s.executionRunning[requestID]; ok {
		if asString(item["peer_id"]) != peerID {
			return 404, map[string]any{"error": "执行申请不存在"}
		}
		return 200, map[string]any{"status": "running", "request_id": requestID}
	}
	if result, ok := s.completed[requestID]; ok {
		if asString(result["peer_id"]) != peerID {
			return 404, map[string]any{"error": "配对申请不存在"}
		}
		return 200, result
	}
	result, ok := s.executionCompleted[requestID]
	if !ok || asString(result["peer_id"]) != peerID {
		return 404, map[string]any{"error": "执行申请不存在"}
	}
	return 200, map[string]any{"status": asString(result["status"]), "request_id": requestID}
}

func (s *Server) local(w http.ResponseWriter, r *http.Request, path string, payload map[string]any) (int, any) {
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !isLoopback(remoteIP) || r.Header.Get("X-Lanbus-Approval") != s.approvalSecret {
		return 403, map[string]any{"error": "只能在目标机器本机审批"}
	}
	if path == "/peer/inbox" {
		s.mu.Lock()
		s.expireLocked()
		requests := []map[string]any{}
		for id, item := range s.pending {
			entry := copyGrant(item)
			entry["request_id"] = id
			requests = append(requests, entry)
		}
		for id, item := range s.executionPending {
			entry := copyGrant(item)
			entry["request_id"] = id
			requests = append(requests, entry)
		}
		s.mu.Unlock()
		return 200, map[string]any{"requests": requests, "grants": s.Grants.ListGrants()}
	}
	requestID := asString(payload["request_id"])
	if path == "/peer/revoke" {
		removed := s.Grants.Revoke(asString(payload["peer_id"]))
		return 200, map[string]any{"ok": removed}
	}
	s.mu.Lock()
	execution, executionOK := s.executionPending[requestID]
	if executionOK {
		delete(s.executionPending, requestID)
		status := "denied"
		if path == "/peer/approve" {
			status = "approved"
		}
		if path != "/peer/approve" && path != "/peer/deny" {
			s.mu.Unlock()
			return 404, map[string]any{"error": "未知审批操作"}
		}
		execution["status"] = status
		s.executionCompleted[requestID] = execution
		s.mu.Unlock()
		return 200, map[string]any{"ok": true, "status": status}
	}
	pending, ok := s.pending[requestID]
	if ok {
		delete(s.pending, requestID)
	}
	s.mu.Unlock()
	if !ok {
		return 404, map[string]any{"error": "配对申请不存在或已处理"}
	}
	source, _ := pending["source"].(map[string]any)
	if path == "/peer/deny" {
		s.mu.Lock()
		s.completed[requestID] = map[string]any{"status": "denied", "peer_id": asString(source["peer_id"])}
		s.mu.Unlock()
		return 200, map[string]any{"ok": true, "status": "denied"}
	}
	if path != "/peer/approve" {
		return 404, map[string]any{"error": "未知审批操作"}
	}
	companyJoin := asString(pending["kind"]) == "company"
	mode, scope := asString(payload["mode"]), asString(payload["scope"])
	if companyJoin {
		mode, scope = "company", asString(pending["scope"])
	}
	if (mode != "pair" && mode != "session" && mode != "company") || (scope != "chat" && scope != "project") {
		return 400, map[string]any{"error": "请选择 pair/session 和 chat/project"}
	}
	if scope == "project" && asString(pending["scope"]) != "project" {
		return 400, map[string]any{"error": "不能把仅交流申请提升为项目任务"}
	}
	cwdInput := asString(payload["cwd"])
	if cwdInput == "" {
		cwdInput = asString(pending["cwd"])
	}
	if cwdInput == "" {
		cwdInput = s.cwd
	}
	cwd, _ := realpath(cwdInput)
	if scope == "project" && !isDir(cwd) {
		return 400, map[string]any{"error": "授权工作目录不存在"}
	}
	grant, err := s.makeGrant(pending, mode, scope, cwd)
	if err != nil {
		return 500, map[string]any{"error": err.Error()}
	}
	completed := map[string]any{"status": "approved", "peer_id": asString(source["peer_id"]), "grant": grant}
	response := map[string]any{"ok": true, "status": "approved", "grant": grant}
	if companyJoin {
		key, err := s.Identity.SessionKey(asString(grant["x_public"]), asString(grant["initiator_nonce"]), asString(grant["target_nonce"]))
		if err != nil {
			return 500, map[string]any{"error": err.Error()}
		}
		packet, err := EncryptPacket(key, map[string]any{"company_id": s.Company.ID,
			"company_name": s.Company.Name, "secret": s.Company.Secret, "members": []any{}},
			"company:"+asString(grant["grant_id"]))
		if err != nil {
			return 500, map[string]any{"error": err.Error()}
		}
		completed["company_packet"] = packet
		response["company_packet"] = packet
	}
	s.mu.Lock()
	s.completed[requestID] = completed
	s.mu.Unlock()
	return 200, response
}

func (s *Server) directRequest(body map[string]any) (int, any) {
	grantID := asString(body["grant_id"])
	requestID := asString(body["request_id"])
	grant := s.Grants.Grant(grantID, "")
	if grant == nil {
		return 401, map[string]any{"error": "授权不存在、已过期或已撤销"}
	}
	if requestID == "" || len(requestID) > 128 {
		return 400, map[string]any{"error": "无效 request_id"}
	}
	requestV2 := asFloat(body["request_version"]) == 2 && s.cfg.RequestPolicy == "each"
	rawBody, _ := Canonical(body)
	digest := sha256Sum(rawBody)
	s.mu.Lock()
	if requestV2 {
		stores := []map[string]map[string]any{s.executionPending, s.executionRunning, s.executionCompleted}
		for _, store := range stores {
			if record := store[requestID]; record != nil && asString(record["digest"]) != digest {
				s.mu.Unlock()
				return 409, map[string]any{"error": "相同 request_id 的请求内容已改变"}
			}
		}
		if s.executionPending[requestID] != nil {
			s.mu.Unlock()
			return 202, map[string]any{"status": "approval_required", "request_id": requestID}
		}
		if s.executionRunning[requestID] != nil {
			s.mu.Unlock()
			return 202, map[string]any{"status": "execution_running", "request_id": requestID}
		}
		if completed := s.executionCompleted[requestID]; completed != nil {
			switch asString(completed["status"]) {
			case "denied":
				s.mu.Unlock()
				return 403, map[string]any{"error": "目标机器已拒绝本次执行"}
			case "expired":
				s.mu.Unlock()
				return 410, map[string]any{"error": "本次执行审批已过期"}
			case "failed":
				s.mu.Unlock()
				return 500, map[string]any{"error": "直连执行失败：" + asString(completed["error"])}
			case "completed":
				packet := completed["packet"]
				s.mu.Unlock()
				return 200, map[string]any{"packet": packet}
			case "approved":
				s.executionRunning[requestID] = completed
				delete(s.executionCompleted, requestID)
			}
		}
	} else if s.seenRequests[requestID] {
		s.mu.Unlock()
		return 409, map[string]any{"error": "重复请求已拒绝"}
	}
	s.mu.Unlock()
	key, err := s.Identity.SessionKey(asString(grant["x_public"]),
		asString(grant["initiator_nonce"]), asString(grant["target_nonce"]))
	if err != nil {
		return 400, map[string]any{"error": err.Error()}
	}
	aad := grantID + ":" + requestID
	packet, _ := body["packet"].(map[string]any)
	if packet == nil {
		packet = map[string]any{}
	}
	payload, err := DecryptPacket(key, packet, aad)
	if err != nil {
		return 400, map[string]any{"error": err.Error()}
	}
	scope := asString(payload["scope"])
	if scope != "chat" && scope != "project" {
		return 400, map[string]any{"error": "无效 scope"}
	}
	if asString(grant["scope"]) != "project" && scope != asString(grant["scope"]) {
		return 403, map[string]any{"error": "请求 scope 超出授权范围"}
	}
	cwd := s.cwd
	if scope == "project" {
		requested := asString(payload["cwd"])
		if requested == "" {
			requested = s.cwd
		}
		cwd, _ = realpath(requested)
		allowed := false
		if cwds, ok := grant["cwds"].([]any); ok {
			for _, item := range cwds {
				parent, _ := realpath(asString(item))
				if cwd == parent || strings.HasPrefix(cwd, parent+string(os.PathSeparator)) {
					allowed = true
					break
				}
			}
		}
		if !allowed || !isDir(cwd) {
			return 403, map[string]any{"error": "请求工作目录超出授权范围"}
		}
	}
	message := trimSpace(asString(payload["message"]))
	if message == "" || len([]rune(message)) > 40000 {
		return 400, map[string]any{"error": "消息为空或超过 40000 字符"}
	}
	s.mu.Lock()
	if requestV2 {
		if s.executionRunning[requestID] == nil {
			sourceAgent := asString(grant["source_agent"])
			if sourceAgent == "" {
				sourceAgent = "lanbus-cli"
			}
			s.executionPending[requestID] = map[string]any{
				"kind": "execution", "source": map[string]any{
					"peer_id": asString(grant["peer_id"]), "hostname": asString(grant["source_hostname"]),
					"agent": sourceAgent,
				},
				"message": message, "scope": scope, "cwd": cwd,
				"created_at": PyFloat(now()), "expires_at": PyFloat(now() + PendingTTLS),
				"peer_id": asString(grant["peer_id"]), "grant_id": grantID,
				"digest": digest, "payload": payload,
			}
		}
	} else {
		if s.seenRequests[requestID] {
			s.mu.Unlock()
			return 409, map[string]any{"error": "重复请求已拒绝"}
		}
		s.seenRequests[requestID] = true
	}
	isPending := requestV2 && s.executionPending[requestID] != nil
	s.mu.Unlock()
	if isPending {
		fmt.Printf("[peer:%s] 待批准执行 %s: %.120s\n", s.cfg.Name, requestID, message)
		s.notify(requestID, "execution", message)
		return 202, map[string]any{"status": "approval_required", "request_id": requestID}
	}
	timeout := asFloat(payload["timeout"])
	if timeout <= 0 {
		timeout = 300
	}
	if timeout > 900 {
		timeout = 900
	}
	result := s.execute(message, scope, cwd, timeout)
	encrypted, err := EncryptPacket(key, result, aad)
	if err != nil {
		return 500, map[string]any{"error": "直连执行失败：" + err.Error()}
	}
	if requestV2 {
		s.mu.Lock()
		running := s.executionRunning[requestID]
		delete(s.executionRunning, requestID)
		running["status"] = "completed"
		running["packet"] = encrypted
		s.executionCompleted[requestID] = running
		s.mu.Unlock()
	}
	return 200, map[string]any{"packet": encrypted}
}

func (s *Server) execute(message, scope, cwd string, timeout float64) map[string]any {
	if s.cfg.Executor != nil {
		return s.cfg.Executor(message, scope, cwd, timeout)
	}
	return s.shellExecute(message, cwd, timeout)
}

// shellExecute is the standalone fallback executor (worker injects its own
// streaming executor): prompt file + {prompt_file} substitution or stdin.
func (s *Server) shellExecute(message, cwd string, timeout float64) map[string]any {
	tmp, err := os.CreateTemp("", "lanbus-peer-*.md")
	if err != nil {
		return map[string]any{"ok": false, "exit_code": 1, "reply": err.Error(), "peer": s.cfg.Name}
	}
	promptFile := tmp.Name()
	defer os.Remove(promptFile)
	tmp.WriteString(message)
	tmp.Close()
	command := strings.ReplaceAll(s.cfg.Command, "{prompt_file}", promptFile)
	stdinText := message
	if strings.Contains(s.cfg.Command, "{prompt_file}") {
		stdinText = ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout*float64(time.Second)))
	defer cancel()
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Dir = cwd
	if stdinText != "" {
		cmd.Stdin = strings.NewReader(stdinText)
	}
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else {
			code = 1
		}
	}
	reply := string(out)
	if runes := []rune(reply); len(runes) > 20000 {
		reply = string(runes[len(runes)-20000:])
	}
	return map[string]any{"ok": code == 0, "exit_code": code, "reply": reply, "peer": s.cfg.Name}
}

func isLoopback(address string) bool {
	ip := net.ParseIP(address)
	return ip != nil && ip.IsLoopback()
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func first(values []string) string {
	if len(values) > 0 {
		return values[0]
	}
	return ""
}
