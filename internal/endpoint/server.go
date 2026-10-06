package endpoint

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
		case r.URL.Path == "/v1/users" && (r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodDelete):
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

func (a *Agent) Serve(ctx context.Context) error {
	monitorCtx, stopMonitor := context.WithCancel(ctx)
	defer stopMonitor()
	go a.monitor(monitorCtx)
	dir := filepath.Dir(a.config.Socket)
	if os.MkdirAll(dir, 0700) != nil {
		return errors.New("cannot create control socket directory")
	}
	if os.Chmod(dir, 0700) != nil {
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
	if os.Chmod(a.config.Socket, 0600) != nil {
		return errors.New("cannot secure control socket")
	}
	server := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
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

func (a *Agent) clients(ctx context.Context) ([]protocol.ClientMetric, error) {
	u, err := url.Parse(a.config.MetricsURL)
	if err != nil || u.Scheme != "http" || u.User != nil {
		return nil, errors.New("invalid private metrics URL")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("metrics URL must use a loopback IP address")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/clients"
	u.RawQuery = ""
	u.Fragment = ""
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, errors.New("cannot create metrics request")
	}
	client := http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("endpoint metrics are unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("endpoint metrics returned an error")
	}
	var metrics []protocol.ClientMetric
	if json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&metrics) != nil {
		return nil, errors.New("invalid endpoint metrics")
	}
	if metrics == nil {
		metrics = []protocol.ClientMetric{}
	}
	return metrics, nil
}
func (a *Agent) certificates() ([]protocol.Certificate, error) {
	doc, _, err := readTOML(filepath.Join(a.config.ConfigDir, "hosts.toml"))
	if err != nil {
		return nil, err
	}
	out := []protocol.Certificate{}
	for _, key := range []string{"main_hosts", "fallback_hosts"} {
		hosts, _ := doc[key].([]any)
		for _, entry := range hosts {
			h, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			name, _ := h["hostname"].(string)
			path, _ := h["cert_chain_path"].(string)
			item := protocol.Certificate{Hostname: name}
			if !filepath.IsAbs(path) {
				path = filepath.Join(a.config.ConfigDir, path)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				item.Error = "cannot read certificate"
			} else {
				block, _ := pem.Decode(b)
				if block == nil {
					item.Error = "invalid certificate PEM"
				} else {
					cert, err := x509.ParseCertificate(block.Bytes)
					if err != nil {
						item.Error = "invalid certificate"
					} else {
						item.NotAfter = cert.NotAfter
					}
				}
			}
			out = append(out, item)
		}
	}
	return out, nil
}
