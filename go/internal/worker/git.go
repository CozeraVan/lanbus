package worker

import (
	"strings"
)

func (w *Worker) gitSnapshot(cwd string) (string, string) {
	if cwd == "" {
		return "", "(cwd 不存在)"
	}
	if _, err := execLookPath("git"); err != nil {
		return "", "(无 git)"
	}
	status := capture("git", "-C", cwd, "status", "--porcelain")
	if status == "" {
		// git could fail or be clean; keep empty changed
	}
	diff := capture("git", "-C", cwd, "diff", "HEAD", "--", ".")
	if diff == "" {
		diff = capture("git", "-C", cwd, "diff", "--", ".")
	}
	changed := []string{}
	for _, line := range strings.Split(strings.TrimRight(status, "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			changed = append(changed, line)
		}
	}
	return limit(diff, 200000), limit(strings.Join(changed, "\n"), 8000)
}
