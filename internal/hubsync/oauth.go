package hubsync

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OAuth is the GitHub OAuth gate for the admin surface. When configured
// (client id + secret), it is the *authentication boundary* for /admin:
// unauthenticated requests are redirected to GitHub, and only logins on
// the allowlist are granted an admin session. There is no network-level
// trust assumption — reaching the port is not enough, you must hold a
// valid GitHub session cookie.
//
// Hand-rolled against the GitHub OAuth web flow using stdlib net/http
// — no golang.org/x/oauth2 dependency. The flow:
//
//	/admin/oauth/github/start    → redirect to GitHub authorize
//	/admin/oauth/github/callback → exchange code, check allowlist, grant session
//	/admin/oauth/github/logout   → drop the session
//
// State tokens are single-use and expire after 10 minutes. Sessions are
// in-memory (lost on restart, which just forces re-login) and last 12h.
type OAuth struct {
	clientID     string
	clientSecret string
	baseURL      string          // public base URL of this hub, e.g. https://hub.example
	allow        map[string]bool // lowercased GitHub logins permitted admin access
	secureCookie bool            // set the Secure cookie flag (true when baseURL is https)

	mu       sync.Mutex
	states   map[string]time.Time // CSRF state → expiry
	sessions map[string]session   // session token → login + expiry

	httpClient *http.Client
}

type session struct {
	login string
	exp   time.Time
}

const (
	sessionCookie = "hyphae_admin_session"
	sessionTTL    = 12 * time.Hour
	stateTTL      = 10 * time.Minute
)

// OAuthConfig configures the GitHub gate. ClientID and ClientSecret are
// required (NewOAuth returns nil without them). AdminLogins is the set of
// GitHub usernames allowed admin access; an empty list denies everyone.
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	BaseURL      string
	AdminLogins  []string
}

// NewOAuth builds the gate. Returns nil when ClientID or ClientSecret
// is empty, so callers can do `if oauth != nil` without extra checks.
func NewOAuth(cfg OAuthConfig) *OAuth {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil
	}
	allow := make(map[string]bool, len(cfg.AdminLogins))
	for _, l := range cfg.AdminLogins {
		if l = strings.TrimSpace(strings.ToLower(l)); l != "" {
			allow[l] = true
		}
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	return &OAuth{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		baseURL:      baseURL,
		allow:        allow,
		secureCookie: strings.HasPrefix(baseURL, "https://"),
		states:       make(map[string]time.Time),
		sessions:     make(map[string]session),
		httpClient:   &http.Client{Timeout: 15 * time.Second},
	}
}

// AllowedCount reports how many logins are on the admin allowlist. Zero
// means no one can be granted access (a likely misconfiguration).
func (o *OAuth) AllowedCount() int { return len(o.allow) }

// Mount registers the OAuth routes (these are never gated, or login
// would redirect-loop).
func (o *OAuth) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/admin/oauth/github/start", o.handleStart)
	mux.HandleFunc("/admin/oauth/github/callback", o.handleCallback)
	mux.HandleFunc("/admin/oauth/github/logout", o.handleLogout)
}

// Guard wraps an admin handler so it requires a valid GitHub session.
// Unauthenticated requests are redirected to the OAuth start.
func (o *OAuth) Guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := o.sessionLogin(r); ok {
			next(w, r)
			return
		}
		http.Redirect(w, r, "/admin/oauth/github/start", http.StatusSeeOther)
	}
}

func (o *OAuth) handleStart(w http.ResponseWriter, r *http.Request) {
	state := o.newState()
	q := url.Values{}
	q.Set("client_id", o.clientID)
	q.Set("redirect_uri", o.baseURL+"/admin/oauth/github/callback")
	q.Set("scope", "read:user")
	q.Set("state", state)
	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+q.Encode(), http.StatusSeeOther)
}

func (o *OAuth) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || !o.consumeState(state) {
		pageStatus(w, http.StatusBadRequest, "GitHub login", `<p class="err">invalid or expired OAuth state — start again</p>`)
		return
	}
	token, err := o.exchangeCode(code)
	if err != nil {
		pageStatus(w, http.StatusBadGateway, "GitHub login", `<p class="err">token exchange failed: `+html.EscapeString(err.Error())+`</p>`)
		return
	}
	login, err := o.fetchLogin(token)
	if err != nil {
		pageStatus(w, http.StatusBadGateway, "GitHub login", `<p class="err">fetch user failed: `+html.EscapeString(err.Error())+`</p>`)
		return
	}
	if !o.allow[strings.ToLower(login)] {
		pageStatus(w, http.StatusForbidden, "Access denied", fmt.Sprintf(
			`<p class="err">GitHub user <code>%s</code> is not on the admin allowlist.</p>`+
				`<p class="muted">Ask an operator to add you to <code>HYPHAE_ADMIN_LOGINS</code>.</p>`,
			html.EscapeString(login)))
		return
	}
	o.grantSession(w, login)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (o *OAuth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		o.mu.Lock()
		delete(o.sessions, c.Value)
		o.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/admin",
		HttpOnly: true, Secure: o.secureCookie, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	pageStatus(w, http.StatusOK, "Logged out", `<p class="ok">Signed out.</p><p><a href="/admin">log back in</a></p>`)
}

// ─── sessions ─────────────────────────────────────────────────────────────

func (o *OAuth) grantSession(w http.ResponseWriter, login string) {
	tok := randToken()
	o.mu.Lock()
	o.sessions[tok] = session{login: login, exp: time.Now().Add(sessionTTL)}
	o.gcSessionsLocked()
	o.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/admin",
		HttpOnly: true, Secure: o.secureCookie, SameSite: http.SameSiteLaxMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
}

// sessionLogin returns the authenticated GitHub login for the request, if
// the session cookie is present and unexpired.
func (o *OAuth) sessionLogin(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return "", false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	s, ok := o.sessions[c.Value]
	if !ok || time.Now().After(s.exp) {
		delete(o.sessions, c.Value)
		return "", false
	}
	return s.login, true
}

func (o *OAuth) gcSessionsLocked() {
	now := time.Now()
	for k, s := range o.sessions {
		if now.After(s.exp) {
			delete(o.sessions, k)
		}
	}
}

// ─── GitHub API ─────────────────────────────────────────────────────────────

func (o *OAuth) exchangeCode(code string) (string, error) {
	form := url.Values{}
	form.Set("client_id", o.clientID)
	form.Set("client_secret", o.clientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", o.baseURL+"/admin/oauth/github/callback")

	req, _ := http.NewRequest(http.MethodPost,
		"https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	if out.AccessToken == "" {
		if out.Error != "" {
			return "", fmt.Errorf("github: %s", out.Error)
		}
		return "", fmt.Errorf("github: empty access token")
	}
	return out.AccessToken, nil
}

func (o *OAuth) fetchLogin(token string) (string, error) {
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var out struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("decode user response: %w", err)
	}
	if out.Login == "" {
		return "", fmt.Errorf("github: empty login")
	}
	return out.Login, nil
}

// ─── state management ───────────────────────────────────────────────────────

func (o *OAuth) newState() string {
	s := randToken()
	o.mu.Lock()
	o.states[s] = time.Now().Add(stateTTL)
	o.gcStatesLocked()
	o.mu.Unlock()
	return s
}

func (o *OAuth) consumeState(s string) bool {
	if s == "" {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	exp, ok := o.states[s]
	if !ok || time.Now().After(exp) {
		delete(o.states, s)
		return false
	}
	delete(o.states, s)
	return true
}

func (o *OAuth) gcStatesLocked() {
	now := time.Now()
	for k, exp := range o.states {
		if now.After(exp) {
			delete(o.states, k)
		}
	}
}

func randToken() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
