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
	Language     string
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
		"t":     translate,
		"bytes": formatBytes,
		"datetime": func(language string, t time.Time) string {
			if t.IsZero() {
				return "—"
			}

			return t.UTC().Format(translate(language, "format.datetime"))
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
		"remaining": func(language string, t time.Time) string {
			if t.IsZero() {
				return "—"
			}

			days := int(time.Until(t).Hours() / 24)
			if days < 0 {
				return translate(language, "format.expired")
			}
			if days == 1 {
				return translate(language, "format.days_one", days)
			}
			return translate(language, "format.days_many", days)
		},
		"urlencode": url.QueryEscape,
	}).ParseFS(
		assets,
		"web/*.html",
	)
}

func (s *Server) render(w http.ResponseWriter, v View) {
	if !validLanguage(v.Language) {
		v.Language = defaultLanguage
	}
	v.Title = translate(v.Language, v.Title)
	// Buffer template execution so a template error does not leave partial HTML.
	var b bytes.Buffer
	if err := s.templates.ExecuteTemplate(&b, v.Page, v); err != nil {
		http.Error(w, translate(v.Language, "error.render"), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b.Bytes())
}

func formatBytes(language string, n uint64) string {
	units := []string{
		translate(language, "format.bytes"),
		translate(language, "format.kib"),
		translate(language, "format.mib"),
		translate(language, "format.gib"),
		translate(language, "format.tib"),
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

func notice(language, action string) string {
	// Only known actions can produce a banner; arbitrary query text is ignored.
	key := map[string]string{
		"users/add":    "notice.user_added",
		"users/edit":   "notice.user_edited",
		"users/delete": "notice.user_deleted",
		"apply":        "notice.applied",
		"discard":      "notice.discarded",
		"restart":      "notice.restarted",
		"tls/reload":   "notice.tls_reload",
		"password":     "notice.password_changed",
		"preferences":  "notice.preferences_saved",
	}[action]
	if key == "" {
		return ""
	}
	return translate(language, key)
}
