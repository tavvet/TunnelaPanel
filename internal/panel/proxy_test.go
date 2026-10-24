package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProxyClientIP(t *testing.T) {
	for _, tc := range []struct {
		name, remote, forwardedFor, want string
		trusted                          []string
		realIP                           []string
	}{
		{
			name:   "disabled by default",
			remote: "172.30.0.2:45000",
			realIP: []string{"203.0.113.4"},
			want:   "172.30.0.2",
		},
		{
			name:    "exact trusted proxy",
			trusted: []string{"172.30.0.2"},
			remote:  "172.30.0.2:45000",
			realIP:  []string{"203.0.113.4"},
			want:    "203.0.113.4",
		},
		{
			name:    "untrusted peer cannot spoof",
			trusted: []string{"172.30.0.2"},
			remote:  "172.30.0.3:45000",
			realIP:  []string{"203.0.113.4"},
			want:    "172.30.0.3",
		},
		{
			name:    "trusted IPv4 CIDR",
			trusted: []string{"172.30.0.0/24"},
			remote:  "172.30.0.7:45000",
			realIP:  []string{"203.0.113.4"},
			want:    "203.0.113.4",
		},
		{
			name:    "outside CIDR",
			trusted: []string{"172.30.0.0/24"},
			remote:  "172.30.1.2:45000",
			realIP:  []string{"203.0.113.4"},
			want:    "172.30.1.2",
		},
		{
			name:    "trusted IPv6",
			trusted: []string{"2001:db8:1::/64"},
			remote:  "[2001:db8:1::2]:45000",
			realIP:  []string{"2001:db8:2::4"},
			want:    "2001:db8:2::4",
		},
		{
			name:    "mapped peer normalized",
			trusted: []string{"172.30.0.2"},
			remote:  "[::ffff:172.30.0.2]:45000",
			realIP:  []string{"::ffff:203.0.113.4"},
			want:    "203.0.113.4",
		},
		{
			name:    "mapped trusted CIDR",
			trusted: []string{"::ffff:172.30.0.0/120"},
			remote:  "172.30.0.2:45000",
			realIP:  []string{"203.0.113.4"},
			want:    "203.0.113.4",
		},
		{
			name:         "XFF never trusted",
			trusted:      []string{"172.30.0.2"},
			remote:       "172.30.0.2:45000",
			forwardedFor: "203.0.113.4",
			want:         "172.30.0.2",
		},
		{
			name:    "missing header",
			trusted: []string{"172.30.0.2"},
			remote:  "172.30.0.2:45000",
			want:    "172.30.0.2",
		},
		{
			name:    "empty header",
			trusted: []string{"172.30.0.2"},
			remote:  "172.30.0.2:45000",
			realIP:  []string{""},
			want:    "172.30.0.2",
		},
		{
			name:    "malformed header",
			trusted: []string{"172.30.0.2"},
			remote:  "172.30.0.2:45000",
			realIP:  []string{"not-an-ip"},
			want:    "172.30.0.2",
		},
		{
			name:    "header with port",
			trusted: []string{"172.30.0.2"},
			remote:  "172.30.0.2:45000",
			realIP:  []string{"203.0.113.4:80"},
			want:    "172.30.0.2",
		},
		{
			name:    "header chain",
			trusted: []string{"172.30.0.2"},
			remote:  "172.30.0.2:45000",
			realIP:  []string{"203.0.113.4, 198.51.100.9"},
			want:    "172.30.0.2",
		},
		{
			name:    "repeated header",
			trusted: []string{"172.30.0.2"},
			remote:  "172.30.0.2:45000",
			realIP: []string{
				"203.0.113.4",
				"198.51.100.9",
			},
			want: "172.30.0.2",
		},
		{
			name:    "scoped header",
			trusted: []string{"172.30.0.2"},
			remote:  "172.30.0.2:45000",
			realIP:  []string{"fe80::4%eth0"},
			want:    "172.30.0.2",
		},
		{
			name:    "invalid peer never trusted",
			trusted: []string{"172.30.0.2"},
			remote:  "invalid-peer",
			realIP:  []string{"203.0.113.4"},
			want:    "invalid-peer",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := parseTrustedProxies(tc.trusted)
			if err != nil {
				t.Fatal(err)
			}

			s := &Server{trustedProxies: policy}
			r := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
			r.RemoteAddr = tc.remote
			for _, value := range tc.realIP {
				r.Header.Add("X-Real-IP", value)
			}

			r.Header.Set("X-Forwarded-For", tc.forwardedFor)
			if got := s.remoteIP(r); got != tc.want {
				t.Fatalf("client IP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInvalidTrustedProxiesFailBeforeBootstrap(t *testing.T) {
	for _, value := range []string{
		"",
		"proxy",
		"http://172.30.0.2",
		"172.30.0.2:8080",
		"172.30.0.0/33",
		"2001:db8::/129",
		"fe80::1%eth0",
		"::ffff:172.30.0.0/64",
	} {
		t.Run(value, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			_, err := New(Config{
				StateDir: dir,
				TrustedProxies: []string{
					"172.30.0.2",
					value,
				},
			})
			if err == nil || !strings.Contains(err.Error(), "PANEL_TRUSTED_PROXIES") {
				t.Fatalf("invalid proxy %q accepted: %v", value, err)
			}

			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("invalid configuration created state: %v", err)
			}
		})
	}
}

func TestProxyClientsHaveIndependentLoginLimitsAndAudit(t *testing.T) {
	dir := t.TempDir()
	s, err := New(Config{
		StateDir:       dir,
		TrustedProxies: []string{"172.30.0.2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	initial := request(s, http.MethodGet, "/login", nil)
	var loginCookie *http.Cookie
	for _, cookie := range initial.Result().Cookies() {
		if cookie.Name == "tp_login" {
			loginCookie = cookie
		}
	}

	if loginCookie == nil {
		t.Fatal("missing login token")
	}

	login := func(clientIP, password string) *httptest.ResponseRecorder {
		values := url.Values{
			"username": {"admin"},
			"password": {password},
			"csrf":     {loginCookie.Value},
		}

		r := httptest.NewRequest(http.MethodPost, "http://localhost:8080/login", strings.NewReader(values.Encode()))
		r.RemoteAddr = "172.30.0.2:45000"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "http://localhost:8080")
		r.Header.Set("X-Real-IP", clientIP)
		r.Header.Set("X-Forwarded-For", "192.0.2.99")
		r.AddCookie(loginCookie)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}

	// An oversized password can never match, but it still uses an attempt.
	if w := login("203.0.113.4", strings.Repeat("x", 129)); w.Code != http.StatusUnauthorized {
		t.Fatalf("oversized password: %d", w.Code)
	}

	// One real failure exercises the hashing path; the remaining tries are
	// consumed directly to avoid eight unnecessary expensive password hashes.
	if w := login("203.0.113.4", "invalid-password"); w.Code != http.StatusUnauthorized {
		t.Fatalf("failed login: %d", w.Code)
	}

	for i := 2; i < loginAttempts; i++ {
		if !s.auth.allowAttempt("203.0.113.4", time.Now()) {
			t.Fatalf("attempt %d limited early", i)
		}
	}

	if w := login("203.0.113.4", "invalid-password"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("first client was not rate limited: %d", w.Code)
	}

	password, err := os.ReadFile(filepath.Join(dir, "bootstrap-password"))
	if err != nil {
		t.Fatal(err)
	}

	if w := login("198.51.100.5", string(password)); w.Code != http.StatusSeeOther {
		t.Fatalf("second client cannot sign in: %d %s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("audit entry count: %d", len(lines))
	}

	for i, line := range lines {
		var entry struct {
			Action, IP string
		}

		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}

		wantIP, wantAction := "203.0.113.4", "login_failed"
		if i == 2 {
			wantAction = "login_rate_limited"
		}
		if i == 3 {
			wantIP, wantAction = "198.51.100.5", "login"
		}

		if entry.IP != wantIP || entry.Action != wantAction {
			t.Fatalf("audit entry %d has wrong identity: %+v", i, entry)
		}
	}
}
