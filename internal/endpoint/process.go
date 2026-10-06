package endpoint

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

// supervisor tracks one child process. Agent.mu serializes lifecycle operations;
// mu protects identity and status shared with the asynchronous wait goroutine.
type supervisor struct {
	mu       sync.Mutex
	config   Config
	listener string
	logs     *logRing
	cmd      *exec.Cmd
	// done belongs to the most recently started cmd and is closed only by its
	// wait goroutine, after that goroutine updates process status.
	done               chan struct{}
	started            time.Time
	lastError, version string
}

func (p *supervisor) status() protocol.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := protocol.Status{
		StartedAt: p.started,
		Version:   p.version,
		LastError: p.lastError,
	}
	if p.cmd != nil {
		s.Running = true
		s.PID = p.cmd.Process.Pid
	}
	return s
}

func (p *supervisor) readVersion() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p.config.Binary, "--version").Output()
	if err == nil && len(out) < 256 {
		p.mu.Lock()
		p.version = strings.TrimSpace(string(out))
		p.mu.Unlock()
	}
}

func (p *supervisor) start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil {
		return errors.New("endpoint is already running")
	}
	conn, err := net.DialTimeout("tcp", p.listener, 200*time.Millisecond)
	if err == nil {
		conn.Close()
		p.lastError = "endpoint listener is already occupied"
		return errors.New(p.lastError)
	}

	cmd := exec.Command(
		p.config.Binary,
		filepath.Join(p.config.ConfigDir, "vpn.toml"),
		filepath.Join(p.config.ConfigDir, "hosts.toml"),
	)
	cmd.Dir = p.config.ConfigDir
	cmd.Stdout = p.logs
	cmd.Stderr = p.logs
	if cmd.Start() != nil {
		p.lastError = "cannot start endpoint process"
		return errors.New(p.lastError)
	}

	p.cmd = cmd
	p.done = make(chan struct{})
	p.started = time.Now().UTC()
	p.lastError = ""
	done := p.done
	// Capture this process's completion channel so its waiter cannot close a
	// later process's channel.
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		if p.cmd == cmd {
			p.cmd = nil
			if err != nil {
				p.lastError = "endpoint process exited with an error"
			}
		}
		close(done)
		p.mu.Unlock()
		if err != nil {
			_, _ = p.logs.Write([]byte("[agent] Endpoint process exited with an error\n"))
		}
	}()

	return nil
}

// verify polls child liveness and requires successful TCP dials before and after
// StableTime. It does not validate TLS, VPN authentication, or forwarding.
func (p *supervisor) verify(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, p.config.ReadyTimeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var ready time.Time
	for {
		if !p.status().Running {
			return errors.New("endpoint exited during startup")
		}
		if ready.IsZero() || time.Since(ready) >= p.config.StableTime {
			conn, err := net.DialTimeout("tcp", p.listener, 150*time.Millisecond)
			if err == nil {
				conn.Close()
				if !ready.IsZero() {
					return nil
				}
				ready = time.Now()
			} else {
				ready = time.Time{}
			}
		}
		select {
		case <-ctx.Done():
			return errors.New("endpoint did not become ready")
		case <-ticker.C:
		}
	}
}

func (p *supervisor) stop() error {
	p.mu.Lock()
	cmd, done := p.cmd, p.done
	p.mu.Unlock()
	if cmd == nil {
		return nil
	}

	// Wait without mu: the child's wait goroutine needs it before closing done.
	// Stable 1.1.0 handles Ctrl-C for graceful shutdown, not SIGTERM.
	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-done:
		return nil
	case <-time.After(p.config.StopTimeout):
	}

	_ = cmd.Process.Kill()
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("endpoint process did not stop")
	}
}

func (p *supervisor) restart(ctx context.Context) error {
	if err := p.stop(); err != nil {
		return err
	}
	if err := p.start(); err != nil {
		return err
	}
	if err := p.verify(ctx); err != nil {
		_ = p.stop()
		p.mu.Lock()
		p.lastError = err.Error()
		p.mu.Unlock()
		return err
	}
	return nil
}

func (p *supervisor) reload() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil {
		return errors.New("endpoint is not running")
	}
	if p.cmd.Process.Signal(syscall.SIGHUP) != nil {
		return errors.New("cannot signal endpoint")
	}
	return nil
}

// Export stdout contains credentials, so neither stdout nor stderr is logged.
func (p *supervisor) export(ctx context.Context, user string) (protocol.Export, error) {
	out := protocol.Export{Username: user}
	for _, format := range []string{"toml", "deeplink"} {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := exec.CommandContext(
			ctx,
			p.config.Binary,
			filepath.Join(p.config.ConfigDir, "vpn.toml"),
			filepath.Join(p.config.ConfigDir, "hosts.toml"),
			"-c", user,
			"-a", p.config.PublicAddress,
			"--format", format,
		)
		cmd.Dir = p.config.ConfigDir
		var stdout limitedBuffer
		cmd.Stdout = &stdout
		cmd.Stderr = io.Discard
		err := cmd.Run()
		cancel()
		if err != nil || stdout.overflow {
			return out, errors.New("client export failed")
		}

		if format == "toml" {
			out.TOML = stdout.String()
		} else {
			for _, line := range strings.Split(stdout.String(), "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "tt://?") {
					out.DeepLink = line
					break
				}
			}
		}
	}

	if out.TOML == "" || out.DeepLink == "" {
		return protocol.Export{}, errors.New("endpoint returned an invalid client export")
	}
	return out, nil
}

type limitedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len()+n > 1<<20 {
		b.overflow = true
		return n, nil
	}
	_, err := b.Buffer.Write(p)
	return n, err
}
