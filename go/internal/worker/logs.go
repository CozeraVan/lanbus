package worker

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type logSender struct {
	w       *Worker
	tid     string
	claim   string
	seq     int
	mu      sync.Mutex
	flushMu sync.Mutex
	pending []logLine
	stop    chan struct{}
	local   *os.File
}

type logLine struct {
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

func newLogSender(w *Worker, t *Task) *logSender {
	ls := &logSender{w: w, tid: t.ID, claim: t.ClaimToken, seq: t.LogSeq, stop: make(chan struct{})}
	os.MkdirAll(w.logDir, 0755)
	ls.local, _ = os.OpenFile(filepath.Join(w.logDir, t.ID+"-"+strconv.FormatInt(time.Now().Unix(), 10)+".log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	go ls.loop()
	return ls
}

func (l *logSender) write(stream, text string) {
	l.mu.Lock()
	l.pending = append(l.pending, logLine{Stream: stream, Text: text})
	if l.local != nil {
		l.local.WriteString("[" + stream + "] " + text + "\n")
	}
	l.mu.Unlock()
}

func (l *logSender) loop() {
	ticker := time.NewTicker(700 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			l.flush()
			return
		case <-ticker.C:
			l.flush()
		}
	}
}

func (l *logSender) flush() {
	l.flushMu.Lock()
	defer l.flushMu.Unlock()
	l.mu.Lock()
	if len(l.pending) == 0 {
		l.mu.Unlock()
		return
	}
	batch := append([]logLine{}, l.pending...)
	seq := l.seq
	l.pending = nil
	l.mu.Unlock()
	lines := []map[string]any{}
	for i, ln := range batch {
		lines = append(lines, map[string]any{"seq": seq + i + 1, "stream": ln.Stream, "text": limit(ln.Text, 20000)})
	}
	if _, err := l.w.postLog(l.tid, l.claim, lines); err != nil {
		l.mu.Lock()
		l.pending = append(batch, l.pending...)
		l.mu.Unlock()
		return
	}
	l.mu.Lock()
	l.seq = seq + len(batch)
	l.mu.Unlock()
}

func (l *logSender) close() {
	close(l.stop)
	time.Sleep(800 * time.Millisecond)
	l.flush()
	if l.local != nil {
		l.local.Close()
	}
}

func (w *Worker) logLinesFor(lines []string) []map[string]any {
	_ = strings.TrimSpace
	return nil
}
