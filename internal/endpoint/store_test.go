package endpoint

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

const fixtureCredentials = `label = "retained"
threshold = 1.0
extra = { mode = "a", nested = { value = 5 } }

[[client]]
username = "existing"
password = "oldpass"
max_http2_conns = 7
max_http3_conns = 3
extra = { flag = true }
`

func testStore(t *testing.T) *store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.toml")
	if err := os.WriteFile(path, []byte(fixtureCredentials), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := newStore(path, filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestPreservesUnknownValuesAndBasicQuotedPasswords(t *testing.T) {
	d, err := parseDocument([]byte(fixtureCredentials))
	if err != nil {
		t.Fatal(err)
	}
	d.Users[0].Fields["password"] = "safe<&>password"
	b, err := d.encode(true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"password" = "safe<&>password"`) {
		t.Fatal("password must use unescaped ASCII in basic double quotes")
	}
	d2, err := parseDocument(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d, d2) {
		t.Fatal("credentials rewrite changed unknown values")
	}
	var values map[string]any
	if err := toml.Unmarshal(b, &values); err != nil {
		t.Fatal(err)
	}
	if _, ok := values["threshold"].(float64); !ok {
		t.Fatal("float became an integer")
	}
}
func TestDraftLifecycleAndLastEnabledProtection(t *testing.T) {
	s := testStore(t)
	off, on := false, true
	if err := s.change("PATCH", protocol.UserChange{Username: "existing", Enabled: &off}); err == nil {
		t.Fatal("disabled last enabled user")
	}
	if err := s.change("DELETE", protocol.UserChange{Username: "existing"}); err == nil {
		t.Fatal("deleted last enabled user")
	}
	if err := s.change("POST", protocol.UserChange{Username: "new", Password: "safe-password-123"}); err != nil {
		t.Fatal(err)
	}
	if err := s.change("PATCH", protocol.UserChange{Username: "existing", Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if !s.pending() {
		t.Fatal("draft not pending")
	}
	b, _ := s.draft.encode(true)
	if strings.Contains(string(b), "oldpass") {
		t.Fatal("disabled user in active credentials")
	}
	reloaded, err := newStore(s.path, filepath.Dir(s.statePath))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.draft, reloaded.draft) {
		t.Fatal("draft lost on reload")
	}
	if err := reloaded.change("PATCH", protocol.UserChange{Username: "existing", Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	if reloaded.draft.Users[0].Fields["password"] != "oldpass" {
		t.Fatal("disabled user's imported password lost")
	}
	listing := reloaded.list()
	if listing.Users[0].MaxHTTP2Conns == nil || *listing.Users[0].MaxHTTP2Conns != 7 {
		t.Fatal("connection limit lost")
	}
	info, err := os.Stat(s.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal("state permissions are not private")
	}
}
func TestRejectsCredentialsDrift(t *testing.T) {
	s := testStore(t)
	if err := os.WriteFile(s.path, []byte(fixtureCredentials+"\n# operator change\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if s.drift() == nil {
		t.Fatal("external changes ignored")
	}
	if _, err := newStore(s.path, filepath.Dir(s.statePath)); err == nil {
		t.Fatal("external changes ignored on restart")
	}
}
func TestPasswordValidation(t *testing.T) {
	for _, p := range []string{"short", `bad"password123`, `bad\password123`, " badpassword123", "badpassword123 ", "badpasswordé123", "badpassword\n123"} {
		if validPassword(p) {
			t.Errorf("accepted unsafe password %q", p)
		}
	}
	if !validPassword("A-safe_<>&-password123") {
		t.Fatal("safe ASCII password rejected")
	}
}
func TestLogRedactionAndBounds(t *testing.T) {
	r := &logRing{}
	d, _ := parseDocument([]byte(fixtureCredentials))
	r.addSecrets(d)
	_, _ = r.Write([]byte("output old"))
	_, _ = r.Write([]byte("pass password=unknownsecret\n"))
	for i := 0; i < 300; i++ {
		_, _ = r.Write([]byte("oldpass\n"))
	}
	out := r.read()
	if len(out.Lines) != 200 {
		t.Fatal("unbounded log ring")
	}
	for _, line := range out.Lines {
		if strings.Contains(line, "oldpass") {
			t.Fatal("secret leaked")
		}
	}
}
