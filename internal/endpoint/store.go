package endpoint

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,64}$`)

type managedUser struct {
	Fields  map[string]any
	Enabled bool
}
type document struct {
	Root  map[string]any
	Users []managedUser
}
type snapshot struct {
	TOML    string          `json:"toml"`
	Enabled map[string]bool `json:"enabled"`
}
type savedState struct {
	Version int      `json:"version"`
	Hash    string   `json:"credentials_hash"`
	Applied snapshot `json:"applied"`
	Draft   snapshot `json:"draft"`
}
type store struct {
	path, statePath string
	applied, draft  document
	hash            string
}

func parseDocument(b []byte) (document, error) {
	var root map[string]any
	if err := toml.Unmarshal(b, &root); err != nil {
		return document{}, errors.New("invalid credentials TOML")
	}
	doc := document{Root: root}
	clients, ok := root["client"].([]any)
	if !ok {
		return doc, errors.New("credentials must contain [[client]] entries")
	}
	seen := map[string]bool{}
	for _, item := range clients {
		fields, ok := item.(map[string]any)
		if !ok {
			return doc, errors.New("invalid client entry")
		}
		name, nok := fields["username"].(string)
		_, pok := fields["password"].(string)
		if !nok || !pok || name == "" || seen[name] {
			return doc, errors.New("invalid or duplicate credentials entry")
		}
		seen[name] = true
		doc.Users = append(doc.Users, managedUser{Fields: fields, Enabled: true})
	}
	delete(root, "client")
	if len(doc.Users) == 0 {
		return doc, errors.New("at least one enabled user is required")
	}
	return doc, nil
}

// TrustTunnel 1.1.0 reads credentials through TOML's textual representation.
// Basic double-quoted strings are required: literal single-quoted strings would
// become part of the actual password. Unknown fields remain semantically intact.
func tomlValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return basicString(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case uint64:
		return strconv.FormatUint(x, 10), nil
	case int:
		return strconv.Itoa(x), nil
	case float64:
		if math.IsInf(x, 1) {
			return "+inf", nil
		}
		if math.IsInf(x, -1) {
			return "-inf", nil
		}
		if math.IsNaN(x) {
			return "nan", nil
		}
		s := strconv.FormatFloat(x, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0"
		}
		return s, nil
	case time.Time:
		return x.Format(time.RFC3339Nano), nil
	case toml.LocalDate:
		return x.String(), nil
	case toml.LocalDateTime:
		return x.String(), nil
	case toml.LocalTime:
		return x.String(), nil
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			s, err := tomlValue(item)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		parts := []string{}
		for _, k := range sortedKeys(x) {
			s, err := tomlValue(x[k])
			if err != nil {
				return "", err
			}
			parts = append(parts, basicString(k)+" = "+s)
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	default:
		return "", errors.New("unsupported credentials value")
	}
}

func basicString(s string) string {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
func (d document) encode(activeOnly bool) ([]byte, error) {
	var b bytes.Buffer
	write := func(m map[string]any) error {
		for _, k := range sortedKeys(m) {
			v, err := tomlValue(m[k])
			if err != nil {
				return err
			}
			fmt.Fprintf(&b, "%s = %s\n", basicString(k), v)
		}
		return nil
	}
	if err := write(d.Root); err != nil {
		return nil, err
	}
	for _, u := range d.Users {
		if activeOnly && !u.Enabled {
			continue
		}
		b.WriteString("\n[[client]]\n")
		if err := write(u.Fields); err != nil {
			return nil, err
		}
	}
	return b.Bytes(), nil
}
func (d document) save() (snapshot, error) {
	b, err := d.encode(false)
	s := snapshot{TOML: string(b), Enabled: map[string]bool{}}
	for _, u := range d.Users {
		s.Enabled[u.Fields["username"].(string)] = u.Enabled
	}
	return s, err
}
func restore(s snapshot) (document, error) {
	d, err := parseDocument([]byte(s.TOML))
	if err != nil {
		return d, err
	}
	for i := range d.Users {
		name := d.Users[i].Fields["username"].(string)
		enabled, ok := s.Enabled[name]
		if !ok {
			return d, errors.New("incomplete saved user state")
		}
		d.Users[i].Enabled = enabled
	}
	if enabledCount(d) == 0 {
		return d, errors.New("saved state has no enabled users")
	}
	return d, nil
}
func clone(d document) document {
	s, err := d.save()
	if err != nil {
		panic(err)
	}
	out, err := restore(s)
	if err != nil {
		panic(err)
	}
	return out
}
func digest(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
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
	s := &store{path: credentials, statePath: filepath.Join(stateDir, "state.json"), applied: d, draft: clone(d), hash: digest(b)}
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
	b, err := json.Marshal(savedState{Version: 1, Hash: s.hash, Applied: a, Draft: d})
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
func enabledCount(d document) int {
	n := 0
	for _, u := range d.Users {
		if u.Enabled {
			n++
		}
	}
	return n
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
		s.draft.Users = append(s.draft.Users, managedUser{Fields: map[string]any{"username": c.Username, "password": c.Password}, Enabled: enabled})
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
