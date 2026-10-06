"use strict";
// githints dependency graph viewer. Self-contained and offline: no network,
// no libraries, and every string from the repository reaches the page through
// textContent only, never as markup.
(function () {
  const data = JSON.parse(document.getElementById("graph-data").textContent);
  const $ = (id) => document.getElementById(id);
  const canvas = $("graph");
  const ctx = canvas.getContext("2d");
  const tooltip = $("tooltip");

  // ---- model -------------------------------------------------------------
  const nodes = data.nodes.map((n, i) => ({
    ...n, i, x: 0, y: 0, vx: 0, vy: 0, fixed: false,
    // External targets are namespaced "ext:" in the data so they can never
    // collide with a file; the prefix is not shown.
    name: n.external ? n.id.slice(4) : n.id,
    // Adjacency, kept apart from the data's own "in"/"out" degree counts.
    imports: [], importedBy: [], top: (n.dir === "." ? "." : n.dir.split("/")[0]),
  }));
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const edges = [];
  for (const e of data.edges) {
    const a = byId.get(e.from), b = byId.get(e.to);
    if (!a || !b) continue;
    a.imports.push(b); b.importedBy.push(a);
    edges.push([a, b]);
  }
  for (const n of nodes) n.r = 3 + Math.min(15, 2.2 * Math.sqrt(n.in));

  // ---- header ------------------------------------------------------------
  const when = data.last_indexed_at ? new Date(data.last_indexed_at * 1000).toLocaleString() : "never";
  const pkgs = nodes.filter((n) => n.files && n.files.length).length;
  const ext = nodes.filter((n) => n.external).length;
  const plural = (k, one, many) => `${k} ${k === 1 ? one : many}`;
  const parts = [plural(nodes.length - pkgs - ext, "file", "files")];
  if (pkgs) parts.push(plural(pkgs, "package", "packages"));
  if (ext) parts.push(`${ext} external`);
  $("stats").textContent = `${parts.join(" · ")} · ${edges.length} imports · indexed ${when}`;
  if (data.focus) $("stats").textContent += ` · around ${data.focus} (depth ${data.depth})`;
  if (data.truncated) {
    $("truncated").hidden = false;
    $("truncated").textContent = `showing the ${nodes.length} most connected of ${data.total_nodes}; use -focus to explore the rest`;
  }

  // ---- color: categorical, three validated slots, the rest folds to Other --
  const css = () => getComputedStyle(document.documentElement);
  let palette = {};
  function readPalette() {
    const s = css();
    const v = (k) => s.getPropertyValue(k).trim();
    palette = {
      surface: v("--surface"), ink: v("--ink"), ink2: v("--ink-2"), muted: v("--muted"),
      edge: v("--edge"), edgeHi: v("--edge-hi"), other: v("--other"), focus: v("--focus"),
      series: [v("--series-1"), v("--series-2"), v("--series-3")],
    };
  }
  readPalette();

  const keyFns = {
    language: (n) => n.external ? "external" : (n.language || "unknown"),
    directory: (n) => n.external ? "external" : n.top,
    facet: (n) => n.external ? "external" : ((n.facets && n.facets[0]) || "none"),
  };
  let colorBy = "language";
  let categories = []; // [{key, count, slot}] slot -1 = Other
  let slotOf = new Map();
  let hiddenCats = new Set();

  function buildCategories() {
    const counts = new Map();
    for (const n of nodes) {
      const k = keyFns[colorBy](n);
      counts.set(k, (counts.get(k) || 0) + 1);
    }
    const sorted = [...counts.entries()]
      .filter(([k]) => k !== "external")
      .sort((a, b) => b[1] - a[1] || (a[0] < b[0] ? -1 : 1));
    slotOf = new Map();
    // A node-link diagram puts any two colors side by side, so only the three
    // slots that stay distinct for every pair (incl. color-vision deficiency)
    // are used; the remaining categories share the neutral Other.
    sorted.slice(0, 3).forEach(([k], i) => slotOf.set(k, i));
    categories = sorted.slice(0, 3).map(([k, c], i) => ({ key: k, label: k, count: c, slot: i }));
    const rest = sorted.slice(3).reduce((s, [, c]) => s + c, 0);
    if (rest) categories.push({ key: "__other", label: `other (${sorted.length - 3} more)`, count: rest, slot: -1 });
    if (counts.has("external")) categories.push({ key: "external", label: "external package", count: counts.get("external"), slot: -2 });
    hiddenCats = new Set();
    renderLegend();
  }
  function catKey(n) {
    const k = keyFns[colorBy](n);
    if (k === "external") return "external";
    return slotOf.has(k) ? k : "__other";
  }
  function fillOf(n) {
    const k = keyFns[colorBy](n);
    if (slotOf.has(k)) return palette.series[slotOf.get(k)];
    return palette.other;
  }

  function renderLegend() {
    const legend = $("legend");
    legend.replaceChildren();
    for (const c of categories) {
      const b = document.createElement("button");
      b.type = "button";
      b.setAttribute("aria-pressed", "true");
      b.title = "Show or dim this group";
      const sw = document.createElement("span");
      sw.className = "swatch" + (c.slot === -2 ? " hollow" : "");
      sw.style.background = c.slot >= 0 ? palette.series[c.slot] : palette.other;
      const label = document.createElement("span");
      label.textContent = c.label;
      const count = document.createElement("span");
      count.className = "count";
      count.textContent = String(c.count);
      b.append(sw, label, count);
      b.addEventListener("click", () => {
        if (hiddenCats.has(c.key)) hiddenCats.delete(c.key); else hiddenCats.add(c.key);
        b.setAttribute("aria-pressed", hiddenCats.has(c.key) ? "false" : "true");
        draw();
      });
      legend.append(b);
    }
  }

  // ---- layout: force simulation with grid-bucketed repulsion ----------------
  function seedPositions() {
    // Files start grouped by top-level directory so the settled layout keeps
    // packages together, and the result is the same on every load.
    const dirs = [...new Set(nodes.map((n) => n.top))].sort();
    const R = 40 * Math.sqrt(nodes.length + 1);
    const hash = (s) => { let h = 2166136261; for (let i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 16777619); } return (h >>> 0) / 4294967296; };
    for (const n of nodes) {
      const a = (dirs.indexOf(n.top) / Math.max(1, dirs.length)) * Math.PI * 2;
      const j = hash(n.id), k = hash(n.id + "#");
      n.x = Math.cos(a) * R * 0.6 + (j - 0.5) * R * 0.5;
      n.y = Math.sin(a) * R * 0.6 + (k - 0.5) * R * 0.5;
    }
  }
  let alpha = 1;
  const CELL = 90, CUT2 = 180 * 180;
  function tick() {
    const grid = new Map();
    for (const n of nodes) {
      const key = Math.floor(n.x / CELL) + "," + Math.floor(n.y / CELL);
      let b = grid.get(key); if (!b) grid.set(key, (b = [])); b.push(n);
    }
    for (const n of nodes) {
      const cx = Math.floor(n.x / CELL), cy = Math.floor(n.y / CELL);
      for (let dx = -2; dx <= 2; dx++) for (let dy = -2; dy <= 2; dy++) {
        const b = grid.get((cx + dx) + "," + (cy + dy)); if (!b) continue;
        for (const m of b) {
          if (m.i <= n.i) continue;
          let x = n.x - m.x, y = n.y - m.y, d2 = x * x + y * y;
          if (d2 > CUT2) continue;
          if (d2 < 0.01) { x = Math.random() - 0.5; y = Math.random() - 0.5; d2 = 0.5; }
          const f = (900 * alpha) / d2;
          n.vx += x * f; n.vy += y * f; m.vx -= x * f; m.vy -= y * f;
        }
      }
    }
    for (const [a, b] of edges) {
      const x = b.x - a.x, y = b.y - a.y, d = Math.sqrt(x * x + y * y) || 1;
      const f = ((d - 60) / d) * 0.05 * alpha;
      a.vx += x * f; a.vy += y * f; b.vx -= x * f; b.vy -= y * f;
    }
    for (const n of nodes) {
      n.vx -= n.x * 0.004 * alpha; n.vy -= n.y * 0.004 * alpha;
      if (n.fixed) { n.vx = n.vy = 0; continue; }
      n.vx *= 0.82; n.vy *= 0.82;
      const v = Math.hypot(n.vx, n.vy); if (v > 30) { n.vx *= 30 / v; n.vy *= 30 / v; }
      n.x += n.vx; n.y += n.vy;
    }
    alpha *= 0.985;
  }

  // ---- view ----------------------------------------------------------------
  let view = { x: 0, y: 0, k: 1 };
  let W = 0, H = 0, dpr = 1;
  function resize() {
    const r = canvas.getBoundingClientRect();
    dpr = window.devicePixelRatio || 1;
    W = r.width; H = r.height;
    canvas.width = Math.max(1, Math.round(W * dpr));
    canvas.height = Math.max(1, Math.round(H * dpr));
    draw();
  }
  const toScreen = (n) => [(n.x - view.x) * view.k + W / 2, (n.y - view.y) * view.k + H / 2];
  const toWorld = (sx, sy) => [(sx - W / 2) / view.k + view.x, (sy - H / 2) / view.k + view.y];
  function fit() {
    if (!nodes.length) return;
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    for (const n of nodes) { x0 = Math.min(x0, n.x); y0 = Math.min(y0, n.y); x1 = Math.max(x1, n.x); y1 = Math.max(y1, n.y); }
    view.x = (x0 + x1) / 2; view.y = (y0 + y1) / 2;
    view.k = Math.min(2, 0.9 * Math.min(W / Math.max(40, x1 - x0), H / Math.max(40, y1 - y0)));
    draw();
  }
  function centerOn(n) { view.x = n.x; view.y = n.y; view.k = Math.max(view.k, 1.2); draw(); }

  // ---- state ---------------------------------------------------------------
  let selected = null, hovered = null, matches = new Set(), labels = true;
  const neighbors = (n) => new Set([n, ...n.imports, ...n.importedBy]);
  const hubs = new Set([...nodes].sort((a, b) => b.in - a.in).slice(0, 12).filter((n) => n.in > 0));

  function visible(n) { return !hiddenCats.has(catKey(n)); }

  function draw() {
    if (!W) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.fillStyle = palette.surface;
    ctx.fillRect(0, 0, W, H);
    const near = selected ? neighbors(selected) : null;
    const lit = (n) => visible(n) && (!near || near.has(n)) && (!matches.size || matches.has(n) || (near && near.has(n)));

    // Edges: hairlines; the selection's edges are inked, with arrowheads.
    ctx.lineWidth = 1;
    ctx.strokeStyle = palette.edge;
    ctx.beginPath();
    for (const [a, b] of edges) {
      if (!visible(a) || !visible(b)) continue;
      if (near && (a === selected || b === selected)) continue;
      if ((near || matches.size) && !(lit(a) && lit(b))) continue;
      const [ax, ay] = toScreen(a), [bx, by] = toScreen(b);
      ctx.moveTo(ax, ay); ctx.lineTo(bx, by);
    }
    ctx.stroke();
    if (selected) {
      ctx.strokeStyle = palette.edgeHi; ctx.fillStyle = palette.edgeHi; ctx.lineWidth = 1.5;
      for (const [a, b] of edges) {
        if (a !== selected && b !== selected) continue;
        if (!visible(a) || !visible(b)) continue;
        const [ax, ay] = toScreen(a), [bx, by] = toScreen(b);
        const d = Math.hypot(bx - ax, by - ay) || 1, ux = (bx - ax) / d, uy = (by - ay) / d;
        const tx = bx - ux * (b.r * view.k + 2), ty = by - uy * (b.r * view.k + 2);
        ctx.beginPath(); ctx.moveTo(ax, ay); ctx.lineTo(tx, ty); ctx.stroke();
        ctx.beginPath(); ctx.moveTo(tx, ty);
        ctx.lineTo(tx - ux * 7 - uy * 3.5, ty - uy * 7 + ux * 3.5);
        ctx.lineTo(tx - ux * 7 + uy * 3.5, ty - uy * 7 - ux * 3.5);
        ctx.closePath(); ctx.fill();
      }
    }

    // Nodes, with a 2px surface ring so overlapping marks stay separable.
    for (const n of nodes) {
      if (!visible(n)) continue;
      const [x, y] = toScreen(n);
      const r = Math.max(2.5, n.r * Math.sqrt(view.k));
      if (x < -r || y < -r || x > W + r || y > H + r) continue;
      ctx.globalAlpha = lit(n) ? 1 : 0.15;
      ctx.beginPath(); ctx.arc(x, y, r + 2, 0, Math.PI * 2); ctx.fillStyle = palette.surface; ctx.fill();
      ctx.beginPath(); ctx.arc(x, y, r, 0, Math.PI * 2);
      if (n.external) { ctx.strokeStyle = palette.muted; ctx.lineWidth = 2; ctx.stroke(); }
      else { ctx.fillStyle = fillOf(n); ctx.fill(); }
      if (n === selected || n === hovered || (matches.size && matches.has(n))) {
        ctx.beginPath(); ctx.arc(x, y, r + 4, 0, Math.PI * 2);
        ctx.strokeStyle = palette.ink; ctx.lineWidth = 2; ctx.stroke();
      }
    }
    ctx.globalAlpha = 1;

    // Labels: selective, never on every node. Ink text on a surface halo.
    if (labels) {
      const want = new Set();
      if (near) near.forEach((n) => want.add(n));
      else if (matches.size) matches.forEach((n) => want.add(n));
      else if (view.k > 0.6) hubs.forEach((n) => want.add(n));
      if (hovered) want.add(hovered);
      ctx.font = "12px system-ui, -apple-system, 'Segoe UI', sans-serif";
      ctx.textBaseline = "middle";
      ctx.lineJoin = "round";
      for (const n of want) {
        if (!visible(n)) continue;
        const [x, y] = toScreen(n);
        const r = Math.max(2.5, n.r * Math.sqrt(view.k));
        const text = n.name.length > 48 ? "…" + n.name.slice(-47) : n.name;
        ctx.lineWidth = 4; ctx.strokeStyle = palette.surface; ctx.strokeText(text, x + r + 6, y);
        ctx.fillStyle = n === selected ? palette.ink : palette.ink2; ctx.fillText(text, x + r + 6, y);
      }
    }
  }

  // ---- side panel ----------------------------------------------------------
  function el(tag, text, cls) { const e = document.createElement(tag); if (text !== undefined) e.textContent = text; if (cls) e.className = cls; return e; }
  function nodeList(title, list) {
    const frag = document.createDocumentFragment();
    frag.append(el("h3", `${title} (${list.length})`));
    const ul = el("ul");
    for (const m of [...list].sort((a, b) => (a.name < b.name ? -1 : 1))) {
      const li = el("li");
      if (m.external) { li.append(el("div", m.name, "ext")); }
      else {
        const b = el("button", m.name); b.type = "button";
        b.addEventListener("click", () => { select(m); centerOn(m); });
        li.append(b);
      }
      ul.append(li);
    }
    if (!list.length) ul.append(el("li", "none", "hint"));
    frag.append(ul);
    return frag;
  }
  function renderPanel() {
    const panel = $("panel");
    panel.replaceChildren();
    if (!selected) {
      panel.append(el("h2", "Dependency graph"),
        el("p", "Click a file to see what it imports and what imports it. Drag to pan, scroll to zoom, drag a node to move it. Press / to search and Esc to clear.", "hint"));
      return;
    }
    const n = selected;
    panel.append(el("h2", n.name));
    const dl = el("dl");
    const row = (k, v) => dl.append(el("dt", k), el("dd", v));
    if (n.external) row("kind", "external package (not indexed)");
    else {
      row("kind", n.files && n.files.length ? `package (${n.files.length} files)` : "file");
      row("language", n.language || "unknown"); row("directory", n.dir); row("symbols", String(n.symbols));
    }
    row("imported by", String(n.in));
    row("imports", String(n.out));
    if (n.facets && n.facets.length) row("roles", n.facets.join(", "));
    panel.append(dl);
    panel.append(nodeList("Imports", n.imports), nodeList("Imported by", n.importedBy));
    if (n.files && n.files.length) {
      // A package node: every file that shares this import path.
      panel.append(el("h3", `Files (${n.files.length})`));
      const ul = el("ul");
      for (const f of n.files) ul.append(el("li", f, "ext"));
      panel.append(ul);
    }
  }
  function select(n) {
    selected = n; renderPanel(); draw();
    // Mirror the selection in the URL fragment so a view can be bookmarked or
    // shared; fragments never leave the browser.
    try { history.replaceState(null, "", n ? "#" + encodeURIComponent(n.name) : location.pathname); } catch (_) { /* file:// in some browsers */ }
  }
  const byName = new Map(nodes.map((n) => [n.name, n]));

  // ---- table view (the accessible, sortable alternative) ---------------------
  let sortKey = "in", sortDir = -1;
  function renderTable() {
    const body = $("table-body");
    body.replaceChildren();
    const rows = [...nodes].filter(visible).sort((a, b) => {
      const va = a[sortKey], vb = b[sortKey];
      return (va < vb ? -1 : va > vb ? 1 : 0) * sortDir || (a.id < b.id ? -1 : 1);
    });
    for (const n of rows) {
      const tr = el("tr");
      const c = el("td"); const b = el("button", n.name); b.type = "button";
      b.className = "linklike";
      b.addEventListener("click", () => { toggleTable(false); select(n); centerOn(n); });
      c.append(b);
      tr.append(c, el("td", n.external ? "external" : (n.language || "")),
        el("td", String(n.in), "num"), el("td", String(n.out), "num"), el("td", String(n.symbols), "num"));
      body.append(tr);
    }
  }
  function toggleTable(on) {
    $("table-view").hidden = !on;
    $("toggle-table").setAttribute("aria-pressed", String(on));
    if (on) renderTable();
  }
  document.querySelectorAll("th button[data-key]").forEach((b) => b.addEventListener("click", () => {
    const k = b.dataset.key;
    sortDir = sortKey === k ? -sortDir : (k === "id" || k === "language" ? 1 : -1);
    sortKey = k; renderTable();
  }));

  // ---- interaction -----------------------------------------------------------
  function hit(sx, sy) {
    const [wx, wy] = toWorld(sx, sy);
    let best = null, bd = Infinity;
    for (const n of nodes) {
      if (!visible(n)) continue;
      const d = Math.hypot(n.x - wx, n.y - wy);
      const reach = (Math.max(2.5, n.r * Math.sqrt(view.k)) + 6) / view.k; // target bigger than the mark
      if (d < reach && d < bd) { best = n; bd = d; }
    }
    return best;
  }
  function showTooltip(n, sx, sy) {
    if (!n) { tooltip.hidden = true; return; }
    tooltip.replaceChildren(el("div", n.name),
      el("div", n.external ? "external package" : `${n.language || "unknown"} · ${n.symbols} symbols`, "sub"),
      el("div", `imported by ${n.in} · imports ${n.out}`, "sub"));
    tooltip.hidden = false;
    const box = $("stage").getBoundingClientRect();
    const tw = tooltip.offsetWidth, th = tooltip.offsetHeight;
    tooltip.style.left = Math.min(box.width - tw - 8, sx + 14) + "px";
    tooltip.style.top = Math.min(box.height - th - 8, sy + 14) + "px";
  }

  let drag = null, touched = false;
  canvas.addEventListener("pointerdown", (e) => {
    touched = true;
    canvas.setPointerCapture(e.pointerId);
    const n = hit(e.offsetX, e.offsetY);
    drag = { n, sx: e.offsetX, sy: e.offsetY, vx: view.x, vy: view.y, moved: false };
    canvas.classList.add("dragging");
  });
  canvas.addEventListener("pointermove", (e) => {
    if (drag) {
      const dx = e.offsetX - drag.sx, dy = e.offsetY - drag.sy;
      if (Math.abs(dx) + Math.abs(dy) > 3) drag.moved = true;
      if (drag.n && drag.moved) {
        const [wx, wy] = toWorld(e.offsetX, e.offsetY);
        drag.n.x = wx; drag.n.y = wy; drag.n.fixed = true; alpha = Math.max(alpha, 0.15); kick();
      } else if (!drag.n) {
        view.x = drag.vx - dx / view.k; view.y = drag.vy - dy / view.k; draw();
      }
      return;
    }
    const n = hit(e.offsetX, e.offsetY);
    if (n !== hovered) { hovered = n; draw(); }
    showTooltip(n, e.offsetX, e.offsetY);
  });
  canvas.addEventListener("pointerup", () => {
    canvas.classList.remove("dragging");
    if (drag && !drag.moved) select(drag.n);
    drag = null;
  });
  canvas.addEventListener("pointerleave", () => { hovered = null; tooltip.hidden = true; draw(); });
  canvas.addEventListener("wheel", (e) => {
    e.preventDefault();
    touched = true;
    const [wx, wy] = toWorld(e.offsetX, e.offsetY);
    view.k = Math.min(8, Math.max(0.05, view.k * Math.exp(-e.deltaY * 0.0015)));
    view.x = wx - (e.offsetX - W / 2) / view.k; view.y = wy - (e.offsetY - H / 2) / view.k;
    draw();
  }, { passive: false });

  const search = $("search");
  search.addEventListener("input", () => {
    const q = search.value.trim().toLowerCase();
    // A package matches by any of its files too, so searching for a file name
    // finds the package it belongs to.
    matches = new Set(q ? nodes.filter((n) => n.name.toLowerCase().includes(q) ||
      (n.files || []).some((f) => f.toLowerCase().includes(q))) : []);
    $("match-count").textContent = q ? `${matches.size} match${matches.size === 1 ? "" : "es"}` : "";
    draw();
  });
  search.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && matches.size) { const n = [...matches].sort((a, b) => b.in - a.in)[0]; select(n); centerOn(n); }
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "/" && document.activeElement !== search) { e.preventDefault(); search.focus(); }
    if (e.key === "Escape") { select(null); search.value = ""; matches = new Set(); $("match-count").textContent = ""; draw(); }
  });
  $("color-by").addEventListener("change", (e) => { colorBy = e.target.value; buildCategories(); draw(); if (!$("table-view").hidden) renderTable(); });
  $("fit").addEventListener("click", fit);
  $("toggle-labels").addEventListener("click", (e) => { labels = !labels; e.currentTarget.setAttribute("aria-pressed", String(labels)); draw(); });
  $("toggle-table").addEventListener("click", () => toggleTable($("table-view").hidden));
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => { readPalette(); renderLegend(); draw(); });
  new ResizeObserver(resize).observe(canvas);

  // ---- run -------------------------------------------------------------------
  let running = false;
  function kick() {
    if (running) return;
    running = true;
    const step = () => {
      const t0 = performance.now();
      while (alpha > 0.02 && performance.now() - t0 < 12) tick();
      draw();
      if (alpha > 0.02) requestAnimationFrame(step); else running = false;
    };
    requestAnimationFrame(step);
  }
  seedPositions();
  // Settle most of the layout before the first paint so the graph does not
  // fly in from the seed positions.
  const t0 = performance.now();
  while (alpha > 0.2 && performance.now() - t0 < 400) tick();
  buildCategories();
  renderPanel();
  resize();
  fit();
  kick();
  if (location.hash.length > 1) {
    const n = byName.get(decodeURIComponent(location.hash.slice(1)));
    if (n) { select(n); touched = true; setTimeout(() => centerOn(n), 900); }
  }
  // Refit once the layout has spread out, unless the user already moved the view.
  setTimeout(() => { if (!touched) fit(); }, 1200);
})();
