package panel

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/tavvet/TunnelaPanel/internal/protocol"
)

// validNewPassword applies the UI's 128-byte limit for new VPN user passwords.
// The endpoint has its own validation rules and accepts a wider length range.
func validNewPassword(password string) bool {
	if len(password) < 12 || len(password) > 128 || strings.TrimSpace(password) != password {
		return false
	}

	for _, c := range password {
		if c < 32 || c > 126 || c == '"' || c == '\\' {
			return false
		}
	}

	return true
}

func (s *Server) action(w http.ResponseWriter, r *http.Request, se session) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	target := "/users"
	method := "POST"
	apiPath := "/v1/" + path
	var input any
	if strings.HasPrefix(path, "users/") {
		apiPath = "/v1/users"
		change := protocol.UserChange{
			Username: strings.TrimSpace(r.PostForm.Get("username")),
			Password: r.PostForm.Get("password"),
		}

		if path == "users/add" || (path == "users/edit" && change.Password != "") {
			if !validNewPassword(change.Password) {
				s.page(
					w,
					r,
					se,
					"users",
					translate(requestLanguage(r), "error.vpn_password"),
				)
				return
			}
		}

		if path == "users/edit" {
			method = "PATCH"
			if e := r.PostForm.Get("enabled"); e != "" {
				if e != "true" && e != "false" {
					http.Error(w, translate(requestLanguage(r), "error.user_state"), 400)
					return
				}

				enabled := e == "true"
				change.Enabled = &enabled
			}
		} else if path == "users/delete" {
			method = "DELETE"
			change.Password = ""
		}

		input = change
	} else if path == "restart" {
		target = "/"
	} else if path == "tls/reload" {
		target = "/settings"
	}

	err := s.agent.call(r.Context(), method, apiPath, input, nil)
	s.audit(path, s.remoteIP(r), err == nil)
	if err != nil {
		page := strings.TrimPrefix(target, "/")
		if page == "" {
			page = "dashboard"
		}

		s.page(w, r, se, page, errorText(requestLanguage(r), err))
		return
	}

	http.Redirect(w, r, target+"?notice="+url.QueryEscape(path), http.StatusSeeOther)
}
