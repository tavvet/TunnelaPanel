package panel

import (
	"net/http"
	"time"
)

func (s *Server) cookie(w http.ResponseWriter, name, value, path string, maxAge int) {
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     name,
			Value:    value,
			Path:     path,
			MaxAge:   maxAge,
			HttpOnly: true,
			Secure:   s.config.SecureCookie,
			SameSite: http.SameSiteStrictMode,
		},
	)
}

// loginPage issues a short-lived cookie and matching form token before a session exists.
func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	token := randomToken()
	s.cookie(w, "tp_login", token, "/login", 900)
	s.render(
		w,
		View{
			Page:    "login",
			Title:   "Вход",
			CSRF:    token,
			Version: s.config.Version,
			Flash:   notice(r.URL.Query().Get("notice")),
		},
	)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("tp_login")
	if err != nil || !sameToken(cookie.Value, r.PostForm.Get("csrf")) {
		http.Error(w, "Обновите страницу входа.", http.StatusForbidden)
		return
	}

	ip := s.remoteIP(r)
	if !s.auth.allowAttempt(ip, time.Now()) {
		http.Error(
			w,
			"Слишком много попыток. Повторите через 15 минут.",
			http.StatusTooManyRequests,
		)
		return
	}

	password := r.PostForm.Get("password")
	var id string
	var authenticated bool
	if len(password) <= 128 {
		id, authenticated = s.auth.authenticate(r.PostForm.Get("username"), password, time.Now())
	}

	if !authenticated {
		s.audit("login_failed", ip, false)
		w.WriteHeader(http.StatusUnauthorized)
		s.render(
			w,
			View{
				Page:    "login",
				Title:   "Вход",
				CSRF:    cookie.Value,
				Error:   "Неверное имя пользователя или пароль.",
				Version: s.config.Version,
			},
		)
		return
	}

	s.auth.clearAttempts(ip)
	s.cookie(w, "tp_session", id, "/", 8*3600)
	s.cookie(w, "tp_login", "", "/login", -1)
	s.audit("login", ip, true)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, se session) {
	if c, err := r.Cookie("tp_session"); err == nil {
		s.auth.removeSession(c.Value)
	}

	s.cookie(w, "tp_session", "", "/", -1)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) password(w http.ResponseWriter, r *http.Request, se session) {
	err := s.auth.changePassword(r.PostForm.Get("current_password"), r.PostForm.Get("new_password"))
	s.audit("password", s.remoteIP(r), err == nil)
	if err != nil {
		s.page(w, r, se, "settings", err.Error())
		return
	}

	s.cookie(w, "tp_session", "", "/", -1)
	http.Redirect(w, r, "/login?notice=password", http.StatusSeeOther)
}
