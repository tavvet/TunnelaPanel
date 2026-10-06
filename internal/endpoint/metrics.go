package endpoint

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

// clients reads private metrics from the endpoint's loopback HTTP listener.
// Redirects are disabled so a metrics response cannot change that destination.
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
	client := http.Client{
		Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
		},
	}
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
