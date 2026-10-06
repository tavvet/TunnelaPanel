package endpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// managedUser retains unknown TOML fields alongside panel-only enablement state.
type managedUser struct {
	Fields  map[string]any
	Enabled bool
}

// document includes every user, including disabled users whose credentials are
// retained in private state so they can be enabled again without a password reset.
type document struct {
	Root  map[string]any
	Users []managedUser
}

// snapshot stores TOML rather than JSON field maps to preserve TOML value types.
// Enablement is separate because the native credentials format has no such flag.
type snapshot struct {
	TOML    string          `json:"toml"`
	Enabled map[string]bool `json:"enabled"`
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

// encode writes the enabled subset for TrustTunnel when activeOnly is true.
// Private snapshots pass false to retain disabled users and their passwords.
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

// clone operates on an already validated document. A failed round trip indicates
// an internal invariant violation rather than an invalid incoming user request.
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

func enabledCount(d document) int {
	n := 0
	for _, u := range d.Users {
		if u.Enabled {
			n++
		}
	}
	return n
}
