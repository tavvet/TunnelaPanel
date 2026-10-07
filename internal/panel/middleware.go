package panel

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// security applies headers and validates the host and POST envelope for every route.
func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(
			"Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
		)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		// no-referrer makes browsers send Origin: null on form POSTs.
		// same-origin preserves form origins without sending referrers off-site.
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")

		// The allowlist matches hostnames without ports; loopback hosts are always allowed.
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}

		allowed := host == "localhost" || host == "127.0.0.1" || host == "::1"
		for _, h := range s.config.AllowedHosts {
			if strings.EqualFold(host, h) {
				allowed = true
			}
		}

		if !allowed {
			http.Error(w, translate(requestLanguage(r), "error.unknown_host"), http.StatusForbidden)
			return
		}

		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
			// An explicit Origin must match the full Host, including its port.
			// Missing Origin is allowed; login and protected POSTs still require CSRF tokens.
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
					http.Error(w, translate(requestLanguage(r), "error.origin"), http.StatusForbidden)
					return
				}
			}

			if err := r.ParseForm(); err != nil {
				http.Error(w, translate(requestLanguage(r), "error.request"), http.StatusBadRequest)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func sameToken(a, b string) bool {
	return len(a) >= 32 && len(b) >= 32 && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// protect requires a live in-memory session and its separate CSRF token for POSTs.
// security must run first so the request body is bounded and PostForm is populated.
func (s *Server) protect(fn func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("tp_session")
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		se, ok := s.auth.lookup(cookie.Value, time.Now())
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		if r.Method == http.MethodPost && !sameToken(se.CSRF, r.PostForm.Get("csrf")) {
			http.Error(w, translate(requestLanguage(r), "error.csrf"), http.StatusForbidden)
			return
		}

		fn(w, r, se)
	}
}
