package hubsync

import (
	"database/sql"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strings"
	"time"

	"m31labs.dev/hyphae/internal/capability"
	"m31labs.dev/hyphae/internal/peers"
	"m31labs.dev/hyphae/internal/receipts"
	"m31labs.dev/hyphae/internal/types"
)

// Admin is the server-rendered HTML control surface for a hub: peers,
// spaces, capability tokens (a.k.a. "API keys"), and the audit log.
// No SPA, no JS build step — plain HTML forms posting back to the same
// handlers. Mounted under /admin by the hub when --admin is set.
//
// Auth model: when a GitHub OAuth gate is configured (see oauth.go),
// every admin route requires a valid GitHub session whose login is on
// the allowlist — reaching the port is not sufficient. When OAuth is
// not configured, the surface is ungated and intended for local
// operator use only (the hub binds 127.0.0.1 by default).
type Admin struct {
	installRoot string
	authConn    *sql.DB // capabilities + receipts live here
	server      *Server
	oauth       *OAuth // nil when GitHub OAuth not configured
}

// NewAdmin builds the admin surface. authConn must be the index DB
// (capabilities + receipts tables). oauth may be nil.
func NewAdmin(installRoot string, authConn *sql.DB, server *Server, oauth *OAuth) *Admin {
	return &Admin{installRoot: installRoot, authConn: authConn, server: server, oauth: oauth}
}

// Mount registers the admin routes on mux. When a GitHub OAuth gate is
// configured, every content route is wrapped so it requires a valid
// session; the OAuth routes themselves are mounted ungated (gating them
// would redirect-loop login).
func (a *Admin) Mount(mux *http.ServeMux) {
	guard := func(h http.HandlerFunc) http.HandlerFunc {
		if a.oauth == nil {
			return h
		}
		return a.oauth.Guard(h)
	}
	mux.HandleFunc("/admin", guard(a.handleHome))
	mux.HandleFunc("/admin/keys", guard(a.handleKeys))
	mux.HandleFunc("/admin/keys/issue", guard(a.handleKeyIssue))
	mux.HandleFunc("/admin/keys/revoke", guard(a.handleKeyRevoke))
	mux.HandleFunc("/admin/peers", guard(a.handlePeers))
	mux.HandleFunc("/admin/peers/add", guard(a.handlePeerAdd))
	mux.HandleFunc("/admin/peers/remove", guard(a.handlePeerRemove))
	mux.HandleFunc("/admin/audit", guard(a.handleAudit))
	if a.oauth != nil {
		a.oauth.Mount(mux)
	}
}

// ─── pages ──────────────────────────────────────────────────────────────────

func (a *Admin) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/admin" {
		http.NotFound(w, r)
		return
	}
	spaces := a.server.Hubs()
	var rows strings.Builder
	keys := make([]string, 0, len(spaces))
	for uri := range spaces {
		keys = append(keys, uri)
	}
	sort.Strings(keys)
	for _, uri := range keys {
		h := spaces[uri]
		fmt.Fprintf(&rows, "<tr><td><code>%s</code></td><td>%d connected</td></tr>",
			html.EscapeString(uri), h.ClientCount())
	}
	page(w, "Overview", fmt.Sprintf(`
<p>Hub serving <strong>%d</strong> space(s).</p>
<table>
  <thead><tr><th>Space</th><th>Clients</th></tr></thead>
  <tbody>%s</tbody>
</table>
<p class="nav">
  <a href="/admin/keys">API keys</a> ·
  <a href="/admin/peers">Peers</a> ·
  <a href="/admin/audit">Audit log</a>
  %s
</p>`, len(spaces), rows.String(), a.oauthNav()))
}

func (a *Admin) oauthNav() string {
	if a.oauth == nil {
		return ""
	}
	return ` · <a href="/admin/oauth/github/logout">Log out</a>`
}

func (a *Admin) handleKeys(w http.ResponseWriter, r *http.Request) {
	if a.authConn == nil {
		page(w, "API keys", `<p class="err">No capability DB configured (start the hub with an index available).</p>`)
		return
	}
	caps, err := capability.List(a.authConn, "", true)
	if err != nil {
		page(w, "API keys", `<p class="err">`+html.EscapeString(err.Error())+`</p>`)
		return
	}
	var rows strings.Builder
	for _, c := range caps {
		status := "active"
		cls := "ok"
		switch {
		case c.RevokedAt != nil:
			status, cls = "revoked", "muted"
		case time.Now().After(c.ExpiresAt):
			status, cls = "expired", "muted"
		}
		revokeBtn := ""
		if c.RevokedAt == nil {
			revokeBtn = fmt.Sprintf(
				`<form method="post" action="/admin/keys/revoke" class="inline">`+
					`<input type="hidden" name="id" value="%s">`+
					`<button type="submit">revoke</button></form>`,
				html.EscapeString(c.ID))
		}
		fmt.Fprintf(&rows,
			`<tr class="%s"><td><code>%s</code></td><td>%s</td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			cls,
			html.EscapeString(truncate(c.ID, 20)),
			html.EscapeString(c.Subject),
			html.EscapeString(c.SpaceID),
			html.EscapeString(strings.Join(c.Permissions, ", ")),
			html.EscapeString(c.ExpiresAt.Format("2006-01-02 15:04")),
			status+revokeBtn,
		)
	}
	form := `
<h3>Issue a key</h3>
<form method="post" action="/admin/keys/issue" class="stack">
  <label>Subject URI <input name="subject" placeholder="agent://cloud/runner" required></label>
  <label>Space URI <input name="space" placeholder="hypha://myorg/knowledge" required></label>
  <label>Permissions <input name="permissions" value="memory:recall,spore:create"></label>
  <label>Expires <input name="expires" value="90d"></label>
  <button type="submit">Issue</button>
</form>`
	page(w, "API keys", fmt.Sprintf(`
<table>
  <thead><tr><th>Token</th><th>Subject</th><th>Space</th><th>Permissions</th><th>Expires</th><th></th></tr></thead>
  <tbody>%s</tbody>
</table>
%s`, rows.String(), form))
}

func (a *Admin) handleKeyIssue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || a.authConn == nil {
		http.Redirect(w, r, "/admin/keys", http.StatusSeeOther)
		return
	}
	subject := strings.TrimSpace(r.FormValue("subject"))
	space := strings.TrimSpace(r.FormValue("space"))
	permsCSV := strings.TrimSpace(r.FormValue("permissions"))
	expiresStr := strings.TrimSpace(r.FormValue("expires"))
	if subject == "" || space == "" {
		page(w, "API keys", `<p class="err">subject and space are required</p><p><a href="/admin/keys">back</a></p>`)
		return
	}
	if permsCSV == "" {
		permsCSV = "memory:recall,spore:create"
	}
	if expiresStr == "" {
		expiresStr = "90d"
	}
	expires, err := parseFlexDuration(expiresStr)
	if err != nil {
		page(w, "API keys", `<p class="err">bad expires: `+html.EscapeString(err.Error())+`</p>`)
		return
	}
	var perms []string
	for _, p := range strings.Split(permsCSV, ",") {
		if p = strings.TrimSpace(p); p != "" {
			perms = append(perms, p)
		}
	}
	cap, err := capability.Issue(a.authConn, subject, space, perms, types.Limits{
		MaxRecallResults: 25, MaxResponseTokens: 800, MaxSpores: 3, MaxBytes: 200000,
	}, expires)
	if err != nil {
		page(w, "API keys", `<p class="err">`+html.EscapeString(err.Error())+`</p>`)
		return
	}
	page(w, "Key issued", fmt.Sprintf(`
<p class="ok">Issued a key. Copy it now — it is shown once.</p>
<pre class="token">%s</pre>
<p>Subject: <code>%s</code><br>Space: <code>%s</code><br>Expires: %s</p>
<p><a href="/admin/keys">back to keys</a></p>`,
		html.EscapeString(cap.ID),
		html.EscapeString(cap.Subject),
		html.EscapeString(cap.SpaceID),
		cap.ExpiresAt.Format(time.RFC3339)))
}

func (a *Admin) handleKeyRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && a.authConn != nil {
		if id := strings.TrimSpace(r.FormValue("id")); id != "" {
			_ = capability.Revoke(a.authConn, id)
		}
	}
	http.Redirect(w, r, "/admin/keys", http.StatusSeeOther)
}

func (a *Admin) handlePeers(w http.ResponseWriter, r *http.Request) {
	list, err := peers.List(a.installRoot)
	if err != nil {
		page(w, "Peers", `<p class="err">`+html.EscapeString(err.Error())+`</p>`)
		return
	}
	var rows strings.Builder
	for _, p := range list {
		fmt.Fprintf(&rows,
			`<tr><td>%s</td><td><code>%s</code></td><td>%s</td><td>`+
				`<form method="post" action="/admin/peers/remove" class="inline">`+
				`<input type="hidden" name="needle" value="%s">`+
				`<button type="submit">remove</button></form></td></tr>`,
			html.EscapeString(p.Name), html.EscapeString(p.URI),
			p.AddedAt.Format("2006-01-02"), html.EscapeString(p.Name))
	}
	page(w, "Peers", fmt.Sprintf(`
<table>
  <thead><tr><th>Name</th><th>URI</th><th>Added</th><th></th></tr></thead>
  <tbody>%s</tbody>
</table>
<h3>Add a peer</h3>
<form method="post" action="/admin/peers/add" class="stack">
  <label>URI <input name="uri" placeholder="ws://hub.internal:7777" required></label>
  <label>Name <input name="name" placeholder="(optional)"></label>
  <button type="submit">Add</button>
</form>`, rows.String()))
}

func (a *Admin) handlePeerAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		uri := strings.TrimSpace(r.FormValue("uri"))
		name := strings.TrimSpace(r.FormValue("name"))
		if uri != "" {
			_, _ = peers.Add(a.installRoot, name, uri)
		}
	}
	http.Redirect(w, r, "/admin/peers", http.StatusSeeOther)
}

func (a *Admin) handlePeerRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if needle := strings.TrimSpace(r.FormValue("needle")); needle != "" {
			_, _ = peers.Remove(a.installRoot, needle)
		}
	}
	http.Redirect(w, r, "/admin/peers", http.StatusSeeOther)
}

func (a *Admin) handleAudit(w http.ResponseWriter, r *http.Request) {
	if a.authConn == nil {
		page(w, "Audit log", `<p class="err">No index DB configured.</p>`)
		return
	}
	list, err := receipts.List(a.authConn, receipts.ListFilter{Limit: 100})
	if err != nil {
		page(w, "Audit log", `<p class="err">`+html.EscapeString(err.Error())+`</p>`)
		return
	}
	var rows strings.Builder
	for _, rc := range list {
		fmt.Fprintf(&rows,
			`<tr><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
			rc.CreatedAt.Format("2006-01-02 15:04"),
			html.EscapeString(rc.Action),
			html.EscapeString(rc.SubjectID),
			html.EscapeString(truncate(rc.ID, 28)))
	}
	page(w, "Audit log", fmt.Sprintf(`
<table>
  <thead><tr><th>When</th><th>Action</th><th>Subject</th><th>Receipt</th></tr></thead>
  <tbody>%s</tbody>
</table>`, rows.String()))
}

// ─── rendering ────────────────────────────────────────────────────────────

func page(w http.ResponseWriter, title, body string) {
	pageStatus(w, http.StatusOK, title, body)
}

// pageStatus renders the same chrome as page but with an explicit HTTP
// status (e.g. 403 for an allowlist rejection). Content-Type must be set
// before WriteHeader, so this writes the header before the body.
func pageStatus(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html>
<html><head><meta charset="utf-8"><title>Hyphae · %s</title>
<style>
  :root { --bg:#1c1a17; --fg:#e8e2d6; --accent:#9c7a4d; --muted:#8a8276; --line:#3a352e; }
  body { background:var(--bg); color:var(--fg); font:15px/1.5 ui-monospace,Menlo,monospace; max-width:920px; margin:2rem auto; padding:0 1rem; }
  h1 { color:var(--accent); font-size:1.3rem; }
  h3 { color:var(--accent); margin-top:2rem; }
  a { color:var(--accent); }
  table { border-collapse:collapse; width:100%%; margin:1rem 0; }
  th,td { text-align:left; padding:.4rem .6rem; border-bottom:1px solid var(--line); vertical-align:top; }
  th { color:var(--muted); font-weight:normal; }
  code,pre { color:#c9b68f; }
  pre.token { background:#13110f; padding:.8rem; border:1px solid var(--line); overflow-x:auto; }
  .stack label { display:block; margin:.5rem 0; }
  .stack input { background:#13110f; color:var(--fg); border:1px solid var(--line); padding:.4rem; width:24rem; max-width:100%%; }
  button { background:var(--accent); color:#1c1a17; border:0; padding:.4rem .8rem; cursor:pointer; }
  form.inline { display:inline; }
  .ok { color:#8fae6b; } .err { color:#c47b6b; } .muted { color:var(--muted); }
  .nav { margin-top:2rem; padding-top:1rem; border-top:1px solid var(--line); }
  header a { margin-right:1rem; }
</style></head>
<body>
<header><a href="/admin">Hyphae hub</a><a href="/admin/keys">keys</a><a href="/admin/peers">peers</a><a href="/admin/audit">audit</a></header>
<h1>%s</h1>
%s
</body></html>`, html.EscapeString(title), html.EscapeString(title), body)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// parseFlexDuration accepts Go durations plus human-friendly shorthands:
// "Nd" → N*24h, "q1".."q4" → 90d. Mirrors the cmd/hypha helper so the
// admin key form accepts the same "90d"-style values as `hypha cap issue`.
func parseFlexDuration(s string) (time.Duration, error) {
	if len(s) > 1 && s[len(s)-1] == 'd' {
		n, err := time.ParseDuration(s[:len(s)-1] + "h")
		if err == nil {
			return n * 24, nil
		}
	}
	if len(s) == 2 && s[0] == 'q' && s[1] >= '1' && s[1] <= '4' {
		return 90 * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}
