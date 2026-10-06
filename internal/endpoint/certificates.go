package endpoint

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"

	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

// certificates reports files on disk, which may differ from what a running
// process serves until it reloads TLS settings.
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

// reloadTLS checks main-host certificate/key pairs before requesting SIGHUP.
// The caller must hold a.mu to serialize this request with process transitions.
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
