package endpoint

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Config defines the executable, persistent paths, and private control endpoints.
// New requires absolute filesystem paths and a public address with an explicit port.
type Config struct {
	Binary    string
	ConfigDir string
	StateDir  string
	Socket    string

	// PublicAddress is exported to clients and can differ from the local listener.
	PublicAddress string
	// MetricsURL must point to an HTTP endpoint on a loopback IP address.
	MetricsURL string

	ReadyTimeout time.Duration
	StableTime   time.Duration
	StopTimeout  time.Duration
}

// ConfigFromEnv reads TT_* settings and supplies defaults for optional values.
func ConfigFromEnv() Config {
	env := func(k, d string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return d
	}

	return Config{
		Binary:        env("TT_BINARY", "/usr/local/bin/trusttunnel_endpoint"),
		ConfigDir:     env("TT_CONFIG_DIR", "/data/config"),
		StateDir:      env("TT_STATE_DIR", "/data/state"),
		Socket:        env("TT_SOCKET", "/run/tunnelapanel/control.sock"),
		PublicAddress: os.Getenv("TT_PUBLIC_ADDRESS"),
		MetricsURL:    env("TT_METRICS_URL", "http://127.0.0.1:1987"),
		ReadyTimeout:  10 * time.Second,
		StableTime:    2 * time.Second,
		StopTimeout:   8 * time.Second,
	}
}

func (c *Config) normalize() error {
	if c.ReadyTimeout == 0 {
		c.ReadyTimeout = 10 * time.Second
	}
	if c.StableTime == 0 {
		c.StableTime = 2 * time.Second
	}
	if c.StopTimeout == 0 {
		c.StopTimeout = 8 * time.Second
	}

	host, port, err := net.SplitHostPort(c.PublicAddress)
	n, perr := strconv.Atoi(port)
	if err != nil || host == "" || perr != nil || n < 1 || n > 65535 {
		return errors.New("TT_PUBLIC_ADDRESS must include a host and port")
	}
	if !filepath.IsAbs(c.Binary) || !filepath.IsAbs(c.ConfigDir) ||
		!filepath.IsAbs(c.StateDir) || !filepath.IsAbs(c.Socket) {
		return errors.New("agent paths must be absolute")
	}
	return nil
}

func readTOML(path string) (map[string]any, []byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, errors.New("cannot read configuration file")
	}
	var doc map[string]any
	if err := toml.Unmarshal(b, &doc); err != nil {
		return nil, nil, errors.New("invalid TOML configuration")
	}
	return doc, b, nil
}

func readRuntime(c Config) (credentials, listener string, err error) {
	doc, _, err := readTOML(filepath.Join(c.ConfigDir, "vpn.toml"))
	if err != nil {
		return "", "", err
	}
	name, ok := doc["credentials_file"].(string)
	if !ok || name == "" {
		return "", "", errors.New("credentials_file must be configured")
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(c.ConfigDir, name)
	}
	name = filepath.Clean(name)
	rel, err := filepath.Rel(c.ConfigDir, name)
	if err != nil || rel == ".." || len(rel) > 3 && rel[:3] == "../" {
		return "", "", errors.New("credentials_file must be inside TT_CONFIG_DIR")
	}

	addr, ok := doc["listen_address"].(string)
	if !ok {
		return "", "", errors.New("listen_address must be configured")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", "", errors.New("invalid endpoint listen_address")
	}
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" || host == "" {
		host = "::1"
	}
	return name, net.JoinHostPort(host, port), nil
}
