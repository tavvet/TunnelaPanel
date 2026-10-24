package endpoint

import (
	"context"
	"sync"
	"time"
)

// Agent owns persistent user state and coordinates the TrustTunnel process.
type Agent struct {
	// mu guards store and serializes process transitions. Background process
	// completion and log writes use their own mutexes and never acquire mu.
	mu      sync.Mutex
	config  Config
	store   *store
	process *supervisor
	logs    *logRing
}

// New recovers an interrupted apply and loads state without starting TrustTunnel.
// Call Start before serving the control API.
func New(c Config) (*Agent, error) {
	if err := c.normalize(); err != nil {
		return nil, err
	}
	credentials, listener, err := readRuntime(c)
	if err != nil {
		return nil, err
	}
	if err = checkCertificateFiles(c.ConfigDir); err != nil {
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

// Start launches TrustTunnel with the applied credentials and waits for readiness.
func (a *Agent) Start(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.process.readVersion()
	return a.process.restart(ctx)
}

// Close stops TrustTunnel with a bounded graceful-shutdown period.
func (a *Agent) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.process.stop()
}

// ReloadTLS validates certificate/key pairs and requests a TLS-only reload.
func (a *Agent) ReloadTLS() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reloadTLS()
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
