package endpoint

import (
	"regexp"
	"strings"
	"sync"

	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

// logRing buffers bounded complete lines from child stdout/stderr. Its mutex also
// protects partial lines and all passwords learned during this agent lifetime.
type logRing struct {
	mu      sync.Mutex
	lines   []string
	partial string
	secrets map[string]bool
}

var secretField = regexp.MustCompile(`(?i)(password|authorization|token)(\s*[=:]\s*)[^\s,;]+`)

// redact requires r.mu; callers may be reading logs while a new secret is added.
func (r *logRing) redact(s string) string {
	for secret := range r.secrets {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[redacted]")
		}
	}
	return secretField.ReplaceAllString(s, "$1$2[redacted]")
}

func (r *logRing) addSecrets(d document) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.secrets == nil {
		r.secrets = map[string]bool{}
	}
	for _, u := range d.Users {
		if p, ok := u.Fields["password"].(string); ok && p != "" {
			r.secrets[p] = true
		}
	}
}

// Write accepts process output in arbitrary chunks and redacts complete lines.
func (r *logRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.partial += string(p)
	for {
		i := strings.IndexByte(r.partial, '\n')
		if i < 0 {
			break
		}
		line := r.partial[:i]
		r.partial = r.partial[i+1:]
		line = r.redact(line)
		if len(line) > 8192 {
			line = line[:8192] + " [truncated]"
		}
		r.lines = append(r.lines, line)
		if len(r.lines) > 200 {
			r.lines = r.lines[len(r.lines)-200:]
		}
	}
	if len(r.partial) > 8192 {
		r.partial = "[oversized log line omitted]"
	}
	return len(p), nil
}

func (r *logRing) read() protocol.LogResponse {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := protocol.LogResponse{Lines: make([]string, len(r.lines))}
	for i, line := range r.lines {
		// A secret learned after the line was stored must also be removed.
		out.Lines[i] = r.redact(line)
	}
	return out
}
