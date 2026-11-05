package worker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (w *Worker) composePrompt(t *Task) string {
	cwd := t.CWD
	if cwd == "" {
		cwd = w.cfg.CWD
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# lanbus 远程任务上下文\n")
	fmt.Fprintf(&b, "- task_id: %s\n", t.ID)
	fmt.Fprintf(&b, "- env: %s    worker: %s\n", w.cfg.Env, w.cfg.Name)
	fmt.Fprintf(&b, "- title: %s\n", t.Title)
	fmt.Fprintf(&b, "- kind: %s\n", t.Kind)
	fmt.Fprintf(&b, "- cwd: %s\n", cwd)
	fmt.Fprintf(&b, "- 本轮 retry: %d/%d\n", t.Retry, t.MaxRetry)
	fmt.Fprintf(&b, "- 恢复次数: 0\n")
	if t.Verify != "" {
		fmt.Fprintf(&b, "- 验收命令: `%s`（退出码必须为 0）\n", t.Verify)
	}
	if t.Context != "" {
		b.WriteString("\n## 主 agent 交接的背景与约束\n")
		b.WriteString(limit(t.Context, 40000))
		b.WriteString("\n")
	}
	// local context files
	for _, rel := range []string{".lanbus/context.md", ".lanbus/rules.md"} {
		p := filepath.Join(cwd, rel)
		if st, err := os.Stat(p); err == nil && st.Size() <= 32768 {
			if data, err := os.ReadFile(p); err == nil {
				fmt.Fprintf(&b, "\n### %s\n%s\n", rel, string(data))
			}
		}
	}
	if branch := capture("git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD"); branch != "" {
		fmt.Fprintf(&b, "\n### git branch\n%s\n", strings.TrimSpace(branch))
	}
	if status := capture("git", "-C", cwd, "status", "--porcelain"); status != "" {
		fmt.Fprintf(&b, "\n### git status（执行前）\n%s\n", limit(status, 4000))
	}
	b.WriteString("\n## 本轮任务\n")
	b.WriteString(strings.TrimSpace(t.Prompt))
	if t.Feedback != "" {
		b.WriteString("\n\n## 上一轮验收意见（必须逐条修掉）\n")
		b.WriteString(limit(t.Feedback, 20000))
	}
	b.WriteString("\n\n## 执行约束\n")
	b.WriteString("- 你运行在无人值守的远程环境，不要等待人工输入，不要向用户提问。\n")
	b.WriteString("- 只修改上面 cwd 内的文件；不要动无关文件、不要 git commit/push。\n")
	b.WriteString("- 完成后输出一段简短摘要：改了什么文件、跑了什么验证、剩余风险。\n")
	return limit(b.String(), 120000)
}

func capture(name string, args ...string) string {
	cmd := exec.Command(name, args...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func limit(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
