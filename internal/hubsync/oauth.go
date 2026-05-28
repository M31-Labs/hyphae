package hubsync

import (
	"crypto/rand"
	"database/sql"
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
	org          string          // optional GitHub org; members get admin access
	secureCookie bool            // set the Secure cookie flag (true when baseURL is https)

	sessions sessionStore // persists admin sessions across restarts when DB-backed

	mu     sync.Mutex
	states map[string]time.Time // CSRF state → expiry (in-memory; fine to lose on restart)

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
// GitHub usernames allowed admin access; AdminOrg, when set, additionally
// grants access to any member of that GitHub org. With neither, no one is
// granted access. Store, when non-nil, persists sessions across restarts
// (otherwise sessions are in-memory).
type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	BaseURL      string
	AdminLogins  []string
	AdminOrg     string
	Store        *sql.DB
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
	var store sessionStore
	if cfg.Store != nil {
		store = dbSessionStore{conn: cfg.Store}
	} else {
		store = newMemSessionStore()
	}
	baseURL := strings.TrimRight(cfg.BaseURL, "/")
	return &OAuth{
		clientID:     cfg.ClientID,
		clientSecret: cfg.ClientSecret,
		baseURL:      baseURL,
		allow:        allow,
		org:          strings.TrimSpace(cfg.AdminOrg),
		secureCookie: strings.HasPrefix(baseURL, "https://"),
		sessions:     store,
		states:       make(map[string]time.Time),
		httpClient:   &http.Client{Timeout: 15 * time.Second},
	}
}

// AllowedCount reports how many logins are on the admin allowlist. Zero
// means no one can be granted access (a likely misconfiguration).
func (o *OAuth) AllowedCount() int { return len(o.allow) }

// AdmitsAnyone reports whether the gate can grant access to at least one
// principal — i.e. the allowlist is non-empty or an org is configured.
// When false, the gate is a locked door with no key (a misconfiguration).
func (o *OAuth) AdmitsAnyone() bool { return len(o.allow) > 0 || o.org != "" }

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
	// read:org is only requested when org-based access is configured, so
	// allowlist-only deployments keep the minimal read:user consent screen.
	scope := "read:user"
	if o.org != "" {
		scope = "read:user read:org"
	}
	q := url.Values{}
	q.Set("client_id", o.clientID)
	q.Set("redirect_uri", o.baseURL+"/admin/oauth/github/callback")
	q.Set("scope", scope)
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
	if !o.authorized(token, login) {
		pageStatus(w, http.StatusForbidden, "Access denied", fmt.Sprintf(
			`<p class="err">GitHub user <code>%s</code> is not permitted admin access.</p>`+
				`<p class="muted">Ask an operator to add you to <code>HYPHAE_ADMIN_LOGINS</code>`+
				`%s.</p>`,
			html.EscapeString(login), o.orgHint()))
		return
	}
	if !o.grantSession(w, login) {
		return // grantSession already wrote an error response
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// authorized reports whether a successfully-authenticated GitHub login may
// access the admin surface: it is on the allowlist, or (when an org is
// configured) it is an active member of that org.
func (o *OAuth) authorized(token, login string) bool {
	if o.allow[strings.ToLower(login)] {
		return true
	}
	if o.org != "" {
		return o.isOrgMember(token)
	}
	return false
}

func (o *OAuth) orgHint() string {
	if o.org == "" {
		return ""
	}
	return ` or to the <code>` + html.EscapeString(o.org) + `</code> GitHub org`
}

func (o *OAuth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		o.sessions.drop(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/admin",
		HttpOnly: true, Secure: o.secureCookie, SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	pageStatus(w, http.StatusOK, "Logged out", `<p class="ok">Signed out.</p><p><a href="/admin">log back in</a></p>`)
}

// ─── sessions ─────────────────────────────────────────────────────────────

// grantSession mints a session, persists it, and sets the cookie. Returns
// false (after writing an error response) if the session could not be
// persisted, so the caller must not also write to w.
func (o *OAuth) grantSession(w http.ResponseWriter, login string) bool {
	tok := randToken()
	if err := o.sessions.create(tok, login, time.Now().Add(sessionTTL)); err != nil {
		http.Error(w, "could not establish session", http.StatusInternalServerError)
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/admin",
		HttpOnly: true, Secure: o.secureCookie, SameSite: http.SameSiteLaxMode,
		MaxAge: int(sessionTTL.Seconds()),
	})
	return true
}

// sessionLogin returns the authenticated GitHub login for the request, if
// the session cookie is present and unexpired.
func (o *OAuth) sessionLogin(r *http.Request) (string, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return "", false
	}
	return o.sessions.lookup(c.Value)
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

// isOrgMember reports whether the authenticated user (identified by their
// own access token) is an active member of o.org. Uses the membership
// endpoint, which the user-to-server token can read for itself with the
// read:org scope. Any non-active state or API error is treated as "no".
func (o *OAuth) isOrgMember(token string) bool {
	req, _ := http.NewRequest(http.MethodGet,
		"https://api.github.com/user/memberships/orgs/"+url.PathEscape(o.org), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := o.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	body, _ := io.ReadAll(resp.Body)
	var out struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return false
	}
	return out.State == "active"
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
