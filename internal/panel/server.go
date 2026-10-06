package panel

import (
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"sync"
)

// Config defines persistent storage, agent access, and HTTP security settings.
type Config struct {
	StateDir       string
	Socket         string
	SecureCookie   bool
	AllowedHosts   []string
	TrustedProxies []string
	Version        string
}

// Server serves the admin UI and sends endpoint operations through the agent socket.
type Server struct {
	config         Config
	auth           *auth
	agent          *agentClient
	templates      *template.Template
	handler        http.Handler
	auditMu        sync.Mutex
	trustedProxies trustedProxyPolicy
}

// New validates configuration, loads or bootstraps admin credentials, and builds the routes.
func New(config Config) (*Server, error) {
	// Reject an invalid trust policy before creating any authentication state.
	trustedProxies, err := parseTrustedProxies(config.TrustedProxies)
	if err != nil {
		return nil, err
	}

	a, err := newAuth(config.StateDir)
	if err != nil {
		return nil, err
	}

	t, err := newTemplates()
	if err != nil {
		return nil, err
	}

	s := &Server{
		config:         config,
		auth:           a,
		agent:          newAgentClient(config.Socket),
		templates:      t,
		trustedProxies: trustedProxies,
	}

	mux := http.NewServeMux()
	static, _ := fs.Sub(assets, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	// This probe checks the panel process; endpoint readiness comes from agent status.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc(
		"GET /{$}",
		s.protect(func(w http.ResponseWriter, r *http.Request, se session) {
			s.page(w, r, se, "dashboard", "")
		}),
	)
	for _, page := range []string{
		"users",
		"logs",
		"settings",
	} {
		mux.HandleFunc(
			"GET /"+page,
			s.protect(func(w http.ResponseWriter, r *http.Request, se session) {
				s.page(w, r, se, page, "")
			}),
		)
	}

	mux.HandleFunc("GET /users/export", s.protect(s.export))
	mux.HandleFunc("GET /users/qr", s.protect(s.qr))
	mux.HandleFunc("POST /logout", s.protect(s.logout))
	mux.HandleFunc("POST /password", s.protect(s.password))
	for _, action := range []string{
		"users/add",
		"users/edit",
		"users/delete",
		"apply",
		"discard",
		"restart",
		"tls/reload",
	} {
		mux.HandleFunc("POST /"+action, s.protect(s.action))
	}

	s.handler = s.security(mux)
	return s, nil
}

// ServeHTTP applies security checks and dispatches the request to a panel route.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}
