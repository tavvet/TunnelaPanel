package panel

import (
	"errors"
	"net/http"
	"net/url"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

func (s *Server) page(w http.ResponseWriter, r *http.Request, se session, page, errMessage string) {
	language := requestLanguage(r)
	v := View{
		Language: language,
		Page:     page,
		Title: map[string]string{
			"dashboard": "title.dashboard",
			"users":     "title.users",
			"logs":      "title.logs",
			"settings":  "title.settings",
		}[page],
		CSRF:     se.CSRF,
		Username: "admin",
		Version:  s.config.Version,
		Error:    errMessage,
		Flash:    notice(language, r.URL.Query().Get("notice")),
	}

	var st protocol.Status
	if err := s.agent.call(r.Context(), "GET", "/v1/status", nil, &st); err != nil {
		v.AgentError = errorText(requestLanguage(r), err)
	} else {
		v.Status = &st
	}

	var err error
	switch page {
	case "dashboard":
		var users protocol.Users
		clientsErr := s.agent.call(r.Context(), "GET", "/v1/clients", nil, &v.Clients)
		usersErr := s.agent.call(r.Context(), "GET", "/v1/users", nil, &users)
		certificatesErr := s.agent.call(r.Context(), "GET", "/v1/certificates", nil, &v.Certificates)
		v.Users = users.Users
		err = errors.Join(clientsErr, usersErr, certificatesErr)
	case "users":
		var us protocol.Users
		err = s.agent.call(r.Context(), "GET", "/v1/users", nil, &us)
		v.Users = us.Users
	case "logs":
		var logs protocol.LogResponse
		err = s.agent.call(r.Context(), "GET", "/v1/logs", nil, &logs)
		v.Logs = logs.Lines
	case "settings":
		err = s.agent.call(r.Context(), "GET", "/v1/certificates", nil, &v.Certificates)
	}

	if err != nil && v.AgentError == "" {
		v.AgentError = errorText(requestLanguage(r), err)
	}

	s.render(w, v)
}

func (s *Server) export(w http.ResponseWriter, r *http.Request, se session) {
	var ex protocol.Export
	err := s.agent.call(r.Context(), "GET", "/v1/export?username="+url.QueryEscape(r.URL.Query().Get("username")), nil, &ex)
	if err != nil {
		s.page(w, r, se, "users", errorText(requestLanguage(r), err))
		return
	}

	var st protocol.Status
	_ = s.agent.call(r.Context(), "GET", "/v1/status", nil, &st)
	s.render(
		w,
		View{
			Language: requestLanguage(r),
			Page:     "export",
			Title:    "title.export",
			CSRF:     se.CSRF,
			Username: "admin",
			Version:  s.config.Version,
			Status:   &st,
			Export:   &ex,
		},
	)
}

func (s *Server) qr(w http.ResponseWriter, r *http.Request, se session) {
	var ex protocol.Export
	if err := s.agent.call(r.Context(), "GET", "/v1/export?username="+url.QueryEscape(r.URL.Query().Get("username")), nil, &ex); err != nil {
		http.Error(w, translate(requestLanguage(r), "error.export"), http.StatusBadRequest)
		return
	}

	png, err := qrcode.Encode(ex.DeepLink, qrcode.Medium, 384)
	if err != nil {
		http.Error(w, translate(requestLanguage(r), "error.qr"), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(png)
}
