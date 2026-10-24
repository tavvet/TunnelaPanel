package panel

import (
	"net/http"
	"time"
)

// cookieName adds the __Host- prefix over HTTPS. Browsers then accept the
// cookie only when it is Secure, has Path=/ and no Domain, so a sibling
// subdomain or a plain-HTTP response cannot set or replace it.
func (s *Server) cookieName(name string) string {
	if s.config.SecureCookie {
		return "__Host-" + name
	}

	return name
}

// cookie always uses Path=/, which the __Host- prefix requires.
func (s *Server) cookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(
		w,
		&http.Cookie{
			Name:     s.cookieName(name),
			Value:    value,
			Path:     "/",
			MaxAge:   maxAge,
			HttpOnly: true,
			Secure:   s.config.SecureCookie,
			SameSite: http.SameSiteStrictMode,
		},
	)
}

// expireLegacyLoginCookie removes the Path=/login cookie issued by earlier
// releases. In plain-HTTP mode it has the same name as the current cookie.
func (s *Server) expireLegacyLoginCookie(w http.ResponseWriter) {
	if s.config.SecureCookie {
		return
	}

	http.SetCookie(w, &http.Cookie{Name: "tp_login", Path: "/login", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

// loginToken returns the login cookie whose value matches the form token.
// Browsers send every same-named cookie, and one left by an earlier release
// may come first, so each candidate is checked rather than only the first.
func (s *Server) loginToken(r *http.Request) (string, bool) {
	name := s.cookieName("tp_login")
	form := r.PostForm.Get("csrf")
	for _, c := range r.Cookies() {
		if c.Name == name && sameToken(c.Value, form) {
			return c.Value, true
		}
	}

	return "", false
}

// loginPage issues a short-lived cookie and matching form token before a session exists.
func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	token := randomToken()
	s.expireLegacyLoginCookie(w)
	s.cookie(w, "tp_login", token, 900)
	s.render(
		w,
		View{
			Language: requestLanguage(r),
			Page:     "login",
			Title:    "title.login",
			CSRF:     token,
			Version:  s.config.Version,
			Flash:    notice(requestLanguage(r), r.URL.Query().Get("notice")),
		},
	)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	token, ok := s.loginToken(r)
	if !ok {
		http.Error(w, translate(requestLanguage(r), "error.login_refresh"), http.StatusForbidden)
		return
	}

	ip := s.remoteIP(r)
	limitKey := rateLimitKey(ip)
	// An exhausted key is refused at once rather than waiting for a slot.
	if s.auth.exhausted(limitKey, time.Now()) {
		s.refuseRateLimited(w, r, limitKey, ip)
		return
	}

	release, ok := s.auth.acquireLoginSlot(r.Context(), limitKey, loginHashWait)
	if !ok {
		s.auditRefusal(refusalBusy, limitKey, ip)
		http.Error(w, translate(requestLanguage(r), "error.login_busy"), http.StatusServiceUnavailable)
		return
	}

	// Counting happens while the slot is held; the key may have been
	// exhausted meanwhile, or the attempt table may be full.
	if !s.auth.allowAttempt(limitKey, time.Now()) {
		release()
		s.refuseRateLimited(w, r, limitKey, ip)
		return
	}

	// The slot covers only the password derivation, not auditing or rendering.
	var id string
	var authenticated bool
	if password := r.PostForm.Get("password"); len(password) <= 128 {
		id, authenticated = s.auth.authenticate(r.PostForm.Get("username"), password, time.Now())
	} else {
		// An oversized password can never match, but it still counts as an
		// attempt and costs a derivation, so it cannot bypass either limit.
		s.auth.rejectSlowly()
	}

	release()
	if authenticated {
		s.auth.loginSucceeded(limitKey, time.Now())
	}

	if !authenticated {
		s.auditFailedLogin(ip)
		w.WriteHeader(http.StatusUnauthorized)
		s.render(
			w,
			View{
				Language: requestLanguage(r),
				Page:     "login",
				Title:    "title.login",
				CSRF:     token,
				Error:    translate(requestLanguage(r), "error.login_credentials"),
				Version:  s.config.Version,
			},
		)
		return
	}

	s.cookie(w, "tp_session", id, 8*3600)
	s.cookie(w, "tp_login", "", -1)
	s.expireLegacyLoginCookie(w)
	s.audit("login", ip, true)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// auditRefusal records refused logins without letting a flood fill the log.
func (s *Server) auditRefusal(kind refusal, key, ip string) {
	if s.auth.reportRefusal(kind, key, time.Now()) {
		s.audit(string(kind), ip, false)
	}
}

func (s *Server) refuseRateLimited(w http.ResponseWriter, r *http.Request, key, ip string) {
	s.auditRefusal(refusalRateLimited, key, ip)
	http.Error(
		w,
		translate(requestLanguage(r), "error.login_rate"),
		http.StatusTooManyRequests,
	)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, se session) {
	if c, err := r.Cookie(s.cookieName("tp_session")); err == nil {
		s.auth.removeSession(c.Value)
	}

	s.cookie(w, "tp_session", "", -1)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) password(w http.ResponseWriter, r *http.Request, se session) {
	err := s.auth.changePassword(r.PostForm.Get("current_password"), r.PostForm.Get("new_password"))
	s.audit("password", s.remoteIP(r), err == nil)
	if err != nil {
		s.page(w, r, se, "settings", errorText(requestLanguage(r), err))
		return
	}

	s.cookie(w, "tp_session", "", -1)
	http.Redirect(w, r, "/login?notice=password", http.StatusSeeOther)
}
