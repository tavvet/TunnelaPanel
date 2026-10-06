package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

type agentClient struct {
	http *http.Client
}

func newAgentClient(socket string) *agentClient {
	return &agentClient{
		http: &http.Client{
			Timeout: 70 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socket)
				},
			},
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *agentClient) call(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}

		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, body)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return errors.New("Сервис управления TrustTunnel недоступен.")
	}

	defer res.Body.Close()
	r := io.LimitReader(res.Body, 2<<20)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var e protocol.Error
		if json.NewDecoder(r).Decode(&e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}

		return errors.New("Не удалось выполнить операцию с TrustTunnel.")
	}

	if output == nil {
		_, err = io.Copy(io.Discard, r)
		return err
	}

	if err := json.NewDecoder(r).Decode(output); err != nil {
		return errors.New("Некорректный ответ сервиса управления.")
	}

	return nil
}
