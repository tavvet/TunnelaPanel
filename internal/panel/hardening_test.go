package panel

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoginRateLimitEvictsOldestWhenFull(t *testing.T) {
	a := newAuthState()
	now := time.Now()
	for i := 0; i < loginAttempts; i++ {
		if !a.allowAttempt("admin-ip", now) {
			t.Fatal("early rate limit")
		}
	}

	for i := 0; i < maxLoginAttempts; i++ {
		if !a.allowAttempt(fmt.Sprintf("spray-%d", i), now.Add(time.Second)) {
			t.Fatal("a full table refused a new client")
		}
	}

	if len(a.attempts) > maxLoginAttempts {
		t.Fatal("attempt table is unbounded")
	}

	if !a.allowAttempt("late-client", now.Add(2*time.Second)) {
		t.Fatal("a full table locked out a new client")
	}

	if a.allowAttempt("admin-ip", now.Add(2*time.Second)) {
		t.Fatal("eviction reset an exhausted client's limit")
	}

	for i := 1; i < loginAttempts; i++ {
		a.allowAttempt("late-client", now.Add(2*time.Second))
	}

	if a.allowAttempt("late-client", now.Add(2*time.Second)) {
		t.Fatal("per-client limit not enforced in a full table")
	}
}

func TestFullTableOfExhaustedKeysRefusesNewKeys(t *testing.T) {
	a := newAuthState()
	now := time.Now()
	for i := 0; i < maxLoginAttempts; i++ {
		a.attempts[fmt.Sprintf("spent-%d", i)] = attempt{Count: loginAttempts, Until: now.Add(time.Minute)}
	}

	if a.allowAttempt("rotated-key", now) {
		t.Fatal("a new key reset an exhausted key's window")
	}

	if !a.allowAttempt("rotated-key", now.Add(2*time.Minute)) {
		t.Fatal("expired windows were not cleared")
	}
}

func TestLoginHashingIsBounded(t *testing.T) {
	a := newAuthState()
	var releases []func()
	for i := 0; i < loginHashSlots; i++ {
		release, ok := a.acquireLoginSlot(context.Background(), "client", time.Second)
		if !ok {
			t.Fatal("free slot not granted")
		}
		releases = append(releases, release)
	}

	if _, ok := a.acquireLoginSlot(context.Background(), "client", 20*time.Millisecond); ok {
		t.Fatal("more concurrent hashes than slots")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := a.acquireLoginSlot(ctx, "client", time.Minute); ok {
		t.Fatal("canceled request waited for a slot")
	}

	releases[0]()
	if _, ok := a.acquireLoginSlot(context.Background(), "client", time.Second); !ok {
		t.Fatal("released slot not reusable")
	}
}

func TestTrustedKeyUsesReservedSlotDuringFlood(t *testing.T) {
	a := newAuthState()
	now := time.Now()
	a.loginSucceeded("admin-net", now)
	for i := 0; i < loginHashSlots; i++ {
		a.hashSlots <- struct{}{}
	}

	if _, ok := a.acquireLoginSlot(context.Background(), "fresh-net", 20*time.Millisecond); ok {
		t.Fatal("an unknown key took the reserved slot")
	}

	release, ok := a.acquireLoginSlot(context.Background(), "admin-net", 20*time.Millisecond)
	if !ok {
		t.Fatal("a trusted key was starved by the flood")
	}

	release()
	if len(a.reservedSlot) != 0 || len(a.hashSlots) != loginHashSlots {
		t.Fatal("release returned the wrong slot")
	}

	if a.isTrusted("admin-net", now.Add(trustedFor)) {
		t.Fatal("trust never expires")
	}

	for i := 0; i < maxTrustedKeys+5; i++ {
		a.loginSucceeded(fmt.Sprintf("net-%d", i), now.Add(time.Duration(i+1)*time.Second))
	}

	if len(a.trusted) > maxTrustedKeys {
		t.Fatal("trusted keys are unbounded")
	}
}

func TestRefusalAuditsAreCoalesced(t *testing.T) {
	a := newAuthState()
	now := time.Now()
	for i := 0; i <= loginAttempts; i++ {
		a.allowAttempt("flooder", now)
	}

	if !a.reportRefusal(refusalRateLimited, "flooder", now) || a.reportRefusal(refusalRateLimited, "flooder", now) {
		t.Fatal("rate-limited key not reported exactly once per window")
	}

	if !a.reportRefusal(refusalBusy, "flooder", now) ||
		a.reportRefusal(refusalBusy, "other", now.Add(time.Second)) {
		t.Fatal("busy refusals not coalesced")
	}

	if !a.reportRefusal(refusalBusy, "other", now.Add(refusalReportEvery)) {
		t.Fatal("busy refusals never reported again")
	}
}

func TestBusyLoginIsRefusedWithoutUsingAnAttempt(t *testing.T) {
	s, _ := newTestPanel(t)
	login, _ := loginCookie(t, s)
	for i := 0; i < loginHashSlots; i++ {
		s.auth.hashSlots <- struct{}{}
	}

	for i := 0; i < 2; i++ {
		r := httptest.NewRequest(http.MethodPost, "http://localhost:8080/login", strings.NewReader(url.Values{
			"username": {"admin"},
			"password": {"invalid-password"},
			"csrf":     {login.Value},
		}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(login)
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Millisecond)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r.WithContext(ctx))
		cancel()
		if w.Code != http.StatusServiceUnavailable {
			t.Fatal("busy login not refused", w.Code)
		}
	}

	if len(s.auth.attempts) != 0 {
		t.Fatal("a refused busy login used an attempt")
	}

	data, err := os.ReadFile(filepath.Join(s.config.StateDir, "audit.jsonl"))
	if err != nil || strings.Count(string(data), "login_busy") != 1 {
		t.Fatalf("busy refusals not audited once: %q %v", data, err)
	}
}

func TestLegacyLoginCookieDoesNotShadowCurrentToken(t *testing.T) {
	s, _ := newTestPanel(t)
	w := request(s, "GET", "/login", nil)
	var current *http.Cookie
	legacyExpired := false
	for _, c := range w.Result().Cookies() {
		if c.Name == "tp_login" && c.Path == "/login" && c.MaxAge < 0 {
			legacyExpired = true
		}
		if c.Name == "tp_login" && c.Path == "/" {
			current = c
		}
	}

	if current == nil || !legacyExpired {
		t.Fatal("login page did not replace the legacy cookie")
	}

	p, err := os.ReadFile(filepath.Join(s.config.StateDir, "bootstrap-password"))
	if err != nil {
		t.Fatal(err)
	}

	// Browsers send the more specific Path=/login cookie first.
	legacy := &http.Cookie{Name: "tp_login", Value: strings.Repeat("o", 43)}
	w = request(s, "POST", "/login", url.Values{
		"username": {"admin"},
		"password": {string(p)},
		"csrf":     {current.Value},
	}, legacy, current)
	if w.Code != 303 {
		t.Fatal("stale legacy cookie blocked sign-in", w.Code)
	}
}

func loginCookie(t *testing.T, s *Server) (*http.Cookie, *httptest.ResponseRecorder) {
	t.Helper()
	w := request(s, "GET", "/login", nil)
	for _, c := range w.Result().Cookies() {
		if c.Name == s.cookieName("tp_login") && c.MaxAge > 0 {
			return c, w
		}
	}

	t.Fatal("missing login cookie")
	return nil, nil
}

func TestRateLimitKeyGroupsIPv6Prefixes(t *testing.T) {
	if rateLimitKey("2001:db8:1:2::1") != rateLimitKey("2001:db8:1:2:ffff::9") {
		t.Fatal("addresses in one /64 have separate limits")
	}

	if rateLimitKey("2001:db8:1:2::1") == rateLimitKey("2001:db8:1:3::1") {
		t.Fatal("different /64 prefixes share a limit")
	}

	if rateLimitKey("203.0.113.7") != "203.0.113.7" {
		t.Fatal("IPv4 key changed")
	}
}

func TestSecureModeUsesHostCookiesAndHSTS(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Config{
		StateDir:     filepath.Join(dir, "panel"),
		Socket:       filepath.Join(dir, "missing.sock"),
		SecureCookie: true,
		Version:      "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	w := request(s, "GET", "/login", nil)
	if w.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("HSTS missing in secure mode")
	}

	var login *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "__Host-tp_login" {
			login = c
		}
	}

	if login == nil || !login.Secure || login.Path != "/" || login.Domain != "" {
		t.Fatal("login cookie does not meet __Host- requirements")
	}

	p, err := os.ReadFile(filepath.Join(s.config.StateDir, "bootstrap-password"))
	if err != nil {
		t.Fatal(err)
	}

	w = request(s, "POST", "/login", url.Values{
		"username": {"admin"},
		"password": {string(p)},
		"csrf":     {login.Value},
	}, login)
	if w.Code != 303 {
		t.Fatal(w.Code, w.Body.String())
	}

	var session *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "__Host-tp_session" {
			session = c
		}
	}

	if session == nil || !session.Secure || session.Path != "/" {
		t.Fatal("session cookie does not meet __Host- requirements")
	}

	if w := request(s, "GET", "/settings", nil, session); w.Code != 200 {
		t.Fatal("prefixed session cookie not accepted", w.Code)
	}

	plain := &http.Cookie{Name: "tp_session", Value: session.Value}
	if w := request(s, "GET", "/settings", nil, plain); w.Code != 303 {
		t.Fatal("unprefixed cookie accepted in secure mode")
	}
}

func TestPlainModeSendsNoHSTS(t *testing.T) {
	s, _ := newTestPanel(t)
	if request(s, "GET", "/login", nil).Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS sent without HTTPS")
	}
}

func TestStateDirPermissionErrorNamesTheFix(t *testing.T) {
	err := stateDirError("/data/panel", &fs.PathError{Op: "open", Path: "/data/panel/auth.json", Err: fs.ErrPermission})
	if !strings.Contains(err.Error(), "must be owned by the panel user") {
		t.Fatal(err)
	}
}

func TestExhaustedKeyIsRefusedWithoutWaitingForASlot(t *testing.T) {
	s, _ := newTestPanel(t)
	login, _ := loginCookie(t, s)
	for i := 0; i < loginHashSlots; i++ {
		s.auth.hashSlots <- struct{}{}
	}

	// httptest requests come from 192.0.2.1.
	s.auth.attempts["192.0.2.1"] = attempt{Count: loginAttempts, Until: time.Now().Add(time.Minute)}
	started := time.Now()
	w := request(s, "POST", "/login", url.Values{
		"username": {"admin"},
		"password": {"invalid-password"},
		"csrf":     {login.Value},
	}, login)
	if w.Code != http.StatusTooManyRequests {
		t.Fatal("exhausted key not rate limited", w.Code)
	}

	if time.Since(started) >= loginHashWait {
		t.Fatal("exhausted key waited for a hashing slot")
	}
}

func TestFailedLoginAuditIsRateLimited(t *testing.T) {
	s, _ := newTestPanel(t)
	for i := 0; i < failedAuditsPerMinute+5; i++ {
		s.auditFailedLogin("198.51.100.7")
	}

	path := filepath.Join(s.config.StateDir, "audit.jsonl")
	data, _ := os.ReadFile(path)
	if n := strings.Count(string(data), `"login_failed"`); n != failedAuditsPerMinute {
		t.Fatalf("wrote %d login_failed records in one minute", n)
	}

	s.auditMu.Lock()
	s.failedWindow = s.failedWindow.Add(-time.Minute)
	s.auditMu.Unlock()
	s.auditFailedLogin("198.51.100.7")
	data, _ = os.ReadFile(path)
	// Map keys are encoded in sorted order.
	if !strings.Contains(string(data), `"action":"login_failed_suppressed","count":5`) {
		t.Fatalf("suppressed failures not reported: %s", data[len(data)-200:])
	}

	if n := strings.Count(string(data), `"login_failed"`); n != failedAuditsPerMinute+1 {
		t.Fatal("failure after the minute was not recorded")
	}
}

func TestAuditRotationKeepsSeveralCopies(t *testing.T) {
	s, _ := newTestPanel(t)
	path := filepath.Join(s.config.StateDir, "audit.jsonl")
	for i := 1; i <= auditCopies; i++ {
		if err := os.WriteFile(fmt.Sprintf("%s.%d", path, i), []byte(fmt.Sprint(i)), 0600); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(path, []byte(strings.Repeat("x", auditMaxSize+1)), 0600); err != nil {
		t.Fatal(err)
	}

	s.audit("login", "192.0.2.1", true)
	if st, err := os.Stat(path + ".1"); err != nil || st.Size() != auditMaxSize+1 {
		t.Fatal("full log was not moved to .1")
	}

	for i := 2; i <= auditCopies; i++ {
		data, err := os.ReadFile(fmt.Sprintf("%s.%d", path, i))
		if err != nil || string(data) != fmt.Sprint(i-1) {
			t.Fatalf("copy %d not shifted: %q %v", i, data, err)
		}
	}

	if _, err := os.Stat(fmt.Sprintf("%s.%d", path, auditCopies+1)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("more copies than auditCopies kept")
	}
}
