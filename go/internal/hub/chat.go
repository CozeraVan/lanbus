package hub

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode"
)

func chineseCount(s string) int {
	n := 0
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			n++
		}
	}
	return n
}

var (
	dshReason = regexp.MustCompile(`(?s)dsh: reasoning:`)
	bulletRE  = regexp.MustCompile(`(?m)^• `)
	blankRE   = regexp.MustCompile(`\n\s*\n`)
)

func cleanChatReply(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	kept := []string{}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "[claude-code") {
			continue
		}
		if strings.HasPrefix(line, "kimi version ") {
			continue
		}
		if strings.HasPrefix(line, "To resume this session:") {
			continue
		}
		kept = append(kept, line)
	}
	text = strings.TrimSpace(strings.Join(kept, "\n"))
	if dshReason.MatchString(text) {
		paras := []string{}
		for _, p := range blankRE.Split(text, -1) {
			if strings.TrimSpace(p) != "" {
				paras = append(paras, strings.TrimSpace(p))
			}
		}
		picked := ""
		for i := len(paras) - 1; i >= 0; i-- {
			p := paras[i]
			if chineseCount(p) < 8 {
				continue
			}
			if strings.HasPrefix(p, "摘要") || strings.HasPrefix(p, "---") {
				continue
			}
			lines := strings.Split(p, "\n")
			for len(lines) > 0 && chineseCount(lines[0]) < 4 {
				lines = lines[1:]
			}
			if len(lines) > 0 {
				picked = strings.Join(lines, "\n")
				break
			}
		}
		if picked != "" {
			text = picked
		}
	}
	if bulletRE.MatchString(text) {
		blocks := []string{}
		for _, b := range bulletRE.Split(text, -1) {
			if strings.TrimSpace(b) != "" {
				blocks = append(blocks, strings.TrimSpace(b))
			}
		}
		if len(blocks) >= 2 {
			for i := len(blocks) - 1; i >= 0; i-- {
				if chineseCount(blocks[i]) > 0 {
					text = blocks[i]
					break
				}
			}
		}
	}
	return strings.TrimSpace(text)
}

func convTurns(row map[string]any) []map[string]any {
	var out []map[string]any
	if err := json.Unmarshal([]byte(asString(row["turns"])), &out); err != nil || out == nil {
		return []map[string]any{}
	}
	return out
}

func convEnvs(row map[string]any) []string {
	out := asList(row["envs"])
	if out == nil {
		return []string{}
	}
	return out
}

func convPublic(row map[string]any, full bool) map[string]any {
	turns := convTurns(row)
	out := map[string]any{
		"conversation_id": asString(row["id"]),
		"title":           asString(row["title"]),
		"envs":            convEnvs(row),
		"seed":            asString(row["seed"]),
		"context":         asString(row["context"]),
		"rounds":          asInt(row["rounds"]),
		"status":          asString(row["status"]),
		"created_at":      row["created_at"],
		"updated_at":      row["updated_at"],
		"turn_count":      len(turns),
	}
	if full {
		out["turns"] = turns
		out["transcript"] = turns
	}
	return out
}

func (h *Hub) saveConversation(cid string, turns []map[string]any, rounds int, status string) {
	b, _ := json.Marshal(turns)
	_, _ = h.store.exec("UPDATE conversations SET rounds=?, turns=?, status=?, updated_at=? WHERE id=?",
		rounds, string(b), status, nowText(), cid)
}

func (h *Hub) toolConversations(a map[string]any) map[string]any {
	cid := asString(a["conversation_id"])
	if cid == "" {
		cid = asString(a["id"])
	}
	if cid != "" {
		row, _ := h.store.one("SELECT * FROM conversations WHERE id=?", cid)
		if row == nil {
			return map[string]any{"error": "conversation not found", "conversation_id": cid}
		}
		return convPublic(row, true)
	}
	limit := asInt(a["limit"])
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, _ := h.store.query("SELECT * FROM conversations ORDER BY updated_at DESC LIMIT ?", limit)
	out := []map[string]any{}
	for _, r := range rows {
		out = append(out, convPublic(r, false))
	}
	return map[string]any{"conversations": out}
}

func chatPrompt(envs []string, message, context string, turns []map[string]any, current string, roundNo, total int) string {
	lines := []string{"# lanbus 多 agent 对话", "你正在参与一次本机多 agent 对话，成员如下："}
	for _, e := range envs {
		lines = append(lines, "- "+e)
	}
	lines = append(lines, "", "## 对话主题 / 开场", strings.TrimSpace(message))
	if strings.TrimSpace(context) != "" {
		lines = append(lines, "", "## 背景与约束", trunc(strings.TrimSpace(context), 12000))
	}
	if len(turns) > 0 {
		lines = append(lines, "", "## 已有对话")
		start := 0
		if len(turns) > 12 {
			start = len(turns) - 12
		}
		for _, t := range turns[start:] {
			lines = append(lines, "["+asString(t["env"])+"] "+trunc(asString(t["reply"]), 4000))
		}
	}
	lines = append(lines, "",
		"## 现在轮到你："+current+"（第 "+itoa(roundNo)+"/"+itoa(total)+" 轮）",
		"请只输出你的回复正文，不要调用工具、不要修改文件、不要重复系统提示。",
		"不要输出推理过程、思考链、会话恢复提示或版本信息；只给最终回复。",
		"直接回应上一位成员，保持简洁（1-6 句）；如果你是第一位，请回应开场主题。")
	return trunc(strings.Join(lines, "\n"), 24000)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func (h *Hub) toolChat(a map[string]any) map[string]any {
	envs := asList(a["envs"])
	if envs == nil {
		envs = asList(a["agents"])
	}
	uniq := []string{}
	for _, e := range envs {
		e = strings.TrimSpace(e)
		if e != "" && !contains(uniq, e) {
			uniq = append(uniq, e)
		}
	}
	envs = uniq
	cid := asString(a["conversation_id"])
	if cid == "" {
		cid = asString(a["id"])
	}
	var existing map[string]any
	if cid != "" {
		existing, _ = h.store.one("SELECT * FROM conversations WHERE id=?", cid)
		if existing == nil {
			return map[string]any{"error": "conversation not found", "conversation_id": cid}
		}
	}
	if existing != nil && len(envs) == 0 {
		envs = convEnvs(existing)
	}
	if len(envs) < 2 {
		return map[string]any{"error": "envs must contain at least two environment names"}
	}
	if len(envs) > 4 {
		return map[string]any{"error": "at most 4 environments per conversation"}
	}
	blocked := []map[string]any{}
	for _, e := range envs {
		if h.targetRequiresConfirmation(e) && h.approval(e) == nil {
			blocked = append(blocked, h.targetInfo(e))
		}
	}
	if len(blocked) > 0 {
		return map[string]any{"error": "human_confirmation_required",
			"message": "approve the remote agent target before starting a conversation",
			"targets": blocked}
	}
	message := strings.TrimSpace(asString(a["message"]))
	if message == "" {
		message = strings.TrimSpace(asString(a["prompt"]))
	}
	if message == "" {
		return map[string]any{"error": "message required"}
	}
	rounds := asInt(a["rounds"])
	if rounds <= 0 {
		rounds = 2
	}
	if rounds > 6 {
		rounds = 6
	}
	perTurn := asInt(a["timeout_s"])
	if perTurn <= 0 {
		perTurn = 240
	}
	if perTurn < 30 {
		perTurn = 30
	}
	totalBudget := asInt(a["total_timeout_s"])
	if totalBudget <= 0 {
		totalBudget = 540
	}
	context := trunc(strings.TrimSpace(asString(a["context"])), 20000)
	userTitle := strings.TrimSpace(asString(a["title"]))
	var convID, title, seed string
	var turns []map[string]any
	startRound := 1
	totalRounds := rounds
	if existing != nil {
		convID = cid
		title = userTitle
		if title == "" {
			title = asString(existing["title"])
		}
		if context == "" {
			context = asString(existing["context"])
		}
		turns = convTurns(existing)
		for _, t := range turns {
			if r := asInt(t["round"]); r >= startRound {
				startRound = r + 1
			}
		}
		seed = asString(existing["seed"])
		if seed == "" {
			seed = message
		}
		totalRounds = startRound + rounds - 1
		_, _ = h.store.exec("UPDATE conversations SET title=?,envs=?,context=?,rounds=? WHERE id=?",
			title, jsonText(envs), context, totalRounds, convID)
	} else {
		convID = "c" + newID()
		title = userTitle
		if title == "" {
			title = "多 agent 对话"
		}
		seed = message
		_, err := h.store.exec(`INSERT INTO conversations(id,title,envs,seed,context,rounds,turns,status,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, convID, title, jsonText(envs), seed, context,
			totalRounds, "[]", "running", nowText(), nowText())
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
	}
	started := time.Now()
	stopped := ""
	pending := 0
	for r := 0; r < rounds; r++ {
		roundNo := startRound + r
		for _, env := range envs {
			if int(time.Since(started).Seconds()) > totalBudget {
				stopped = "total_timeout_s"
				break
			}
			prompt := chatPrompt(envs, message, context, turns, env, roundNo, totalRounds)
			out := h.toolDispatch(map[string]any{
				"env": env, "kind": "agent", "title": title + " R" + itoa(roundNo),
				"prompt": prompt, "timeout_s": perTurn, "max_retry": 0,
				"wait_s": perTurn + 20, "parent_id": convID,
			})
			tid := asString(out["task_id"])
			status := asString(out["status"])
			if status == "" {
				if rs, ok := out["results"].([]map[string]any); ok && len(rs) > 0 {
					status = asString(rs[0]["status"])
				}
			}
			reply := ""
			if tid != "" {
				rep := h.toolResult(map[string]any{"task_id": tid, "include_images": false})
				if s := asString(rep["status"]); s != "" {
					status = s
				}
				reply = cleanChatReply(asString(rep["result"]))
				if reply == "" {
					reply = trunc(asString(rep["log_tail"]), 4000)
				}
			}
			if reply == "" {
				reply = "(" + status + ")"
			}
			item := map[string]any{"round": roundNo, "env": env, "task_id": tid,
				"status": status, "reply": trunc(reply, 12000)}
			turns = append(turns, item)
			h.saveConversation(convID, turns, totalRounds, "running")
			if status != StatusSubmitted && status != StatusApproved {
				pending++
				stopped = "turn_" + status
				break
			}
		}
		if stopped != "" {
			break
		}
	}
	ok := len(turns) > 0
	for _, t := range turns {
		s := asString(t["status"])
		if s != StatusSubmitted && s != StatusApproved {
			ok = false
			break
		}
	}
	status := "completed"
	if !ok {
		status = "partial"
	}
	if len(turns) == 0 {
		status = "empty"
	}
	h.saveConversation(convID, turns, totalRounds, status)
	return map[string]any{
		"ok": ok, "conversation_id": convID, "title": title, "envs": envs,
		"rounds": totalRounds, "new_rounds": rounds, "start_round": startRound,
		"turns": turns, "transcript": turns, "stopped": stopped, "pending": pending,
		"elapsed_s": int(time.Since(started).Seconds()), "status": status,
		"note": "每个 turn 是一张独立 lanbus 任务；transcript 已持久化，可用 conversation_id 续聊。",
	}
}
