package panel

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

func newTestPanel(t *testing.T) (*Server, *atomic.Int32) {
	t.Helper()
	// macOS Unix socket paths are limited to 104 bytes.
	dir, err := os.MkdirTemp("/tmp", "tp-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "agent.sock")
	l, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	agent := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		var value any
		switch r.URL.Path {
		case "/v1/status":
			value = protocol.Status{Running: true, Version: "1.1.0", StartedAt: time.Now().Add(-time.Hour), PublicAddress: "vpn.example.com:443", Pending: true}
		case "/v1/users":
			value = protocol.Users{Users: []protocol.User{{Username: "alice", Enabled: true}}, Pending: true}
		case "/v1/clients":
			value = []protocol.ClientMetric{{Username: "alice", Sessions: 1, Inbound: 1024, Outbound: 2048}}
		case "/v1/logs":
			value = protocol.LogResponse{Lines: []string{"<script>alert(1)</script>"}}
		case "/v1/certificates":
			value = []protocol.Certificate{{Hostname: "vpn.example.com", NotAfter: time.Now().Add(30 * 24 * time.Hour)}}
		case "/v1/export":
			value = protocol.Export{Username: "alice", DeepLink: "tt://?example-safe-test", TOML: "username = \"alice\"\npassword = \"example-secret\""}
		default:
			value = protocol.Result{Message: "ok"}
		}
		_ = json.NewEncoder(w).Encode(value)
	}))
	agent.Listener = l
	agent.Start()
	t.Cleanup(agent.Close)
	s, err := New(Config{StateDir: filepath.Join(dir, "panel"), Socket: socket, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return s, calls
}

func request(s *Server, method, path string, values url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}
	r := httptest.NewRequest(method, "http://localhost:8080"+path, body)
	if values != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestAuthenticationCSRFAndSecretRoutes(t *testing.T) {
	s, calls := newTestPanel(t)
	for _, path := range []string{"/", "/users", "/users/export?username=alice", "/users/qr?username=alice", "/logs", "/settings"} {
		w := request(s, "GET", path, nil)
		if w.Code != 303 {
			t.Fatalf("unauthenticated %s: %d", path, w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unauthenticated requests reached agent")
	}
	w := request(s, "GET", "/login", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var loginCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "tp_login" {
			loginCookie = c
		}
	}
	if loginCookie == nil {
		t.Fatal("missing login csrf cookie")
	}
	p, err := os.ReadFile(filepath.Join(s.config.StateDir, "bootstrap-password"))
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{"username": {"admin"}, "password": {string(p)}, "csrf": {loginCookie.Value}}
	w = request(s, "POST", "/login", values, loginCookie)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}
	var authCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "tp_session" {
			authCookie = c
		}
	}
	if authCookie == nil || !authCookie.HttpOnly || authCookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("session cookie missing protections")
	}
	se, ok := s.auth.lookup(authCookie.Value, time.Now())
	if !ok {
		t.Fatal("session not saved")
	}
	before := calls.Load()
	w = request(s, "POST", "/apply", url.Values{"csrf": {"bad"}}, authCookie)
	if w.Code != 403 || calls.Load() != before {
		t.Fatal("CSRF bypass")
	}
	w = request(s, "POST", "/apply", url.Values{"csrf": {se.CSRF}}, authCookie)
	if w.Code != 303 || calls.Load() != before+1 {
		t.Fatal("valid apply failed")
	}
	for _, path := range []string{"/", "/users", "/users/export?username=alice", "/logs", "/settings"} {
		w = request(s, "GET", path, nil, authCookie)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "</html>") {
			t.Fatalf("page %s: %d %s", path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "<script>alert(1)</script>") {
			t.Fatal("unescaped agent content")
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("secret page can be cached")
		}
	}
	w = request(s, "GET", "/users/qr?username=alice", nil, authCookie)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Fatal("QR unavailable")
	}
	w = request(s, "POST", "/logout", url.Values{"csrf": {se.CSRF}}, authCookie)
	if w.Code != 303 {
		t.Fatal("logout failed")
	}
	if _, ok = s.auth.lookup(authCookie.Value, time.Now()); ok {
		t.Fatal("logout did not invalidate session")
	}
}

func TestHostAndOriginProtection(t *testing.T) {
	s, calls := newTestPanel(t)
	for _, tc := range []struct{ url, origin string }{{"http://evil.example/login", ""}, {"http://localhost:8080/login", "https://evil.example"}} {
		r := httptest.NewRequest("POST", tc.url, strings.NewReader("username=admin"))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("origin/host accepted: %d", w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("request reached agent")
	}
}

func TestBrowserFormOriginPolicy(t *testing.T) {
	s, calls := newTestPanel(t)
	page := request(s, "GET", "/login", nil)
	if page.Header().Get("Referrer-Policy") != "same-origin" ||
		!strings.Contains(page.Body.String(), `<meta name="referrer" content="same-origin">`) ||
		strings.Contains(page.Body.String(), `content="no-referrer"`) {
		t.Fatal("form navigation must preserve Origin in both HTTP and HTML policy")
	}
	var loginCookie *http.Cookie
	for _, cookie := range page.Result().Cookies() {
		if cookie.Name == "tp_login" {
			loginCookie = cookie
		}
	}
	if loginCookie == nil {
		t.Fatal("missing login cookie")
	}
	password, err := os.ReadFile(filepath.Join(s.config.StateDir, "bootstrap-password"))
	if err != nil {
		t.Fatal(err)
	}
	post := func(path, origin string, fields url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://localhost:8080"+path, strings.NewReader(fields.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", origin)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	login := post("/login", "http://localhost:8080", url.Values{
		"username": {"admin"}, "password": {string(password)}, "csrf": {loginCookie.Value},
	}, loginCookie)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("browser login failed: %d", login.Code)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == "tp_session" {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil {
		t.Fatal("missing session cookie")
	}
	se, ok := s.auth.lookup(sessionCookie.Value, time.Now())
	if !ok {
		t.Fatal("missing session")
	}
	for _, tc := range []struct{ origin, token string }{
		{"null", se.CSRF}, {"https://evil.example", se.CSRF},
		{"http://localhost:8081", se.CSRF}, {"http://localhost:8080", "invalid"},
	} {
		before := calls.Load()
		w := post("/apply", tc.origin, url.Values{"csrf": {tc.token}}, sessionCookie)
		if w.Code != http.StatusForbidden || calls.Load() != before {
			t.Fatalf("unsafe form reached endpoint: origin=%q status=%d", tc.origin, w.Code)
		}
	}
	w := post("/apply", "http://localhost:8080", url.Values{"csrf": {se.CSRF}}, sessionCookie)
	if w.Code != http.StatusSeeOther || calls.Load() != 1 {
		t.Fatal("same-origin form was rejected")
	}
}

func TestPasswordPersistenceInvalidationAndRateLimit(t *testing.T) {
	dir := t.TempDir()
	a, err := newAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := os.ReadFile(filepath.Join(dir, "bootstrap-password"))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := a.createSession(time.Now())
	if err = a.changePassword(string(p), "New-secure-password-1234"); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.lookup(id, time.Now()); ok {
		t.Fatal("old session survived password change")
	}
	if _, err := os.Stat(filepath.Join(dir, "bootstrap-password")); !os.IsNotExist(err) {
		t.Fatal("bootstrap credential not removed")
	}
	b, err := newAuth(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !b.check("admin", "New-secure-password-1234") || b.check("admin", string(p)) {
		t.Fatal("password not persisted")
	}
	now := time.Now()
	for i := 0; i < 10; i++ {
		if !b.allowAttempt("127.0.0.1", now) {
			t.Fatal("early rate limit")
		}
	}
	if b.allowAttempt("127.0.0.1", now) {
		t.Fatal("unbounded login attempts")
	}
	if !b.allowAttempt("127.0.0.1", now.Add(16*time.Minute)) {
		t.Fatal("rate limit never expires")
	}
	for _, file := range []string{"auth.json"} {
		st, err := os.Stat(filepath.Join(dir, file))
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatal("insecure auth permissions")
		}
	}
}

func TestConcurrentPasswordRotationDoesNotIssueOldCredentialSession(t *testing.T) {
	a, err := newAuth(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := os.ReadFile(filepath.Join(a.dir, "bootstrap-password"))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var loginID string
	var changeErr error
	go func() { defer wg.Done(); <-start; loginID, _ = a.authenticate("admin", string(p), time.Now()) }()
	go func() {
		defer wg.Done()
		<-start
		changeErr = a.changePassword(string(p), "New-concurrent-password-1234")
	}()
	close(start)
	wg.Wait()
	if changeErr != nil {
		t.Fatal(changeErr)
	}
	if loginID != "" {
		if _, ok := a.lookup(loginID, time.Now()); ok {
			t.Fatal("old-password login survived concurrent rotation")
		}
	}
	if id, ok := a.authenticate("admin", string(p), time.Now()); ok || id != "" {
		t.Fatal("old password accepted after rotation")
	}
}
