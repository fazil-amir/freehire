package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// Hardcoded credentials for this private internal tool. Edit these two
// constants directly and rebuild to change them — see README.md.
const (
	consoleUsername = "admin"
	consolePassword = "change-me"
)

const sessionCookieName = "board_console_session"
const sessionTTL = 24 * time.Hour

// SessionStore is a simple in-memory token store. A restart logs everyone
// out, which is acceptable for a private internal tool.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time // token -> expiry
}

func NewSessionStore() *SessionStore {
	return &SessionStore{sessions: map[string]time.Time{}}
}

func (s *SessionStore) Create() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(sessionTTL)
	s.mu.Unlock()
	return token, nil
}

func (s *SessionStore) Valid(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, token)
		return false
	}
	return true
}

func (s *SessionStore) Revoke(token string) {
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

// requireAuth wraps a handler, redirecting to /login when no valid session
// cookie is present.
func requireAuth(store *SessionStore, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookieName)
		if err != nil || !store.Valid(c.Value) {
			// A fetch would follow the redirect and read the login page as
			// a 200 — a success. It gets a 401, and app.js sends it to login.
			if isFetch(r) {
				http.Error(w, "session expired", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func handleLoginPage(tmpl *Templates) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		errMsg := ""
		if r.URL.Query().Get("error") == "1" {
			errMsg = "Invalid username or password."
		}
		tmpl.Render(w, "login", map[string]any{"Error": errMsg})
	}
}

func handleLoginSubmit(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		user := r.FormValue("username")
		pass := r.FormValue("password")
		if !constantTimeEqual(user, consoleUsername) || !constantTimeEqual(pass, consolePassword) {
			http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
			return
		}
		token, err := store.Create()
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   int(sessionTTL.Seconds()),
		})
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

func handleLogout(store *SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookieName); err == nil {
			store.Revoke(c.Value)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
		})
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}
