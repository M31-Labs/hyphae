package hubsync

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/hyphae/internal/capability"
	"m31labs.dev/hyphae/internal/crdtshadow"
	"m31labs.dev/hyphae/internal/db"
)

// newAdminTest builds an Admin backed by a temp index DB and an empty
// server (no registered spaces), mounted on a fresh mux. Returns the
// mux and the auth DB so tests can assert side effects.
func newAdminTest(t *testing.T) (*http.ServeMux, *Admin) {
	t.Helper()
	root := t.TempDir()
	conn, err := db.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	srv := NewServer(root, crdtshadow.NewRegistry(), conn, false)
	a := NewAdmin(root, conn, srv, nil)
	mux := http.NewServeMux()
	a.Mount(mux)
	return mux, a
}

func TestAdminPagesRender(t *testing.T) {
	mux, _ := newAdminTest(t)
	for _, path := range []string{"/admin", "/admin/keys", "/admin/peers", "/admin/audit"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s content-type = %q, want text/html", path, ct)
		}
	}
}

func TestAdminIssueAndRevokeKey(t *testing.T) {
	mux, a := newAdminTest(t)

	// Issue a key through the admin form.
	form := url.Values{
		"subject":     {"agent://test/runner"},
		"space":       {"hypha://m31labs/hyphae"},
		"permissions": {"memory:recall"},
		"expires":     {"90d"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/keys/issue", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("issue = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	caps, err := capability.List(a.authConn, "", true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(caps) != 1 {
		t.Fatalf("after issue, caps len = %d, want 1", len(caps))
	}
	issued := caps[0]
	if issued.Subject != "agent://test/runner" {
		t.Errorf("subject = %q, want agent://test/runner", issued.Subject)
	}
	// 90d shorthand must have parsed (not the 24h default).
	if d := issued.ExpiresAt.Sub(issued.IssuedAt); d < 89*24*60*60*1e9 {
		t.Errorf("expiry span = %s, want ~90d (parseFlexDuration applied)", d)
	}

	// Revoke it through the admin form (redirects on success).
	revForm := url.Values{"id": {issued.ID}}
	revReq := httptest.NewRequest(http.MethodPost, "/admin/keys/revoke", strings.NewReader(revForm.Encode()))
	revReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	revRec := httptest.NewRecorder()
	mux.ServeHTTP(revRec, revReq)
	if revRec.Code != http.StatusSeeOther {
		t.Fatalf("revoke = %d, want 303", revRec.Code)
	}
	if _, err := capability.Verify(a.authConn, issued.ID); err == nil {
		t.Error("expected Verify to fail after admin revoke")
	}
}

func TestAdminIssueRejectsMissingFields(t *testing.T) {
	mux, a := newAdminTest(t)
	form := url.Values{"subject": {""}, "space": {""}}
	req := httptest.NewRequest(http.MethodPost, "/admin/keys/issue", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("issue (bad) = %d, want 200 with error page", rec.Code)
	}
	caps, err := capability.List(a.authConn, "", true)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(caps) != 0 {
		t.Fatalf("no key should be issued on missing fields; got %d", len(caps))
	}
}

func TestNewOAuthNilWithoutCreds(t *testing.T) {
	if o := NewOAuth(OAuthConfig{}); o != nil {
		t.Error("NewOAuth with empty creds should return nil")
	}
	if o := NewOAuth(OAuthConfig{ClientID: "x", ClientSecret: "y"}); o == nil {
		t.Error("NewOAuth with creds should return non-nil")
	}
}

func TestOAuthAllowlist(t *testing.T) {
	o := NewOAuth(OAuthConfig{ClientID: "x", ClientSecret: "y", AdminLogins: []string{"Alice", " bob "}})
	if o.AllowedCount() != 2 {
		t.Fatalf("AllowedCount = %d, want 2", o.AllowedCount())
	}
	// Logins are matched case-insensitively and trimmed.
	if !o.allow["alice"] || !o.allow["bob"] {
		t.Errorf("allowlist = %v, want alice+bob normalized", o.allow)
	}
	if o.allow["carol"] {
		t.Error("carol should not be allowed")
	}
}

// gatedAdmin builds an admin surface fronted by a configured OAuth gate.
func gatedAdmin(t *testing.T, logins ...string) (*http.ServeMux, *OAuth) {
	t.Helper()
	root := t.TempDir()
	conn, err := db.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	o := NewOAuth(OAuthConfig{ClientID: "x", ClientSecret: "y", BaseURL: "https://hub.example", AdminLogins: logins, Store: conn})
	srv := NewServer(root, crdtshadow.NewRegistry(), conn, false)
	mux := http.NewServeMux()
	NewAdmin(root, conn, srv, o).Mount(mux)
	return mux, o
}

func TestAdminGateRedirectsWithoutSession(t *testing.T) {
	mux, _ := gatedAdmin(t, "alice")
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated /admin = %d, want 303 redirect", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/oauth/github/start" {
		t.Errorf("redirect Location = %q, want /admin/oauth/github/start", loc)
	}
}

func TestAdminGateAllowsWithSession(t *testing.T) {
	mux, o := gatedAdmin(t, "alice")
	// Mint a session the way a successful callback would.
	sessionRec := httptest.NewRecorder()
	o.grantSession(sessionRec, "alice")
	cookies := sessionRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("grantSession set no cookie")
	}

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated /admin = %d, want 200", rec.Code)
	}
}

func TestOAuthStartRouteIsNotGated(t *testing.T) {
	mux, _ := gatedAdmin(t, "alice")
	req := httptest.NewRequest(http.MethodGet, "/admin/oauth/github/start", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("oauth start = %d, want 303 to github", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "github.com/login/oauth/authorize") {
		t.Errorf("oauth start Location = %q, want github authorize URL", loc)
	}
}

func TestOAuthLogoutClearsSession(t *testing.T) {
	mux, o := gatedAdmin(t, "alice")
	rec0 := httptest.NewRecorder()
	o.grantSession(rec0, "alice")
	cookie := rec0.Result().Cookies()[0]

	// Session is valid before logout.
	if _, ok := o.sessionLogin(reqWithCookie("/admin", cookie)); !ok {
		t.Fatal("expected valid session before logout")
	}
	logoutReq := reqWithCookie("/admin/oauth/github/logout", cookie)
	logoutRec := httptest.NewRecorder()
	mux.ServeHTTP(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusOK {
		t.Fatalf("logout = %d, want 200", logoutRec.Code)
	}
	if _, ok := o.sessionLogin(reqWithCookie("/admin", cookie)); ok {
		t.Error("session should be invalid after logout")
	}
}

func reqWithCookie(path string, c *http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.AddCookie(c)
	return r
}

func TestOAuthAdmitsAnyone(t *testing.T) {
	none := NewOAuth(OAuthConfig{ClientID: "x", ClientSecret: "y"})
	if none.AdmitsAnyone() {
		t.Error("no logins and no org should admit no one")
	}
	byLogin := NewOAuth(OAuthConfig{ClientID: "x", ClientSecret: "y", AdminLogins: []string{"alice"}})
	if !byLogin.AdmitsAnyone() {
		t.Error("an allowlist should admit someone")
	}
	byOrg := NewOAuth(OAuthConfig{ClientID: "x", ClientSecret: "y", AdminOrg: "m31labs"})
	if !byOrg.AdmitsAnyone() {
		t.Error("an org should admit someone")
	}
}

// TestSessionSurvivesRestart proves DB-backed sessions persist across a
// hub restart: a session minted by one OAuth instance is still valid when
// looked up by a fresh instance sharing the same store.
func TestSessionSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	conn, err := db.Open(filepath.Join(root, "test.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer conn.Close()

	before := NewOAuth(OAuthConfig{ClientID: "x", ClientSecret: "y", AdminLogins: []string{"alice"}, Store: conn})
	rec := httptest.NewRecorder()
	if !before.grantSession(rec, "alice") {
		t.Fatal("grantSession failed")
	}
	cookie := rec.Result().Cookies()[0]

	// Simulate a restart: brand-new OAuth over the same DB.
	after := NewOAuth(OAuthConfig{ClientID: "x", ClientSecret: "y", AdminLogins: []string{"alice"}, Store: conn})
	login, ok := after.sessionLogin(reqWithCookie("/admin", cookie))
	if !ok || login != "alice" {
		t.Fatalf("session lost across restart: login=%q ok=%v", login, ok)
	}
}

func TestOrgScopeRequestedOnlyWhenConfigured(t *testing.T) {
	withOrg := NewOAuth(OAuthConfig{ClientID: "x", ClientSecret: "y", AdminOrg: "m31labs"})
	rec := httptest.NewRecorder()
	withOrg.handleStart(rec, httptest.NewRequest(http.MethodGet, "/admin/oauth/github/start", nil))
	if loc := rec.Header().Get("Location"); !strings.Contains(loc, "read%3Aorg") && !strings.Contains(loc, "read:org") {
		t.Errorf("org-configured start should request read:org scope; Location=%q", loc)
	}

	noOrg := NewOAuth(OAuthConfig{ClientID: "x", ClientSecret: "y", AdminLogins: []string{"alice"}})
	rec2 := httptest.NewRecorder()
	noOrg.handleStart(rec2, httptest.NewRequest(http.MethodGet, "/admin/oauth/github/start", nil))
	if loc := rec2.Header().Get("Location"); strings.Contains(loc, "read%3Aorg") || strings.Contains(loc, "read:org") {
		t.Errorf("allowlist-only start should not request read:org; Location=%q", loc)
	}
}
