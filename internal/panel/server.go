package panel

import (
	"bytes"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

//go:embed web/*.html web/static/*
var assets embed.FS

type Config struct {
	StateDir     string
	Socket       string
	SecureCookie bool
	AllowedHosts []string
	Version      string
}

type View struct {
	Page, Title, CSRF, Error, Flash, Username, AgentError, Version string
	Status                                                         *protocol.Status
	Users                                                          []protocol.User
	Clients                                                        []protocol.ClientMetric
	Logs                                                           []string
	Certificates                                                   []protocol.Certificate
	Export                                                         *protocol.Export
}

type Server struct {
	config    Config
	auth      *auth
	agent     *agentClient
	templates *template.Template
	handler   http.Handler
	auditMu   sync.Mutex
}

func New(config Config) (*Server, error) {
	a, err := newAuth(config.StateDir)
	if err != nil {
		return nil, err
	}
	t, err := template.New("panel").Funcs(template.FuncMap{
		"bytes": formatBytes,
		"datetime": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			return t.UTC().Format("02.01.2006 15:04 UTC")
		},
		"uptime": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			d := time.Since(t)
			if d < 0 {
				return "—"
			}
			return d.Round(time.Second).String()
		},
		"remaining": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}
			days := int(time.Until(t).Hours() / 24)
			if days < 0 {
				return "Истёк"
			}
			return fmt.Sprintf("%d дн.", days)
		},
		"urlencode": url.QueryEscape,
	}).ParseFS(assets, "web/*.html")
	if err != nil {
		return nil, err
	}
	s := &Server{config: config, auth: a, agent: newAgentClient(config.Socket), templates: t}
	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("GET /{$}", s.protect(func(w http.ResponseWriter, r *http.Request, se session) { s.page(w, r, se, "dashboard", "") }))
	for _, page := range []string{"users", "logs", "settings"} {
		mux.HandleFunc("GET /"+page, s.protect(func(w http.ResponseWriter, r *http.Request, se session) { s.page(w, r, se, page, "") }))
	}
	mux.HandleFunc("GET /users/export", s.protect(s.export))
	mux.HandleFunc("GET /users/qr", s.protect(s.qr))
	mux.HandleFunc("POST /logout", s.protect(s.logout))
	mux.HandleFunc("POST /password", s.protect(s.password))
	for _, action := range []string{"users/add", "users/edit", "users/delete", "apply", "discard", "restart", "tls/reload"} {
		mux.HandleFunc("POST /"+action, s.protect(s.action))
	}
	s.handler = s.security(mux)
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		allowed := host == "localhost" || host == "127.0.0.1" || host == "::1"
		for _, h := range s.config.AllowedHosts {
			if strings.EqualFold(host, h) {
				allowed = true
			}
		}
		if !allowed {
			http.Error(w, "Unknown host", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
					http.Error(w, "Недопустимый источник запроса", http.StatusForbidden)
					return
				}
			}
			if err := r.ParseForm(); err != nil {
				http.Error(w, "Некорректный запрос", http.StatusBadRequest)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func sameToken(a, b string) bool {
	return len(a) >= 32 && len(b) >= 32 && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) protect(fn func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("tp_session")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		se, ok := s.auth.lookup(cookie.Value, time.Now())
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost && !sameToken(se.CSRF, r.PostForm.Get("csrf")) {
			http.Error(w, "Сессия формы истекла. Обновите страницу.", http.StatusForbidden)
			return
		}
		fn(w, r, se)
	}
}

func (s *Server) cookie(w http.ResponseWriter, name, value, path string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: path, MaxAge: maxAge, HttpOnly: true, Secure: s.config.SecureCookie, SameSite: http.SameSiteStrictMode})
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	token := randomToken()
	s.cookie(w, "tp_login", token, "/login", 900)
	s.render(w, View{Page: "login", Title: "Вход", CSRF: token, Version: s.config.Version, Flash: notice(r.URL.Query().Get("notice"))})
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("tp_login")
	if err != nil || !sameToken(cookie.Value, r.PostForm.Get("csrf")) {
		http.Error(w, "Обновите страницу входа.", http.StatusForbidden)
		return
	}
	ip := remoteIP(r)
	if !s.auth.allowAttempt(ip, time.Now()) {
		http.Error(w, "Слишком много попыток. Повторите через 15 минут.", http.StatusTooManyRequests)
		return
	}
	password := r.PostForm.Get("password")
	var id string
	var authenticated bool
	if len(password) <= 128 {
		id, authenticated = s.auth.authenticate(r.PostForm.Get("username"), password, time.Now())
	}
	if !authenticated {
		s.audit("login_failed", ip, false)
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, View{Page: "login", Title: "Вход", CSRF: cookie.Value, Error: "Неверное имя пользователя или пароль.", Version: s.config.Version})
		return
	}
	s.auth.clearAttempts(ip)
	s.cookie(w, "tp_session", id, "/", 8*3600)
	s.cookie(w, "tp_login", "", "/login", -1)
	s.audit("login", ip, true)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, se session) {
	if c, err := r.Cookie("tp_session"); err == nil {
		s.auth.removeSession(c.Value)
	}
	s.cookie(w, "tp_session", "", "/", -1)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) page(w http.ResponseWriter, r *http.Request, se session, page, errMessage string) {
	v := View{Page: page, Title: map[string]string{"dashboard": "Обзор", "users": "Пользователи", "logs": "Журнал", "settings": "Настройки"}[page], CSRF: se.CSRF, Username: "admin", Version: s.config.Version, Error: errMessage, Flash: notice(r.URL.Query().Get("notice"))}
	var st protocol.Status
	if err := s.agent.call(r.Context(), "GET", "/v1/status", nil, &st); err != nil {
		v.AgentError = err.Error()
	} else {
		v.Status = &st
	}
	var err error
	switch page {
	case "dashboard":
		err = s.agent.call(r.Context(), "GET", "/v1/clients", nil, &v.Clients)
	case "users":
		var us protocol.Users
		err = s.agent.call(r.Context(), "GET", "/v1/users", nil, &us)
		v.Users = us.Users
	case "logs":
		var logs protocol.LogResponse
		err = s.agent.call(r.Context(), "GET", "/v1/logs", nil, &logs)
		v.Logs = logs.Lines
	case "settings":
		err = s.agent.call(r.Context(), "GET", "/v1/certificates", nil, &v.Certificates)
	}
	if err != nil && v.AgentError == "" {
		v.AgentError = err.Error()
	}
	s.render(w, v)
}

func (s *Server) render(w http.ResponseWriter, v View) {
	var b bytes.Buffer
	if err := s.templates.ExecuteTemplate(&b, v.Page, v); err != nil {
		http.Error(w, "Не удалось отобразить страницу.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b.Bytes())
}

func validNewPassword(password string) bool {
	if len(password) < 12 || len(password) > 128 || strings.TrimSpace(password) != password {
		return false
	}
	for _, c := range password {
		if c < 32 || c > 126 || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}

func (s *Server) action(w http.ResponseWriter, r *http.Request, se session) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	target := "/users"
	method := "POST"
	apiPath := "/v1/" + path
	var input any
	if strings.HasPrefix(path, "users/") {
		apiPath = "/v1/users"
		change := protocol.UserChange{Username: strings.TrimSpace(r.PostForm.Get("username")), Password: r.PostForm.Get("password")}
		if path == "users/add" || (path == "users/edit" && change.Password != "") {
			if !validNewPassword(change.Password) {
				s.page(w, r, se, "users", "Пароль: 12–128 печатных ASCII-символов, без кавычек, обратной косой черты и пробелов по краям.")
				return
			}
		}
		if path == "users/edit" {
			method = "PATCH"
			if e := r.PostForm.Get("enabled"); e != "" {
				if e != "true" && e != "false" {
					http.Error(w, "Некорректное состояние", 400)
					return
				}
				enabled := e == "true"
				change.Enabled = &enabled
			}
		} else if path == "users/delete" {
			method = "DELETE"
			change.Password = ""
		}
		input = change
	} else if path == "restart" {
		target = "/"
	} else if path == "tls/reload" {
		target = "/settings"
	}
	err := s.agent.call(r.Context(), method, apiPath, input, nil)
	s.audit(path, remoteIP(r), err == nil)
	if err != nil {
		page := strings.TrimPrefix(target, "/")
		if page == "" {
			page = "dashboard"
		}
		s.page(w, r, se, page, err.Error())
		return
	}
	http.Redirect(w, r, target+"?notice="+url.QueryEscape(path), http.StatusSeeOther)
}

func (s *Server) password(w http.ResponseWriter, r *http.Request, se session) {
	err := s.auth.changePassword(r.PostForm.Get("current_password"), r.PostForm.Get("new_password"))
	s.audit("password", remoteIP(r), err == nil)
	if err != nil {
		s.page(w, r, se, "settings", err.Error())
		return
	}
	s.cookie(w, "tp_session", "", "/", -1)
	http.Redirect(w, r, "/login?notice=password", http.StatusSeeOther)
}

func (s *Server) export(w http.ResponseWriter, r *http.Request, se session) {
	var ex protocol.Export
	err := s.agent.call(r.Context(), "GET", "/v1/export?username="+url.QueryEscape(r.URL.Query().Get("username")), nil, &ex)
	if err != nil {
		s.page(w, r, se, "users", err.Error())
		return
	}
	var st protocol.Status
	_ = s.agent.call(r.Context(), "GET", "/v1/status", nil, &st)
	s.render(w, View{Page: "export", Title: "Подключение", CSRF: se.CSRF, Username: "admin", Version: s.config.Version, Status: &st, Export: &ex})
}

func (s *Server) qr(w http.ResponseWriter, r *http.Request, se session) {
	var ex protocol.Export
	if err := s.agent.call(r.Context(), "GET", "/v1/export?username="+url.QueryEscape(r.URL.Query().Get("username")), nil, &ex); err != nil {
		http.Error(w, "Конфигурация недоступна", http.StatusBadRequest)
		return
	}
	png, err := qrcode.Encode(ex.DeepLink, qrcode.Medium, 384)
	if err != nil {
		http.Error(w, "Не удалось создать QR-код", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(png)
}

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
	_ = json.NewEncoder(f).Encode(map[string]any{"time": time.Now().UTC(), "action": action, "ip": ip, "success": ok})
}

func formatBytes(n uint64) string {
	units := []string{"Б", "КиБ", "МиБ", "ГиБ", "ТиБ"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", n, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func notice(key string) string {
	return map[string]string{"users/add": "Пользователь добавлен в черновик.", "users/edit": "Изменения пользователя сохранены в черновике.", "users/delete": "Удаление сохранено в черновике.", "apply": "Изменения применены. TrustTunnel перезапущен.", "discard": "Черновик отменён.", "restart": "TrustTunnel перезапущен.", "tls/reload": "Сертификаты перечитаны.", "password": "Пароль изменён. Войдите с новым паролем."}[key]
}
