package panel

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"sync"
	"time"
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
	settings       *panelSettings
	agent          *agentClient
	templates      *template.Template
	handler        http.Handler
	auditMu        sync.Mutex
	trustedProxies trustedProxyPolicy

	// auditMu also guards the login_failed audit limit.
	failedWindow     time.Time
	failedCount      int
	failedSuppressed int
}

// New validates configuration, loads or bootstraps admin credentials, and builds the routes.
func New(config Config) (*Server, error) {
	// Reject an invalid trust policy before creating any authentication state.
	trustedProxies, err := parseTrustedProxies(config.TrustedProxies)
	if err != nil {
		return nil, err
	}

	settings, err := newPanelSettings(config.StateDir)
	if err != nil {
		return nil, stateDirError(config.StateDir, err)
	}

	a, err := newAuth(config.StateDir)
	if err != nil {
		return nil, stateDirError(config.StateDir, err)
	}

	t, err := newTemplates()
	if err != nil {
		return nil, err
	}

	s := &Server{
		config:         config,
		auth:           a,
		settings:       settings,
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
	mux.HandleFunc("POST /settings/preferences", s.protect(s.preferences))
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

// stateDirError explains the usual cause of a permission failure: a state
// directory created by root before the panel started running as a regular user.
func stateDirError(dir string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%w; PANEL_STATE_DIR %s must be owned by the panel user (uid %d)", err, dir, os.Getuid())
	}
	return err
}

// ServeHTTP applies security checks and dispatches the request to a panel route.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Snapshot once per request so a concurrent settings save cannot mix
	// languages between the page title, body, errors, and response headers.
	language := s.settings.language()
	w.Header().Set("Content-Language", language)
	r = r.WithContext(context.WithValue(r.Context(), languageContextKey{}, language))
	s.handler.ServeHTTP(w, r)
}
