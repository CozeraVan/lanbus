package hub

import (
	"strings"
	"testing"
)

func newRegressionHub(t *testing.T) *Hub {
	t.Helper()
	h, err := New(Config{DataDir: t.TempDir(), Token: "agent", WorkerToken: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func dispatchRegressionTask(t *testing.T, h *Hub, args map[string]any) string {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	if _, ok := args["prompt"]; !ok {
		args["prompt"] = "regression"
	}
	out := h.toolDispatch(args)
	if out["error"] != nil {
		t.Fatalf("dispatch: %#v", out)
	}
	return asString(out["task_id"])
}

func registerRegressionWorker(t *testing.T, h *Hub, name string) {
	t.Helper()
	_, err := h.store.exec("INSERT INTO workers(name,env,caps,last_seen) VALUES(?,'','[]',?)", name, nowText())
	if err != nil {
		t.Fatal(err)
	}
}

func TestExplicitZeroLimitsArePreserved(t *testing.T) {
	h := newRegressionHub(t)
	tid := dispatchRegressionTask(t, h, map[string]any{"max_retry": 0, "max_recoveries": 0})
	row, err := h.store.one("SELECT * FROM tasks WHERE id=?", tid)
	if err != nil {
		t.Fatal(err)
	}
	if asInt(row["max_retry"]) != 0 || asInt(row["max_recoveries"]) != 0 {
		t.Fatalf("zero limits lost: %#v", row)
	}
}

func TestReworkRejectsNonSubmittedTask(t *testing.T) {
	h := newRegressionHub(t)
	tid := dispatchRegressionTask(t, h, nil)
	got := h.toolReview(map[string]any{"task_id": tid, "verdict": "rework"})
	if got["error"] == nil {
		t.Fatalf("expected error: %#v", got)
	}
	row, _ := h.store.one("SELECT status FROM tasks WHERE id=?", tid)
	if asString(row["status"]) != StatusQueued {
		t.Fatalf("status changed: %#v", row)
	}
}

func TestZeroMaxRetryFailsFirstRework(t *testing.T) {
	h := newRegressionHub(t)
	tid := dispatchRegressionTask(t, h, map[string]any{"max_retry": 0})
	_, _ = h.store.exec("UPDATE tasks SET status='submitted' WHERE id=?", tid)
	got := h.toolReview(map[string]any{"task_id": tid, "verdict": "rework"})
	if got["status"] != StatusFailed {
		t.Fatalf("zero max retry was replaced by default: %#v", got)
	}
}

func TestReworkAccumulatesFeedbackAndClearsEvidence(t *testing.T) {
	h := newRegressionHub(t)
	tid := dispatchRegressionTask(t, h, map[string]any{"max_retry": 2})
	_, _ = h.store.exec("UPDATE tasks SET status='submitted',feedback='old',result='stale',diff='stale',verify_out='stale' WHERE id=?", tid)
	_, _ = h.store.exec("INSERT INTO artifacts(task_id,name,relpath,path,mime,size,created_at) VALUES(?,?,?,?,?,?,?)", tid, "old.txt", "old.txt", "/old.txt", "text/plain", 1, nowText())
	got := h.toolReview(map[string]any{"task_id": tid, "verdict": "rework", "feedback": "new"})
	if got["status"] != StatusQueued {
		t.Fatalf("rework: %#v", got)
	}
	row, _ := h.store.one("SELECT * FROM tasks WHERE id=?", tid)
	feedback := asString(row["feedback"])
	if !strings.Contains(feedback, "old") || !strings.Contains(feedback, "new") {
		t.Fatalf("feedback: %q", feedback)
	}
	if asString(row["result"]) != "" || asString(row["diff"]) != "" || asString(row["verify_out"]) != "" {
		t.Fatalf("evidence retained: %#v", row)
	}
	artifacts, _ := h.store.query("SELECT id FROM artifacts WHERE task_id=?", tid)
	if len(artifacts) != 0 {
		t.Fatalf("artifacts retained: %#v", artifacts)
	}
}

func TestZeroRecoveriesFailsFirstExpiredClaim(t *testing.T) {
	h := newRegressionHub(t)
	tid := dispatchRegressionTask(t, h, map[string]any{"max_recoveries": 0})
	registerRegressionWorker(t, h, "zero-recovery")
	task, err := h.claim("zero-recovery", "")
	if err != nil || task == nil || asString(task["id"]) != tid {
		t.Fatalf("claim: %#v %v", task, err)
	}
	if got := h.recoverTask(task, "expired"); got != StatusFailed {
		t.Fatalf("recover=%q", got)
	}
	row, _ := h.store.one("SELECT status FROM tasks WHERE id=?", tid)
	if asString(row["status"]) != StatusFailed {
		t.Fatalf("status: %#v", row)
	}
}

func TestStaleRecoverySnapshotDoesNotReplaceNewClaim(t *testing.T) {
	h := newRegressionHub(t)
	tid := dispatchRegressionTask(t, h, map[string]any{"max_recoveries": 2})
	registerRegressionWorker(t, h, "stale-recovery")
	first, err := h.claim("stale-recovery", "")
	if err != nil || first == nil {
		t.Fatalf("first claim: %#v %v", first, err)
	}
	if got := h.recoverTask(first, "restart"); got != StatusQueued {
		t.Fatalf("first recovery=%q", got)
	}
	second, err := h.claim("stale-recovery", "")
	if err != nil || second == nil {
		t.Fatalf("second claim: %#v %v", second, err)
	}
	if asString(first["claim_token"]) == asString(second["claim_token"]) {
		t.Fatal("claim token did not change")
	}
	if got := h.recoverTask(first, "stale reaper snapshot"); got != "" {
		t.Fatalf("stale recovery=%q", got)
	}
	row, _ := h.store.one("SELECT status,claim_token FROM tasks WHERE id=?", tid)
	if asString(row["status"]) != StatusRunning || asString(row["claim_token"]) != asString(second["claim_token"]) {
		t.Fatalf("new claim overwritten: %#v", row)
	}
}
