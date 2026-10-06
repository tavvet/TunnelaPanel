package endpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)

type savedState struct {
	Version int      `json:"version"`
	Hash    string   `json:"credentials_hash"`
	Applied snapshot `json:"applied"`
	Draft   snapshot `json:"draft"`
}

// store has no mutex of its own; shared access must hold the owning Agent's mu.
type store struct {
	path      string
	statePath string

	// Applied state includes disabled users; the active file contains only its
	// enabled subset. Draft edits are persisted without changing that file.
	applied document
	draft   document

	// hash identifies the exact active-file bytes, including TOML formatting,
	// so external edits cannot be silently overwritten by a pending draft.
	hash string
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func newStore(credentials, stateDir string) (*store, error) {
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return nil, errors.New("cannot create state directory")
	}
	if err := os.Chmod(stateDir, 0700); err != nil {
		return nil, errors.New("cannot secure state directory")
	}
	b, err := os.ReadFile(credentials)
	if err != nil {
		return nil, errors.New("cannot read credentials")
	}
	d, err := parseDocument(b)
	if err != nil {
		return nil, err
	}

	s := &store{
		path:      credentials,
		statePath: filepath.Join(stateDir, "state.json"),
		applied:   d,
		draft:     clone(d),
		hash:      digest(b),
	}
	stateBytes, err := os.ReadFile(s.statePath)
	if err == nil {
		var saved savedState
		if json.Unmarshal(stateBytes, &saved) != nil || saved.Version != 1 {
			return nil, errors.New("invalid agent state")
		}
		if saved.Hash != s.hash {
			return nil, errors.New("credentials changed externally; restore the matching credentials backup before starting the agent")
		}
		s.applied, err = restore(saved.Applied)
		if err != nil {
			return nil, err
		}
		s.draft, err = restore(saved.Draft)
		if err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, errors.New("cannot read agent state")
	}
	if err := s.persist(); err != nil {
		return nil, err
	}
	return s, nil
}

// atomicWrite makes the replacement durable by syncing both the file and the
// directory entry. All files written here can contain credentials.
func atomicWrite(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tunnelapanel-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// durableRemove syncs journal deletion so a committed apply stays committed
// after a restart.
func durableRemove(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *store) persist() error {
	a, err := s.applied.save()
	if err != nil {
		return err
	}
	d, err := s.draft.save()
	if err != nil {
		return err
	}
	b, err := json.Marshal(savedState{
		Version: 1,
		Hash:    s.hash,
		Applied: a,
		Draft:   d,
	})
	if err != nil {
		return err
	}
	if atomicWrite(s.statePath, b) != nil {
		return errors.New("cannot save agent state")
	}
	return nil
}

func (s *store) drift() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return errors.New("cannot read credentials")
	}
	if digest(b) != s.hash {
		return errors.New("credentials changed outside the panel; refusing to overwrite them")
	}
	return nil
}

func (s *store) pending() bool {
	a, _ := s.applied.save()
	d, _ := s.draft.save()
	return !reflect.DeepEqual(a, d)
}

func (s *store) list() protocol.Users {
	out := protocol.Users{Users: []protocol.User{}, Pending: s.pending()}
	for _, u := range s.draft.Users {
		p := protocol.User{Username: u.Fields["username"].(string), Enabled: u.Enabled}
		if n, ok := u.Fields["max_http2_conns"].(int64); ok && n >= 0 && n <= 1<<32-1 {
			v := uint32(n)
			p.MaxHTTP2Conns = &v
		}
		if n, ok := u.Fields["max_http3_conns"].(int64); ok && n >= 0 && n <= 1<<32-1 {
			v := uint32(n)
			p.MaxHTTP3Conns = &v
		}
		out.Users = append(out.Users, p)
	}
	return out
}

func validPassword(p string) bool {
	if len(p) < 12 || len(p) > 256 || strings.TrimSpace(p) != p {
		return false
	}
	for _, r := range p {
		if r < 32 || r > 126 || r == '"' || r == '\\' {
			return false
		}
	}
	return true
}

func (s *store) change(method string, c protocol.UserChange) error {
	if !usernamePattern.MatchString(c.Username) {
		return errors.New("username must contain 1–64 letters, digits, _, ., @ or -")
	}
	if c.Password != "" && !validPassword(c.Password) {
		return errors.New("password must be 12–256 printable ASCII characters, without quotes, backslashes or surrounding whitespace")
	}

	old := clone(s.draft)
	idx := -1
	for i, u := range s.draft.Users {
		if u.Fields["username"] == c.Username {
			idx = i
			break
		}
	}

	switch method {
	case "POST":
		if idx >= 0 {
			return errors.New("user already exists")
		}
		if !validPassword(c.Password) {
			return errors.New("a valid password is required")
		}
		enabled := true
		if c.Enabled != nil {
			enabled = *c.Enabled
		}
		s.draft.Users = append(s.draft.Users, managedUser{
			Fields: map[string]any{
				"username": c.Username,
				"password": c.Password,
			},
			Enabled: enabled,
		})
	case "PATCH":
		if idx < 0 {
			return errors.New("user not found")
		}
		if c.Password != "" {
			s.draft.Users[idx].Fields["password"] = c.Password
		}
		if c.Enabled != nil {
			s.draft.Users[idx].Enabled = *c.Enabled
		}
	case "DELETE":
		if idx < 0 {
			return errors.New("user not found")
		}
		s.draft.Users = append(s.draft.Users[:idx], s.draft.Users[idx+1:]...)
	default:
		return errors.New("unsupported user operation")
	}

	if enabledCount(s.draft) == 0 {
		s.draft = old
		return errors.New("at least one enabled user must remain")
	}
	if err := s.persist(); err != nil {
		s.draft = old
		return err
	}
	return nil
}
