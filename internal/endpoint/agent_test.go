package endpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

func TestHelperEndpoint(t *testing.T) {
	if os.Getenv("TUNNELAPANEL_TEST_ENDPOINT") != "1" {
		return
	}
	args := os.Args
	start := 0
	for i, s := range args {
		if s == "--" {
			start = i + 1
			break
		}
	}
	args = args[start:]
	if len(args) == 1 && args[0] == "--version" {
		fmt.Println("fake-endpoint-1.1.0")
		os.Exit(0)
	}
	if len(args) < 2 {
		os.Exit(3)
	}
	dir := filepath.Dir(args[0])
	b, err := os.ReadFile(filepath.Join(dir, "credentials.toml"))
	if err != nil {
		os.Exit(3)
	}
	if strings.Contains(string(b), "fail-startup-123") {
		os.Exit(2)
	}
	if len(args) > 2 {
		if args[len(args)-1] == "deeplink" {
			fmt.Println("tt://?fake=client\nDo not include this extra text")
		} else {
			fmt.Println("username = \"existing\"")
		}
		os.Exit(0)
	}
	var cfg struct {
		Listen string `toml:"listen_address"`
	}
	b, err = os.ReadFile(args[0])
	if err != nil || toml.Unmarshal(b, &cfg) != nil {
		os.Exit(3)
	}
	l, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		os.Exit(4)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	<-ch
	os.Exit(0)
}
func newTestAgent(t *testing.T) *Agent {
	t.Helper()
	dir := t.TempDir()
	config := filepath.Join(dir, "config")
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	vpn := fmt.Sprintf("listen_address = %q\ncredentials_file = \"credentials.toml\"\n", addr)
	for name, b := range map[string]string{"vpn.toml": vpn, "hosts.toml": "", "credentials.toml": fixtureCredentials} {
		if err := os.WriteFile(filepath.Join(config, name), []byte(b), 0600); err != nil {
			t.Fatal(err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "endpoint")
	script := "#!/bin/sh\nTUNNELAPANEL_TEST_ENDPOINT=1 exec '" + strings.ReplaceAll(exe, "'", "'\\''") + "' -test.run=^TestHelperEndpoint$ -- \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{Binary: wrapper, ConfigDir: config, StateDir: filepath.Join(dir, "state"), Socket: filepath.Join(dir, "control.sock"), PublicAddress: "vpn.example.com:443", ReadyTimeout: time.Second, StableTime: 100 * time.Millisecond, StopTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if err := a.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return a
}
func TestFailedApplyRollsBackAndKeepsDraft(t *testing.T) {
	a := newTestAgent(t)
	old, err := os.ReadFile(a.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.store.change("PATCH", protocol.UserChange{Username: "existing", Password: "fail-startup-123"}); err != nil {
		t.Fatal(err)
	}
	err = a.apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "previous settings restored") {
		t.Fatalf("expected rollback, got %v", err)
	}
	current, err := os.ReadFile(a.store.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(old) {
		t.Fatal("credentials were not restored exactly")
	}
	if !a.process.status().Running || !a.store.pending() {
		t.Fatal("rollback lost running endpoint or draft")
	}
	if _, err := os.Stat(filepath.Join(a.config.StateDir, "apply.json")); !os.IsNotExist(err) {
		t.Fatal("completed rollback left transaction journal")
	}
}
func TestSuccessfulApplyAndAppliedOnlyExports(t *testing.T) {
	a := newTestAgent(t)
	if err := a.store.change("POST", protocol.UserChange{Username: "new", Password: "safe-password-123"}); err != nil {
		t.Fatal(err)
	}
	request := func(user string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		a.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/v1/export?username="+user, nil))
		return r
	}
	if r := request("new"); r.Code != 404 {
		t.Fatal("exported pending user")
	}
	if err := a.apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a.store.pending() {
		t.Fatal("successful apply left draft")
	}
	r := request("new")
	if r.Code != 200 {
		t.Fatalf("export failed: %s", r.Body.String())
	}
	var exported protocol.Export
	if json.Unmarshal(r.Body.Bytes(), &exported) != nil || exported.DeepLink != "tt://?fake=client" {
		t.Fatal("deeplink includes non-URI output")
	}
	backups, err := os.ReadDir(filepath.Join(a.config.StateDir, "backups"))
	if err != nil || len(backups) != 1 {
		t.Fatal("credentials backup missing")
	}
}
func TestInterruptedApplyRecovery(t *testing.T) {
	s := testStore(t)
	before, _ := os.ReadFile(s.path)
	state, _ := os.ReadFile(s.statePath)
	journal, _ := json.Marshal(transaction{Credentials: before, State: state})
	dir := filepath.Dir(s.statePath)
	if err := atomicWrite(filepath.Join(dir, "apply.json"), journal); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(s.path, []byte("broken new config")); err != nil {
		t.Fatal(err)
	}
	if err := recoverTransaction(dir, s.path); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(s.path)
	if string(after) != string(before) {
		t.Fatal("interrupted apply was not recovered")
	}
	if _, err := newStore(s.path, dir); err != nil {
		t.Fatal(err)
	}
}

func TestUnexpectedExitCanRecoverWithoutApplyingDraft(t *testing.T) {
	a := newTestAgent(t)
	if err := a.store.change("PATCH", protocol.UserChange{Username: "existing", Password: "fail-startup-123"}); err != nil {
		t.Fatal(err)
	}
	a.process.mu.Lock()
	cmd, done := a.process.cmd, a.process.done
	a.process.mu.Unlock()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-done
	if err := a.recoverProcess(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !a.process.status().Running || !a.store.pending() {
		t.Fatal("recovery changed draft or failed to restart")
	}
}

func TestInvalidTLSReloadKeepsEndpointRunning(t *testing.T) {
	a := newTestAgent(t)
	if err := a.ReloadTLS(); err == nil {
		t.Fatal("accepted invalid TLS configuration")
	}
	if !a.process.status().Running {
		t.Fatal("invalid TLS reload stopped endpoint")
	}
}
