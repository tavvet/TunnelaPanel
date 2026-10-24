package panel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// The log rotates at auditMaxSize and keeps auditCopies older files,
	// audit.jsonl.1 (newest) to audit.jsonl.N (oldest).
	auditMaxSize = 2 << 20
	auditCopies  = 5

	// At most failedAuditsPerMinute login_failed records are written per
	// minute. Further failures are counted and reported in one
	// login_failed_suppressed record with the next failure after that minute,
	// so a distributed flood cannot rotate earlier records away.
	failedAuditsPerMinute = 60
)

// audit records operation outcomes without credentials or request bodies.
// Logging is best effort and does not change the result of the operation.
func (s *Server) audit(action, ip string, ok bool) {
	s.writeAudit(map[string]any{
		"time":    time.Now().UTC(),
		"action":  action,
		"ip":      ip,
		"success": ok,
	})
}

// auditFailedLogin applies the per-minute limit to login_failed records.
func (s *Server) auditFailedLogin(ip string) {
	now := time.Now()
	s.auditMu.Lock()
	var suppressed int
	if now.Sub(s.failedWindow) >= time.Minute {
		suppressed, s.failedSuppressed = s.failedSuppressed, 0
		s.failedWindow, s.failedCount = now, 0
	}

	write := s.failedCount < failedAuditsPerMinute
	if write {
		s.failedCount++
	} else {
		s.failedSuppressed++
	}
	s.auditMu.Unlock()

	if suppressed > 0 {
		s.writeAudit(map[string]any{
			"time":    now.UTC(),
			"action":  "login_failed_suppressed",
			"count":   suppressed,
			"success": false,
		})
	}

	if write {
		s.audit("login_failed", ip, false)
	}
}

func (s *Server) writeAudit(record map[string]any) {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()

	path := filepath.Join(s.config.StateDir, "audit.jsonl")
	if st, err := os.Stat(path); err == nil && st.Size() > auditMaxSize {
		// Renaming over the oldest copy drops it.
		for i := auditCopies - 1; i >= 1; i-- {
			_ = os.Rename(fmt.Sprintf("%s.%d", path, i), fmt.Sprintf("%s.%d", path, i+1))
		}

		_ = os.Rename(path, path+".1")
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}

	defer f.Close()
	_ = json.NewEncoder(f).Encode(record)
}
