package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// contentSecurityPolicy matches what the embedded UI needs: its own script,
// stylesheet, API, covers and media, plus the data: favicon. No inline
// script, nothing third-party, no framing.
const contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self'; object-src 'none'; " +
	"base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// SetAuth turns on HTTP Basic authentication for everything except
// /healthz. Call it before Handler. An empty password leaves the server
// open (the default: auth is then up to the network or a reverse proxy).
func (s *Server) SetAuth(user, password string) {
	if password == "" {
		s.authUser, s.authPass = nil, nil
		return
	}
	u, p := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(password))
	s.authUser, s.authPass = u[:], p[:]
}

// secure wraps the API and UI: security headers on every response,
// cross-site write requests refused, then the optional password.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", contentSecurityPolicy)

		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if !sameOrigin(r) {
			writeError(w, r, http.StatusForbidden, "cross-origin request refused")
			return
		}
		if s.authPass != nil && !s.authorized(r) {
			if _, _, ok := r.BasicAuth(); ok {
				log.Printf("auth: failed login from %s", r.RemoteAddr)
			}
			h.Set("WWW-Authenticate", `Basic realm="easy-music", charset="UTF-8"`)
			writeError(w, r, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authorized compares SHA-256 digests in constant time, so neither the
// length nor the content of the credentials leaks through timing. Both are
// always compared: a right user name doesn't answer faster than a wrong one.
func (s *Server) authorized(r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	u, p := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(pass))
	return subtle.ConstantTimeCompare(u[:], s.authUser)&subtle.ConstantTimeCompare(p[:], s.authPass) == 1
}

// sameOrigin rejects state-changing requests sent by other sites (CSRF).
// Without it any web page you visit could add stations, queue imports or
// delete playlists, even with a password: browsers resend Basic credentials
// and proxy cookies on cross-site requests. The UI only ever calls its own
// origin. Safe methods pass, as do non-browser clients (curl sends neither
// header).
func sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
	default: // same-site, cross-site
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}
