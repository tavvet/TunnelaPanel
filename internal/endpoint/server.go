package endpoint

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

func reply(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, code int, err error) {
	reply(w, code, protocol.Error{Error: err.Error()})
}

func decodeChange(w http.ResponseWriter, r *http.Request) (protocol.UserChange, error) {
	var c protocol.UserChange
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return c, errors.New("invalid user request")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, errors.New("invalid user request")
	}
	return c, nil
}

// Handler exposes the private control API. Serve restricts access through Unix
// socket permissions; the handler itself does not authenticate HTTP clients.
// The socket's owner and the agent's primary group may connect; others may not.
func (a *Agent) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
			reply(w, 200, protocol.Result{Message: "ok"})
			return
		}
		if r.URL.Path == "/v1/logs" && r.Method == http.MethodGet {
			reply(w, 200, a.logs.read())
			return
		}
		if r.URL.Path == "/v1/clients" && r.Method == http.MethodGet {
			v, err := a.clients(r.Context())
			if err != nil {
				fail(w, 502, err)
				return
			}
			reply(w, 200, v)
			return
		}
		if r.URL.Path == "/v1/certificates" && r.Method == http.MethodGet {
			v, err := a.certificates()
			if err != nil {
				fail(w, 500, err)
				return
			}
			reply(w, 200, v)
			return
		}

		a.mu.Lock()
		defer a.mu.Unlock()

		switch {
		case r.URL.Path == "/v1/status" && r.Method == http.MethodGet:
			s := a.process.status()
			s.Pending = a.store.pending()
			s.PublicAddress = a.config.PublicAddress
			if err := a.store.drift(); err != nil {
				s.LastError = err.Error()
			}
			reply(w, 200, s)
		case r.URL.Path == "/v1/users" && r.Method == http.MethodGet:
			reply(w, 200, a.store.list())
		case r.URL.Path == "/v1/users" &&
			(r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodDelete):
			c, err := decodeChange(w, r)
			if err != nil {
				fail(w, 400, err)
				return
			}
			if err = a.store.drift(); err != nil {
				fail(w, 409, err)
				return
			}
			if err = a.store.change(r.Method, c); err != nil {
				fail(w, 400, err)
				return
			}
			a.logs.addSecrets(a.store.draft)
			reply(w, 200, protocol.Result{Message: "draft saved"})
		case r.URL.Path == "/v1/apply" && r.Method == http.MethodPost:
			if err := a.apply(r.Context()); err != nil {
				fail(w, 500, err)
				return
			}
			reply(w, 200, protocol.Result{Message: "settings applied"})
		case r.URL.Path == "/v1/discard" && r.Method == http.MethodPost:
			old := a.store.draft
			a.store.draft = clone(a.store.applied)
			if err := a.store.persist(); err != nil {
				a.store.draft = old
				fail(w, 500, err)
				return
			}
			reply(w, 200, protocol.Result{Message: "draft discarded"})
		case r.URL.Path == "/v1/restart" && r.Method == http.MethodPost:
			if err := a.store.drift(); err != nil {
				fail(w, 409, err)
				return
			}
			if err := a.process.restart(r.Context()); err != nil {
				fail(w, 500, err)
				return
			}
			reply(w, 200, protocol.Result{Message: "endpoint restarted"})
		case r.URL.Path == "/v1/tls/reload" && r.Method == http.MethodPost:
			if err := a.reloadTLS(); err != nil {
				fail(w, 500, err)
				return
			}
			reply(w, 200, protocol.Result{Message: "TLS reload requested"})
		case r.URL.Path == "/v1/export" && r.Method == http.MethodGet:
			user := r.URL.Query().Get("username")
			found := false
			for _, u := range a.store.applied.Users {
				if u.Enabled && u.Fields["username"] == user {
					found = true
					break
				}
			}
			if !found {
				fail(w, 404, errors.New("applied enabled user not found"))
				return
			}
			if err := a.store.drift(); err != nil {
				fail(w, 409, err)
				return
			}
			out, err := a.process.export(r.Context(), user)
			if err != nil {
				fail(w, 500, err)
				return
			}
			reply(w, 200, out)
		default:
			fail(w, 404, errors.New("unknown agent endpoint or method"))
		}
	})
}

// Serve runs the Unix-socket API and crash-recovery monitor until ctx is canceled.
// Start must be called first; Close shuts down the child after serving ends.
func (a *Agent) Serve(ctx context.Context) error {
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	defer stopMonitor()
	go a.monitor(monitorCtx)

	// The panel runs as a different user in the agent's primary group. Grant that
	// group directory search and socket access only; the owner needs no capability
	// to change a file's group to one of its own groups.
	dir := filepath.Dir(a.config.Socket)
	gid := os.Getegid()
	if os.MkdirAll(dir, 0700) != nil {
		return errors.New("cannot create control socket directory")
	}
	if os.Chown(dir, -1, gid) != nil || os.Chmod(dir, 0710) != nil {
		return errors.New("cannot secure control socket directory")
	}
	if info, err := os.Lstat(a.config.Socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("control socket path is occupied by another file")
		}
		conn, err := net.DialTimeout("unix", a.config.Socket, 300*time.Millisecond)
		if err == nil {
			conn.Close()
			return errors.New("control socket is already in use")
		}
		if os.Remove(a.config.Socket) != nil {
			return errors.New("cannot remove stale control socket")
		}
	} else if !os.IsNotExist(err) {
		return errors.New("cannot inspect control socket")
	}

	listener, err := net.Listen("unix", a.config.Socket)
	if err != nil {
		return errors.New("cannot open control socket")
	}
	defer listener.Close()
	defer os.Remove(a.config.Socket)
	if os.Chown(a.config.Socket, -1, gid) != nil || os.Chmod(a.config.Socket, 0660) != nil {
		return errors.New("cannot secure control socket")
	}

	server := &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			stop, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_ = server.Shutdown(stop)
		case <-stopped:
		}
	}()

	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
