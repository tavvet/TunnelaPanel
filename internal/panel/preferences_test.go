package panel

import (
	"bytes"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

func signInTestPanel(t *testing.T, s *Server) (*http.Cookie, string) {
	t.Helper()
	page := request(s, http.MethodGet, "/login", nil)
	var loginCookie *http.Cookie
	for _, cookie := range page.Result().Cookies() {
		if cookie.Name == "tp_login" {
			loginCookie = cookie
		}
	}
	if page.Code != http.StatusOK || loginCookie == nil {
		t.Fatal("login page did not issue a CSRF cookie")
	}
	password, err := os.ReadFile(filepath.Join(s.config.StateDir, "bootstrap-password"))
	if err != nil {
		t.Fatal(err)
	}
	login := request(s, http.MethodPost, "/login", url.Values{
		"username": {"admin"},
		"password": {string(password)},
		"csrf":     {loginCookie.Value},
	}, loginCookie)
	if login.Code != http.StatusSeeOther {
		t.Fatalf("login returned %d", login.Code)
	}
	for _, cookie := range login.Result().Cookies() {
		if cookie.Name == "tp_session" {
			se, ok := s.auth.lookup(cookie.Value, time.Now())
			if !ok {
				t.Fatal("login did not create a session")
			}
			return cookie, se.CSRF
		}
	}
	t.Fatal("login did not return a session cookie")
	return nil, ""
}

func TestPreferencesSwitchAllHTMLRoutesWithoutEndpointChanges(t *testing.T) {
	s, calls := newTestPanel(t)
	cookie, csrf := signInTestPanel(t, s)
	routes := []struct {
		path, en, ru string
	}{
		{"/", "Overview", "Обзор"},
		{"/users", "Users", "Пользователи"},
		{"/logs", "Event log", "Журнал событий"},
		{"/settings", "Settings", "Настройки"},
		{"/users/export?username=alice", "Connection", "Подключение"},
		{"/login", "Welcome back", "С возвращением"},
	}

	for step, language := range []string{"en", "ru", "en"} {
		if step > 0 {
			before := calls.Load()
			saved := request(s, http.MethodPost, "/settings/preferences", url.Values{
				"csrf":     {csrf},
				"language": {language},
			}, cookie)
			if saved.Code != http.StatusSeeOther || saved.Header().Get("Location") != "/settings?notice=preferences" {
				t.Fatalf("saving %s returned status %d and location %q", language, saved.Code, saved.Header().Get("Location"))
			}
			if calls.Load() != before {
				t.Fatal("saving a UI preference contacted the endpoint")
			}
			if _, ok := s.auth.lookup(cookie.Value, time.Now()); !ok {
				t.Fatal("language change invalidated the administrator session")
			}
		}

		for _, route := range routes {
			page := request(s, http.MethodGet, route.path, nil, cookie)
			body := page.Body.String()
			if page.Code != http.StatusOK || page.Header().Get("Content-Language") != language ||
				!strings.Contains(body, `<html lang="`+language+`">`) {
				t.Fatalf("%s did not render language %s (status %d)", route.path, language, page.Code)
			}
			want := route.en
			if language == "ru" {
				want = route.ru
			}
			heading := `<h1>\s*` + regexp.QuoteMeta(want) + `(?:\s|<)`
			if route.path == "/login" {
				heading = `<h2 id="login-title">\s*` + regexp.QuoteMeta(want) + `\s*</h2>`
			}
			if !regexp.MustCompile(heading).MatchString(body) {
				t.Fatalf("%s omitted its %s heading", route.path, language)
			}
			if page.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("%s became cacheable after language selection", route.path)
			}
			if strings.Contains(body, "<script>alert(1)</script>") || strings.Contains(body, "%!") {
				t.Fatalf("%s exposed unescaped content or a translation format error", route.path)
			}
			if route.path == "/logs" && !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
				t.Fatal("language selection changed the original endpoint log")
			}
			if route.path == "/users/export?username=alice" &&
				(!strings.Contains(body, "example-secret") || !strings.Contains(body, "tt://?example-safe-test")) {
				t.Fatal("language selection changed the exported client configuration")
			}
			if route.path == "/settings" {
				if !strings.Contains(body, `<option value="`+language+`" selected>`) ||
					!strings.Contains(body, `action="/settings/preferences"`) {
					t.Fatal("settings did not display the selected language and save form")
				}
			}
			if route.path == "/" || route.path == "/login" {
				wait := html.EscapeString(translate(language, "ui.js.wait"))
				if !strings.Contains(body, `data-wait="`+wait+`"`) {
					t.Fatalf("%s omitted localized JavaScript messages", route.path)
				}
			}
		}
	}
}

func TestPreferencePersistsAcrossRestartAndAppliesToAnonymousLogin(t *testing.T) {
	s, calls := newTestPanel(t)
	cookie, csrf := signInTestPanel(t, s)
	before := map[string][]byte{}
	for _, name := range []string{"auth.json", "bootstrap-password"} {
		data, err := os.ReadFile(filepath.Join(s.config.StateDir, name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = data
	}
	startCalls := calls.Load()
	saved := request(s, http.MethodPost, "/settings/preferences", url.Values{
		"csrf":     {csrf},
		"language": {"ru"},
	}, cookie)
	if saved.Code != http.StatusSeeOther || calls.Load() != startCalls {
		t.Fatal("preference save failed or contacted endpoint")
	}

	restarted, err := New(s.config)
	if err != nil {
		t.Fatal(err)
	}
	page := request(restarted, http.MethodGet, "/login", nil)
	if page.Code != http.StatusOK || page.Header().Get("Content-Language") != "ru" ||
		!strings.Contains(page.Body.String(), `lang="ru"`) || !strings.Contains(page.Body.String(), "С возвращением") {
		t.Fatal("restart lost the global language preference on anonymous login")
	}
	if calls.Load() != startCalls {
		t.Fatal("loading preferences or anonymous login contacted endpoint")
	}
	for name, want := range before {
		got, err := os.ReadFile(filepath.Join(s.config.StateDir, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("preference save or restart changed %s", name)
		}
	}
	signInTestPanel(t, restarted)
}

func TestPreferencePostRequiresAuthenticationAndCSRF(t *testing.T) {
	s, calls := newTestPanel(t)
	unauthenticated := request(s, http.MethodPost, "/settings/preferences", url.Values{"language": {"ru"}})
	if unauthenticated.Code != http.StatusSeeOther || unauthenticated.Header().Get("Location") != "/login" {
		t.Fatal("anonymous preference save was not redirected to login")
	}
	cookie, _ := signInTestPanel(t, s)
	for _, token := range []string{"", "invalid-token"} {
		response := request(s, http.MethodPost, "/settings/preferences", url.Values{
			"csrf":     {token},
			"language": {"ru"},
		}, cookie)
		if response.Code != http.StatusForbidden {
			t.Fatalf("invalid CSRF token returned %d", response.Code)
		}
	}
	if s.settings.language() != "en" || calls.Load() != 0 {
		t.Fatal("rejected request changed language or reached endpoint")
	}
}

func TestPreferencePostValidationAndWriteErrorsAreLocal(t *testing.T) {
	s, calls := newTestPanel(t)
	cookie, csrf := signInTestPanel(t, s)
	for _, language := range []string{"", "de", "EN", " ru ", "en-US"} {
		response := request(s, http.MethodPost, "/settings/preferences", url.Values{
			"csrf":     {csrf},
			"language": {language},
		}, cookie)
		if response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), "Choose English or Russian.") {
			t.Fatalf("unsupported language %q returned status %d", language, response.Code)
		}
	}
	if s.settings.language() != "en" || calls.Load() != 0 {
		t.Fatal("invalid language was published or reached endpoint")
	}

	if err := s.settings.setLanguage("ru"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.config.StateDir, "settings.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	response := request(s, http.MethodPost, "/settings/preferences", url.Values{
		"csrf":     {csrf},
		"language": {"en"},
	}, cookie)
	if response.Code != http.StatusInternalServerError ||
		!strings.Contains(response.Body.String(), "Не удалось сохранить настройки панели.") {
		t.Fatalf("failed preference write returned status %d or an unlocalized error", response.Code)
	}
	if s.settings.language() != "ru" || calls.Load() != 0 {
		t.Fatal("failed write published language or contacted endpoint")
	}
}

func TestConcurrentLanguageChangesKeepEachLoginResponseConsistent(t *testing.T) {
	s, err := New(Config{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	failures := make(chan error, 5)
	var wg sync.WaitGroup
	for worker := 0; worker < 5; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for iteration := 0; iteration < 24; iteration++ {
				if worker == 0 {
					if err := s.settings.setLanguage([]string{"ru", "en"}[iteration%2]); err != nil {
						failures <- err
						return
					}
					continue
				}

				page := request(s, http.MethodGet, "/login", nil)
				language := page.Header().Get("Content-Language")
				welcome := "Welcome back"
				if language == "ru" {
					welcome = "С возвращением"
				}
				body := page.Body.String()
				if page.Code != http.StatusOK || (language != "en" && language != "ru") ||
					!strings.Contains(body, `<html lang="`+language+`">`) ||
					!strings.Contains(body, `<h2 id="login-title">`+welcome+`</h2>`) ||
					!strings.Contains(body, `data-wait="`+html.EscapeString(translate(language, "ui.js.wait"))+`"`) {
					failures <- fmt.Errorf("login response mixed language snapshot %q or failed with status %d", language, page.Code)
					return
				}
			}
		}(worker)
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}
