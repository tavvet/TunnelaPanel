package endpoint

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Agent struct {
	mu      sync.Mutex
	config  Config
	store   *store
	process *supervisor
	logs    *logRing
}

type transaction struct {
	Credentials []byte `json:"credentials"`
	State       []byte `json:"state"`
}

func New(c Config) (*Agent, error) {
	if err := c.normalize(); err != nil {
		return nil, err
	}
	credentials, listener, err := readRuntime(c)
	if err != nil {
		return nil, err
	}
	if err = recoverTransaction(c.StateDir, credentials); err != nil {
		return nil, err
	}

	s, err := newStore(credentials, c.StateDir)
	if err != nil {
		return nil, err
	}
	logs := &logRing{}
	logs.addSecrets(s.applied)
	logs.addSecrets(s.draft)

	return &Agent{
		config: c,
		store:  s,
		logs:   logs,
		process: &supervisor{
			config:   c,
			listener: listener,
			logs:     logs,
		},
	}, nil
}

func recoverTransaction(stateDir, credentials string) error {
	path := filepath.Join(stateDir, "apply.json")
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.New("cannot inspect interrupted apply")
	}
	var tx transaction
	if json.Unmarshal(b, &tx) != nil || len(tx.Credentials) == 0 || len(tx.State) == 0 {
		return errors.New("invalid interrupted apply journal")
	}
	if atomicWrite(credentials, tx.Credentials) != nil ||
		atomicWrite(filepath.Join(stateDir, "state.json"), tx.State) != nil {
		return errors.New("cannot recover interrupted apply")
	}
	if durableRemove(path) != nil {
		return errors.New("cannot finish apply recovery")
	}
	return nil
}

func (a *Agent) apply(ctx context.Context) error {
	if err := a.store.drift(); err != nil {
		return err
	}
	if !a.store.pending() {
		return nil
	}

	oldBytes, err := os.ReadFile(a.store.path)
	if err != nil {
		return errors.New("cannot read active credentials")
	}
	oldState, err := os.ReadFile(a.store.statePath)
	if err != nil {
		return errors.New("cannot read saved state")
	}
	newBytes, err := a.store.draft.encode(true)
	if err != nil {
		return err
	}

	backupDir := filepath.Join(a.config.StateDir, "backups")
	if os.MkdirAll(backupDir, 0700) != nil {
		return errors.New("cannot create backup directory")
	}
	backup := filepath.Join(backupDir, time.Now().UTC().Format("20060102T150405.000000000Z")+".json")
	tx, err := json.Marshal(transaction{Credentials: oldBytes, State: oldState})
	if err != nil {
		return errors.New("cannot prepare backup")
	}
	if atomicWrite(backup, tx) != nil {
		return errors.New("cannot save credentials backup")
	}
	journal := filepath.Join(a.config.StateDir, "apply.json")
	if atomicWrite(journal, tx) != nil {
		return errors.New("cannot create apply journal")
	}

	oldApplied, oldHash := a.store.applied, a.store.hash
	rollback := func(cause error) error {
		_ = a.process.stop()
		if atomicWrite(a.store.path, oldBytes) != nil {
			return errors.New("apply failed and credentials rollback failed; restore the saved backup")
		}
		a.store.applied = oldApplied
		a.store.hash = oldHash
		if atomicWrite(a.store.statePath, oldState) != nil {
			return errors.New("apply failed and state rollback failed; restore the saved backup")
		}
		if err := a.process.restart(context.Background()); err != nil {
			return errors.New("apply failed; previous settings restored but endpoint did not restart")
		}
		_ = durableRemove(journal)
		return errors.New(cause.Error() + "; previous settings restored")
	}

	if atomicWrite(a.store.path, newBytes) != nil {
		return rollback(errors.New("cannot write new credentials"))
	}
	if err := a.process.restart(ctx); err != nil {
		return rollback(err)
	}

	a.store.applied = clone(a.store.draft)
	a.store.hash = digest(newBytes)
	if err := a.store.persist(); err != nil {
		return rollback(err)
	}
	if durableRemove(journal) != nil {
		return rollback(errors.New("cannot finish apply transaction"))
	}
	return nil
}

func (a *Agent) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.process.readVersion()
	return a.process.restart(ctx)
}

func (a *Agent) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.process.stop()
}

func (a *Agent) ReloadTLS() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reloadTLS()
}

func (a *Agent) reloadTLS() error {
	doc, _, err := readTOML(filepath.Join(a.config.ConfigDir, "hosts.toml"))
	if err != nil {
		return err
	}
	hosts, _ := doc["main_hosts"].([]any)
	if len(hosts) == 0 {
		return errors.New("TLS configuration has no main hosts")
	}
	for _, entry := range hosts {
		h, ok := entry.(map[string]any)
		if !ok {
			return errors.New("invalid TLS host configuration")
		}
		cert, _ := h["cert_chain_path"].(string)
		key, _ := h["private_key_path"].(string)
		if cert == "" || key == "" {
			return errors.New("TLS certificate and key paths are required")
		}
		if !filepath.IsAbs(cert) {
			cert = filepath.Join(a.config.ConfigDir, cert)
		}
		if !filepath.IsAbs(key) {
			key = filepath.Join(a.config.ConfigDir, key)
		}
		if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
			return errors.New("TLS certificate or private key is invalid; endpoint kept running")
		}
	}
	return a.process.reload()
}

// The API has no "stop" operation: outside a serialized apply/restart, the
// desired state is running. Recover unexpected exits with bounded backoff.
func (a *Agent) monitor(ctx context.Context) {
	delay := 5 * time.Second
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := a.recoverProcess(ctx); err != nil {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		} else {
			delay = 5 * time.Second
		}
	}
}

func (a *Agent) recoverProcess(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if a.process.status().Running {
		return nil
	}
	if err := a.store.drift(); err != nil {
		return err
	}
	_, _ = a.logs.Write([]byte("[agent] Restarting endpoint after an unexpected exit\n"))
	return a.process.restart(ctx)
}
