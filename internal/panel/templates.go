package panel

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"time"

	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

//go:embed web/*.html web/static/*
var assets embed.FS

// View contains the data shared by the embedded HTML templates.
type View struct {
	Page         string
	Title        string
	CSRF         string
	Error        string
	Flash        string
	Username     string
	AgentError   string
	Version      string
	Status       *protocol.Status
	Users        []protocol.User
	Clients      []protocol.ClientMetric
	Logs         []string
	Certificates []protocol.Certificate
	Export       *protocol.Export
}

func newTemplates() (*template.Template, error) {
	return template.New("panel").Funcs(template.FuncMap{
		"bytes": formatBytes,
		"datetime": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}

			return t.UTC().Format("02.01.2006 15:04 UTC")
		},
		"uptime": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}

			d := time.Since(t)
			if d < 0 {
				return "—"
			}

			return d.Round(time.Second).String()
		},
		"remaining": func(t time.Time) string {
			if t.IsZero() {
				return "—"
			}

			days := int(time.Until(t).Hours() / 24)
			if days < 0 {
				return "Истёк"
			}

			return fmt.Sprintf("%d дн.", days)
		},
		"urlencode": url.QueryEscape,
	}).ParseFS(
		assets,
		"web/*.html",
	)
}

func (s *Server) render(w http.ResponseWriter, v View) {
	// Buffer template execution so a template error does not leave partial HTML.
	var b bytes.Buffer
	if err := s.templates.ExecuteTemplate(&b, v.Page, v); err != nil {
		http.Error(w, "Не удалось отобразить страницу.", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b.Bytes())
}

func formatBytes(n uint64) string {
	units := []string{
		"Б",
		"КиБ",
		"МиБ",
		"ГиБ",
		"ТиБ",
	}

	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}

	if i == 0 {
		return fmt.Sprintf("%d %s", n, units[i])
	}

	return fmt.Sprintf("%.1f %s", v, units[i])
}

func notice(key string) string {
	return map[string]string{
		"users/add":    "Пользователь добавлен в черновик.",
		"users/edit":   "Изменения пользователя сохранены в черновике.",
		"users/delete": "Удаление сохранено в черновике.",
		"apply":        "Изменения применены. TrustTunnel перезапущен.",
		"discard":      "Черновик отменён.",
		"restart":      "TrustTunnel перезапущен.",
		"tls/reload":   "Сертификаты перечитаны.",
		"password":     "Пароль изменён. Войдите с новым паролем.",
	}[key]
}
