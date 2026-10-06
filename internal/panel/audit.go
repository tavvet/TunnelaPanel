package panel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// audit records operation outcomes without credentials or request bodies.
// Logging is best effort and does not change the result of the operation.
func (s *Server) audit(action, ip string, ok bool) {
	s.auditMu.Lock()
	defer s.auditMu.Unlock()

	path := filepath.Join(s.config.StateDir, "audit.jsonl")
	if st, err := os.Stat(path); err == nil && st.Size() > 2<<20 {
		_ = os.Rename(path, path+".1")
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}

	defer f.Close()
	_ = json.NewEncoder(f).Encode(map[string]any{
		"time":    time.Now().UTC(),
		"action":  action,
		"ip":      ip,
		"success": ok,
	})
}
