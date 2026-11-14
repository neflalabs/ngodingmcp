package procman

import (
	"bytes"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ansiRE and oscRE strip terminal control sequences from process output.
var (
	ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	oscRE  = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
)

func sanitizeLine(s string) string {
	s = oscRE.ReplaceAllString(s, "")
	s = ansiRE.ReplaceAllString(s, "")
	if i := strings.LastIndexByte(s, '\r'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimRight(s, "\r")
}

// logBuffer keeps the last maxBytes of output as timestamped lines.
type logBuffer struct {
	mu       sync.Mutex
	maxBytes int
	lines    []LogLine
	partial  []byte
	nextSeq  int64
	bytes    int
	dropped  bool
}

func newLogBuffer(maxBytes int) *logBuffer {
	return &logBuffer{maxBytes: maxBytes}
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.partial = append(b.partial, p...)
	for {
		i := bytes.IndexByte(b.partial, '\n')
		if i < 0 {
			break
		}
		line := string(b.partial[:i])
		b.partial = b.partial[i+1:]
		b.addLineLocked(line)
	}
	if len(b.partial) > b.maxBytes {
		b.partial = append([]byte(nil), b.partial[len(b.partial)-b.maxBytes:]...)
		b.dropped = true
	}
	return len(p), nil
}

func (b *logBuffer) addLineLocked(line string) {
	line = sanitizeLine(line)
	if len(line) > b.maxBytes {
		line = line[:b.maxBytes]
		b.dropped = true
	}
	b.nextSeq++
	b.lines = append(b.lines, LogLine{Seq: b.nextSeq, Time: time.Now(), Text: line})
	b.bytes += len(line) + 1
	for b.bytes > b.maxBytes && len(b.lines) > 1 {
		b.bytes -= len(b.lines[0].Text) + 1
		b.lines = b.lines[1:]
		b.dropped = true
	}
}

func (b *logBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bytes
}

func (b *logBuffer) Dropped() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

func (b *logBuffer) Match(re *regexp.Regexp) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, line := range b.lines {
		if re.MatchString(line.Text) {
			return true
		}
	}
	return len(b.partial) > 0 && re.Match(b.partial)
}

// Read returns the requested slice and the cursor for the next read.
func (b *logBuffer) Read(tailLines int, afterSeq int64) LogsResult {
	b.mu.Lock()
	defer b.mu.Unlock()

	var out []LogLine
	truncated := false
	if afterSeq <= 0 {
		start := 0
		if tailLines > 0 && len(b.lines) > tailLines {
			start = len(b.lines) - tailLines
		}
		out = append(out, b.lines[start:]...)
	} else {
		if len(b.lines) > 0 && b.lines[0].Seq > afterSeq+1 {
			truncated = true
		}
		for _, line := range b.lines {
			if line.Seq > afterSeq {
				out = append(out, line)
			}
		}
	}
	if len(b.partial) > 0 {
		out = append(out, LogLine{Seq: b.nextSeq, Time: time.Now(), Text: sanitizeLine(string(b.partial))})
	}
	return LogsResult{
		Lines:     out,
		NextSeq:   b.nextSeq,
		Truncated: truncated,
		Dropped:   b.dropped,
	}
}
