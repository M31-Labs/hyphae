//go:build !js

package main

import (
	"m31labs.dev/gosx"
	"m31labs.dev/gosx/engine/surface"
	"m31labs.dev/hyphae/cmd/hypha-viz/graphsurface"
)

// pageCSS is the embedded stylesheet for the knowledge graph viewer.
// Earth-tone palette, generous spacing, sans-serif typography.
const pageCSS = `
* { box-sizing: border-box; margin: 0; padding: 0; }

:root {
	--bg:         #f5f2ec;
	--bg-panel:   #ede9e0;
	--bg-card:    #ffffff;
	--border:     #d8d2c6;
	--text:       #2c2a26;
	--text-muted: #7a7268;
	--accent:     #7b5c3a;
	--accent-alt: #3a5c7b;
	--edge:       rgba(120, 100, 75, 0.35);
	--edge-graft: rgba(160, 90, 40, 0.55);
	--font:       -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
}

html, body {
	height: 100%;
	font-family: var(--font);
	background: var(--bg);
	color: var(--text);
	font-size: 14px;
	line-height: 1.5;
}

#app {
	display: grid;
	grid-template-rows: 48px 1fr 32px;
	grid-template-columns: 1fr 320px;
	grid-template-areas:
		"toolbar toolbar"
		"canvas  panel"
		"status  status";
	height: 100vh;
	overflow: hidden;
}

/* Toolbar */
#toolbar {
	grid-area: toolbar;
	background: var(--bg-panel);
	border-bottom: 1px solid var(--border);
	display: flex;
	align-items: center;
	gap: 12px;
	padding: 0 16px;
}

#toolbar h1 {
	font-size: 13px;
	font-weight: 600;
	color: var(--accent);
	letter-spacing: 0.04em;
	text-transform: uppercase;
	flex-shrink: 0;
}

#search-input {
	flex: 1;
	max-width: 360px;
	height: 28px;
	padding: 0 10px;
	border: 1px solid var(--border);
	border-radius: 4px;
	background: var(--bg-card);
	font-family: var(--font);
	font-size: 13px;
	color: var(--text);
	outline: none;
}
#search-input:focus {
	border-color: var(--accent);
}

#search-results {
	position: absolute;
	top: 48px;
	left: 64px;
	width: 360px;
	background: var(--bg-card);
	border: 1px solid var(--border);
	border-radius: 4px;
	box-shadow: 0 4px 12px rgba(0,0,0,0.08);
	z-index: 100;
	max-height: 280px;
	overflow-y: auto;
	display: none;
}
#search-results.open { display: block; }
#search-results .result-item {
	padding: 8px 12px;
	cursor: pointer;
	border-bottom: 1px solid var(--border);
	font-size: 13px;
}
#search-results .result-item:last-child { border-bottom: none; }
#search-results .result-item:hover { background: var(--bg-panel); }
#search-results .result-uri {
	font-size: 11px;
	color: var(--text-muted);
	margin-top: 2px;
}

/* Canvas area */
#canvas-wrap {
	grid-area: canvas;
	position: relative;
	overflow: hidden;
	background: var(--bg);
}

#graph-canvas {
	position: absolute;
	top: 0; left: 0;
	cursor: grab;
}
#graph-canvas:active { cursor: grabbing; }

#canvas-hint {
	position: absolute;
	bottom: 12px;
	left: 12px;
	font-size: 11px;
	color: var(--text-muted);
	pointer-events: none;
	background: rgba(245,242,236,0.7);
	padding: 4px 8px;
	border-radius: 3px;
}

/* Detail panel */
#panel {
	grid-area: panel;
	background: var(--bg-panel);
	border-left: 1px solid var(--border);
	overflow-y: auto;
	padding: 16px;
	display: flex;
	flex-direction: column;
	gap: 12px;
}

#panel h2 {
	font-size: 14px;
	font-weight: 600;
	color: var(--text);
}

#panel-placeholder {
	color: var(--text-muted);
	font-size: 13px;
}

#node-detail { display: none; }
#node-detail.visible { display: block; }

.detail-type {
	display: inline-block;
	font-size: 11px;
	font-weight: 600;
	text-transform: uppercase;
	letter-spacing: 0.06em;
	color: var(--accent);
	margin-bottom: 6px;
}

.detail-title {
	font-size: 16px;
	font-weight: 600;
	color: var(--text);
	margin-bottom: 8px;
}

.detail-summary {
	font-size: 13px;
	color: var(--text-muted);
	margin-bottom: 12px;
	line-height: 1.6;
}

.links-section h3 {
	font-size: 11px;
	font-weight: 600;
	text-transform: uppercase;
	letter-spacing: 0.06em;
	color: var(--text-muted);
	margin-bottom: 6px;
}

.link-item {
	display: block;
	padding: 5px 0;
	font-size: 12px;
	color: var(--accent-alt);
	cursor: pointer;
	text-decoration: none;
	border-bottom: 1px solid var(--border);
}
.link-item:last-child { border-bottom: none; }
.link-item:hover { color: var(--accent); }
.link-item .link-kind {
	font-size: 10px;
	color: var(--text-muted);
	margin-left: 4px;
}

/* Status bar */
#statusbar {
	grid-area: status;
	background: var(--bg-panel);
	border-top: 1px solid var(--border);
	display: flex;
	align-items: center;
	padding: 0 12px;
	gap: 16px;
	font-size: 11px;
	color: var(--text-muted);
}

#statusbar span { flex-shrink: 0; }
`

// panelJS contains the search-dropdown and detail-panel DOM scripting.
// This is intentionally kept as hand-authored JS for v0.1 because those
// sections are pure DOM manipulation with no canvas involvement. They will
// be rewritten as a //gosx:island in v0.1.4.
//
// What was removed from the original pageJS:
//   - All force-directed layout code (tick, loop, startLoop, initPositions)
//   - All canvas drawing code (draw, resize, TYPE_COLORS, EDGE_* constants)
//   - All mouse/wheel/dblclick listeners on the canvas element
//   - loadGraph() — the graph data is now SSR-embedded in the surface props
//
// What remains:
//   - loadDetail() and renderDetail() — fetched on node click (v0.1.4 TODO)
//   - recenterOn() — triggers a page reload with a new center; for v0.1 this
//     only reloads the panel. A full graph-reload hook will land with the island.
//   - updateStatus() — updates the status bar counts
//   - Search dropdown (searchInput / searchResults)
//   - escHtml helper
//   - Boot: updateStatus() with zero counts (counts come from the WASM surface
//     after it fetches /api/graph; the status bridge is a v0.1.4 TODO)
const panelJS = `
(function() {

// ---- Detail panel ----------------------------------------------------------

let center = null;
let statusNodes = 0, statusEdges = 0;

async function loadDetail(id) {
  try {
    const res = await fetch('/api/object/' + encodeURIComponent(id));
    const data = await res.json();
    renderDetail(data);
  } catch (e) {
    console.error('object detail error', e);
  }
}

function renderDetail(data) {
  const panel = document.getElementById('node-detail');
  const placeholder = document.getElementById('panel-placeholder');
  if (!data || !data.object) {
    panel.classList.remove('visible');
    placeholder.style.display = '';
    return;
  }
  placeholder.style.display = 'none';
  panel.classList.add('visible');

  const obj = data.object;
  panel.querySelector('.detail-type').textContent  = obj.type || '';
  panel.querySelector('.detail-title').textContent = obj.title || obj.id || '';
  panel.querySelector('.detail-summary').textContent = obj.summary || '';

  // Back-links
  const backEl = panel.querySelector('.backlinks-list');
  backEl.innerHTML = '';
  (data.backlinks || []).forEach(n => {
    const a = document.createElement('a');
    a.className = 'link-item';
    a.textContent = n.title || n.endpoint;
    const kindSpan = document.createElement('span');
    kindSpan.className = 'link-kind';
    kindSpan.textContent = n.edge ? n.edge.kind : '';
    a.appendChild(kindSpan);
    a.addEventListener('click', () => recenterOn(n.endpoint));
    backEl.appendChild(a);
  });

  // Forward links
  const fwdEl = panel.querySelector('.forward-list');
  fwdEl.innerHTML = '';
  (data.forward || []).forEach(n => {
    const a = document.createElement('a');
    a.className = 'link-item';
    a.textContent = n.title || n.endpoint;
    const kindSpan = document.createElement('span');
    kindSpan.className = 'link-kind';
    kindSpan.textContent = n.edge ? n.edge.kind : '';
    a.appendChild(kindSpan);
    a.addEventListener('click', () => recenterOn(n.endpoint));
    fwdEl.appendChild(a);
  });
}

function recenterOn(id) {
  center = id;
  updateStatus();
  // v0.1.4 TODO: signal the Graph surface to reload via /api/graph?center=id.
  // For now, a full page navigation reloads with the new center.
  window.location.href = '/?center=' + encodeURIComponent(id);
}

function updateStatus() {
  document.getElementById('status-nodes').textContent = statusNodes + ' nodes';
  document.getElementById('status-edges').textContent = statusEdges + ' edges';
  document.getElementById('status-center').textContent = center ? 'center: ' + center : '';
}

// v0.1: The Graph surface WASM will update statusNodes/statusEdges once it
// has fetched the graph. Bridge hook exposed for the future island:
//   window.__hypha_status_update = (nodes, edges) => { statusNodes = nodes; statusEdges = edges; updateStatus(); };
window.__hypha_status_update = function(n, e) {
  statusNodes = n; statusEdges = e; updateStatus();
};

// v0.1: Exposed for future panel<->canvas wiring (see graph_surface.go onUp).
//   window.__hypha_select_node = (id) => { loadDetail(id); };
window.__hypha_select_node = function(id) { loadDetail(id); };

// ---- Search ----------------------------------------------------------------

const searchInput = document.getElementById('search-input');
const searchResults = document.getElementById('search-results');

searchInput.addEventListener('keydown', async e => {
  if (e.key !== 'Enter') return;
  const q = searchInput.value.trim();
  if (!q) { searchResults.classList.remove('open'); return; }
  try {
    const res = await fetch('/api/search?q=' + encodeURIComponent(q));
    const data = await res.json();
    searchResults.innerHTML = '';
    (data.hits || []).forEach(a => {
      const div = document.createElement('div');
      div.className = 'result-item';
      div.innerHTML = '<div>' + escHtml(a.title || a.uri) + '</div><div class="result-uri">' + escHtml(a.uri) + '</div>';
      div.addEventListener('click', () => {
        // Extract the object id from the URI: hypha://<space>/object/<id>
        const m = a.uri.match(/\/object\/(.+)$/);
        if (m) recenterOn(m[1]);
        searchResults.classList.remove('open');
        searchInput.value = '';
      });
      searchResults.appendChild(div);
    });
    if (data.hits && data.hits.length > 0) {
      searchResults.classList.add('open');
    } else {
      const div = document.createElement('div');
      div.className = 'result-item';
      div.textContent = 'No results for "' + q + '"';
      searchResults.appendChild(div);
      searchResults.classList.add('open');
    }
  } catch(err) {
    console.error('search error', err);
  }
});

document.addEventListener('click', e => {
  if (!searchResults.contains(e.target) && e.target !== searchInput) {
    searchResults.classList.remove('open');
  }
});

function escHtml(s) {
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

// ---- Boot ------------------------------------------------------------------

updateStatus();
})();
`


func toolbarNode() gosx.Node {
	return gosx.El("div", gosx.Attrs(gosx.Attr("id", "toolbar")),
		gosx.El("h1", gosx.Text("Hyphae")),
		gosx.El("input", gosx.Attrs(
			gosx.Attr("id", "search-input"),
			gosx.Attr("type", "text"),
			gosx.Attr("placeholder", "Search knowledge graph…"),
			gosx.Attr("autocomplete", "off"),
			gosx.Attr("spellcheck", "false"),
		)),
	)
}

func canvasWrapNode(props graphsurface.GraphProps) gosx.Node {
	graph := surface.NewRenderer("Graph")
	return gosx.El("div", gosx.Attrs(gosx.Attr("id", "canvas-wrap")),
		graph.Mount(props),
		gosx.El("div", gosx.Attrs(gosx.Attr("id", "canvas-hint")),
			gosx.Text("Scroll to zoom · drag to pan · click to inspect · double-click to recenter"),
		),
	)
}

func panelNode() gosx.Node {
	return gosx.El("div", gosx.Attrs(gosx.Attr("id", "panel")),
		gosx.El("h2", gosx.Text("Knowledge Graph")),
		gosx.El("p", gosx.Attrs(gosx.Attr("id", "panel-placeholder")),
			gosx.Text("Click a node to inspect it."),
		),
		gosx.El("div", gosx.Attrs(gosx.Attr("id", "node-detail")),
			gosx.El("div", gosx.Attrs(gosx.Attr("class", "detail-type"))),
			gosx.El("div", gosx.Attrs(gosx.Attr("class", "detail-title"))),
			gosx.El("div", gosx.Attrs(gosx.Attr("class", "detail-summary"))),
			gosx.El("div", gosx.Attrs(gosx.Attr("class", "links-section")),
				gosx.El("h3", gosx.Text("Backlinks")),
				gosx.El("div", gosx.Attrs(gosx.Attr("class", "backlinks-list"))),
			),
			gosx.El("div", gosx.Attrs(gosx.Attr("class", "links-section")),
				gosx.El("h3", gosx.Text("Forward links")),
				gosx.El("div", gosx.Attrs(gosx.Attr("class", "forward-list"))),
			),
		),
	)
}

func statusbarNode() gosx.Node {
	return gosx.El("div", gosx.Attrs(gosx.Attr("id", "statusbar")),
		gosx.El("span", gosx.Attrs(gosx.Attr("id", "status-nodes")), gosx.Text("0 nodes")),
		gosx.El("span", gosx.Attrs(gosx.Attr("id", "status-edges")), gosx.Text("0 edges")),
		gosx.El("span", gosx.Attrs(gosx.Attr("id", "status-center"))),
	)
}

// headNode builds the <head> inline content: CSS + engine-surface runtime
// scripts + WASM preload + deferred panel JS.
//
// surface.HeadAssets() injects the two script tags (wasm_exec.js + runtime.js)
// that the engine-surface bootstrap depends on; without them the canvas
// placeholder never mounts. graph.PageHead() emits the WASM preload link.
func headNode(graph *surface.Renderer) gosx.Node {
	return gosx.Fragment(
		gosx.El("style", gosx.RawHTML(pageCSS)),
		surface.HeadAssets(),
		graph.PageHead(),
		gosx.El("script", gosx.Attrs(gosx.Attr("defer", "defer")), gosx.RawHTML(panelJS)),
	)
}

// BuildGraphPage returns the page body fragment and the <head> assets it
// needs. Callers must inject HeadAssets into the document head themselves —
// the app's Page() handler bridges that via ctx.AddHead. Wrapping in
// server.HTMLDocument here would double-wrap because the App always emits
// its own <!DOCTYPE html><html>...</html> shell around the route result.
// (Defect 5 / spec §E.)
//
// props are used to embed the graph data into the canvas surface placeholder.
func BuildGraphPage(props graphsurface.GraphProps) (body, head gosx.Node) {
	graph := surface.NewRenderer("Graph")
	return graphPageWithProps(props), headNode(graph)
}

// graphPageWithProps is the internal version of GraphPage that receives props
// so the canvas surface placeholder can embed the initial graph data.
func graphPageWithProps(props graphsurface.GraphProps) gosx.Node {
	return gosx.Fragment(
		gosx.El("div", gosx.Attrs(gosx.Attr("id", "app")),
			toolbarNode(),
			canvasWrapNode(props),
			panelNode(),
			statusbarNode(),
		),
		gosx.El("div", gosx.Attrs(gosx.Attr("id", "search-results"))),
	)
}
