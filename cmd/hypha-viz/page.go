package main

import (
	"github.com/odvcencio/gosx"
	"github.com/odvcencio/gosx/server"
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

// Node type colors — earth-tone palette, distinguishable but quiet.
const pageJS = `
(function() {
const TYPE_COLORS = {
  concept:     '#7b5c3a',
  decision:    '#5a7b3a',
  initiative:  '#3a5c7b',
  lesson:      '#7b3a5c',
  spec:        '#5c7b3a',
  plan:        '#3a7b5c',
  spore:       '#7b6b3a',
  skill:       '#3a6b7b',
  protocol:    '#6b3a7b',
  integration: '#7b3a3a',
  readme:      '#9a8870',
  identity:    '#6b7b3a',
};
const DEFAULT_COLOR = '#9a8870';
const EDGE_COLOR       = 'rgba(120,100,75,0.25)';
const EDGE_GRAFT_COLOR = 'rgba(160,90,40,0.55)';
const GRAFT_KINDS = ['derived_from', 'graft'];

// State
let nodes = [], edges = [];
let pos = {};
let vel = {};
let selected = null;
let center = null;
let transform = { x: 0, y: 0, scale: 1 };
let dragging = false, dragNode = null, lastMouse = null;
let animFrame = null;
let statusNodes = 0, statusEdges = 0;

const canvas = document.getElementById('graph-canvas');
const ctx    = canvas.getContext('2d');
const wrap   = document.getElementById('canvas-wrap');

function resize() {
  canvas.width  = wrap.clientWidth;
  canvas.height = wrap.clientHeight;
  draw();
}

// ---- Layout: simple force-directed ----------------------------------------

const REPEL = 4500;
const ATTRACT = 0.012;
const DAMPING = 0.82;
const IDEAL_EDGE = 120;

function initPositions() {
  const w = canvas.width || 800, h = canvas.height || 600;
  nodes.forEach((n, i) => {
    if (!pos[n.id]) {
      const angle = (i / Math.max(nodes.length, 1)) * Math.PI * 2;
      const r = Math.min(w, h) * 0.3;
      pos[n.id] = { x: w / 2 + r * Math.cos(angle), y: h / 2 + r * Math.sin(angle) };
    }
    vel[n.id] = vel[n.id] || { x: 0, y: 0 };
  });
}

function tick() {
  const fx = {}, fy = {};
  nodes.forEach(n => { fx[n.id] = 0; fy[n.id] = 0; });

  // Repulsion
  for (let i = 0; i < nodes.length; i++) {
    for (let j = i + 1; j < nodes.length; j++) {
      const a = nodes[i], b = nodes[j];
      const pa = pos[a.id], pb = pos[b.id];
      if (!pa || !pb) continue;
      const dx = pa.x - pb.x, dy = pa.y - pb.y;
      const dist = Math.sqrt(dx*dx + dy*dy) || 1;
      const force = REPEL / (dist * dist);
      const ux = dx / dist, uy = dy / dist;
      fx[a.id] += ux * force; fy[a.id] += uy * force;
      fx[b.id] -= ux * force; fy[b.id] -= uy * force;
    }
  }

  // Attraction along edges
  edges.forEach(e => {
    const pa = pos[e.from], pb = pos[e.to];
    if (!pa || !pb) return;
    const dx = pb.x - pa.x, dy = pb.y - pa.y;
    const dist = Math.sqrt(dx*dx + dy*dy) || 1;
    const force = ATTRACT * (dist - IDEAL_EDGE);
    const ux = dx / dist, uy = dy / dist;
    fx[e.from] += ux * force; fy[e.from] += uy * force;
    fx[e.to]   -= ux * force; fy[e.to]   -= uy * force;
  });

  // Center gravity
  const cx = canvas.width / 2, cy = canvas.height / 2;
  nodes.forEach(n => {
    const p = pos[n.id];
    if (!p) return;
    fx[n.id] += (cx - p.x) * 0.004;
    fy[n.id] += (cy - p.y) * 0.004;
  });

  // Integrate
  let moving = false;
  nodes.forEach(n => {
    if (dragNode && dragNode === n.id) return;
    const v = vel[n.id] || { x: 0, y: 0 };
    v.x = (v.x + fx[n.id]) * DAMPING;
    v.y = (v.y + fy[n.id]) * DAMPING;
    vel[n.id] = v;
    const p = pos[n.id];
    if (!p) return;
    p.x += v.x;
    p.y += v.y;
    if (Math.abs(v.x) + Math.abs(v.y) > 0.1) moving = true;
  });
  return moving;
}

function loop() {
  const moving = tick();
  draw();
  if (moving) {
    animFrame = requestAnimationFrame(loop);
  } else {
    animFrame = null;
  }
}

function startLoop() {
  if (animFrame) return;
  animFrame = requestAnimationFrame(loop);
}

// ---- Draw ------------------------------------------------------------------

const NODE_RADIUS = 7;
const SELECTED_RADIUS = 10;

function draw() {
  const w = canvas.width, h = canvas.height;
  ctx.clearRect(0, 0, w, h);

  ctx.save();
  ctx.translate(transform.x, transform.y);
  ctx.scale(transform.scale, transform.scale);

  // Draw edges
  edges.forEach(e => {
    const pa = pos[e.from], pb = pos[e.to];
    if (!pa || !pb) return;
    const isGraft = GRAFT_KINDS.includes(e.kind);
    ctx.beginPath();
    ctx.moveTo(pa.x, pa.y);
    ctx.lineTo(pb.x, pb.y);
    ctx.strokeStyle = isGraft ? EDGE_GRAFT_COLOR : EDGE_COLOR;
    ctx.lineWidth = isGraft ? 1.5 : 1;
    ctx.stroke();
  });

  // Draw nodes
  nodes.forEach(n => {
    const p = pos[n.id];
    if (!p) return;
    const isSel = selected === n.id;
    const r = isSel ? SELECTED_RADIUS : NODE_RADIUS;
    const color = TYPE_COLORS[n.type] || DEFAULT_COLOR;

    ctx.beginPath();
    ctx.arc(p.x, p.y, r, 0, Math.PI * 2);
    ctx.fillStyle = isSel ? color : color + 'cc';
    ctx.fill();
    if (isSel) {
      ctx.strokeStyle = color;
      ctx.lineWidth = 2;
      ctx.stroke();
    }

    // Label only when zoomed in enough or selected
    if (transform.scale > 0.8 || isSel) {
      ctx.fillStyle = isSel ? '#2c2a26' : '#5a5550';
      ctx.font = isSel ? 'bold 11px sans-serif' : '10px sans-serif';
      ctx.textAlign = 'center';
      ctx.fillText(n.label.length > 24 ? n.label.slice(0, 22) + '…' : n.label, p.x, p.y + r + 12);
    }
  });

  ctx.restore();
}

// ---- Load data -------------------------------------------------------------

async function loadGraph(centerID, depth) {
  let url = '/api/graph';
  const params = [];
  if (centerID) params.push('center=' + encodeURIComponent(centerID));
  if (depth)    params.push('depth=' + depth);
  if (params.length) url += '?' + params.join('&');

  try {
    const res = await fetch(url);
    const data = await res.json();
    nodes = data.nodes || [];
    edges = data.edges || [];
    statusNodes = nodes.length;
    statusEdges = edges.length;
    center = centerID || null;
    pos = {};
    vel = {};
    initPositions();
    updateStatus();
    startLoop();
  } catch (e) {
    console.error('graph load error', e);
  }
}

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
  panel.querySelector('.detail-title').textContent = obj.title || obj.id || id;
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
  loadGraph(id, 2);
}

function updateStatus() {
  document.getElementById('status-nodes').textContent = statusNodes + ' nodes';
  document.getElementById('status-edges').textContent = statusEdges + ' edges';
  document.getElementById('status-center').textContent = center ? 'center: ' + center : '';
}

// ---- Mouse interaction -----------------------------------------------------

function screenToWorld(x, y) {
  return {
    x: (x - transform.x) / transform.scale,
    y: (y - transform.y) / transform.scale,
  };
}

function nodeAtPoint(wx, wy) {
  let best = null, bestDist = (SELECTED_RADIUS + 4) / transform.scale;
  nodes.forEach(n => {
    const p = pos[n.id];
    if (!p) return;
    const dx = p.x - wx, dy = p.y - wy;
    const dist = Math.sqrt(dx*dx + dy*dy);
    if (dist < bestDist) { bestDist = dist; best = n; }
  });
  return best;
}

canvas.addEventListener('mousedown', e => {
  const w = screenToWorld(e.offsetX, e.offsetY);
  const hit = nodeAtPoint(w.x, w.y);
  if (hit) {
    dragNode = hit.id;
    selected = hit.id;
    loadDetail(hit.id);
    draw();
  } else {
    dragging = true;
    lastMouse = { x: e.clientX, y: e.clientY };
  }
});

canvas.addEventListener('mousemove', e => {
  if (dragNode) {
    const w = screenToWorld(e.offsetX, e.offsetY);
    pos[dragNode] = { x: w.x, y: w.y };
    vel[dragNode] = { x: 0, y: 0 };
    draw();
  } else if (dragging && lastMouse) {
    transform.x += e.clientX - lastMouse.x;
    transform.y += e.clientY - lastMouse.y;
    lastMouse = { x: e.clientX, y: e.clientY };
    draw();
  }
});

canvas.addEventListener('mouseup', () => {
  if (dragNode) { startLoop(); dragNode = null; }
  dragging = false; lastMouse = null;
});

canvas.addEventListener('wheel', e => {
  e.preventDefault();
  const factor = e.deltaY < 0 ? 1.1 : 0.9;
  const mx = e.offsetX, my = e.offsetY;
  transform.x = mx - factor * (mx - transform.x);
  transform.y = my - factor * (my - transform.y);
  transform.scale *= factor;
  transform.scale = Math.max(0.1, Math.min(transform.scale, 5));
  draw();
}, { passive: false });

// Double-click to recenter
canvas.addEventListener('dblclick', e => {
  const w = screenToWorld(e.offsetX, e.offsetY);
  const hit = nodeAtPoint(w.x, w.y);
  if (hit) recenterOn(hit.id);
});

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
    (data.anchors || []).forEach(a => {
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
    if (data.anchors && data.anchors.length > 0) {
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

window.addEventListener('resize', resize);
resize();
loadGraph('', 0);
})();
`

// GraphPage renders the full HTML page for the knowledge graph viewer.
// It uses GoSX's server.HTMLDocument as the document shell and builds
// the page layout using plain gosx.Node trees — no .gsx, no WASM.
func GraphPage() gosx.Node {
	return gosx.Fragment(
		// App shell
		gosx.El("div", gosx.Attrs(gosx.Attr("id", "app")),
			// Toolbar
			toolbarNode(),
			// Canvas wrap
			canvasWrapNode(),
			// Detail panel
			panelNode(),
			// Status bar
			statusbarNode(),
		),
		// Search results dropdown (positioned absolutely)
		gosx.El("div", gosx.Attrs(gosx.Attr("id", "search-results"))),
	)
}

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

func canvasWrapNode() gosx.Node {
	return gosx.El("div", gosx.Attrs(gosx.Attr("id", "canvas-wrap")),
		gosx.El("canvas", gosx.Attrs(gosx.Attr("id", "graph-canvas"))),
		gosx.El("div", gosx.Attrs(gosx.Attr("id", "canvas-hint")),
			gosx.Text("Scroll to zoom · drag to pan · click node to inspect · double-click to recenter"),
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

// headNode builds the <head> inline content: CSS + deferred JS.
func headNode() gosx.Node {
	return gosx.Fragment(
		gosx.El("style", gosx.RawHTML(pageCSS)),
		gosx.El("script", gosx.Attrs(gosx.Attr("defer", "defer")), gosx.RawHTML(pageJS)),
	)
}

// BuildGraphPage wraps GraphPage in GoSX's HTMLDocument shell.
func BuildGraphPage() gosx.Node {
	return server.HTMLDocument("Hyphae — Knowledge Graph", headNode(), GraphPage())
}
