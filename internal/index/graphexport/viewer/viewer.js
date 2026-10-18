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

  // ---- raw graph: what the index holds ---------------------------------------
  const raw = data.nodes.map((n) => ({
    ...n,
    // External targets are namespaced "ext:" in the data so they can never
    // collide with a file; the prefix is not shown.
    name: n.external ? n.id.slice(4) : n.id,
    outE: [], inE: [],
  }));
  const rawById = new Map(raw.map((n) => [n.id, n]));
  const rawEdges = [];
  for (const e of data.edges) {
    const a = rawById.get(e.from), b = rawById.get(e.to);
    if (!a || !b) continue;
    const re = { a, b, test: !!e.test };
    rawEdges.push(re); a.outE.push(re); b.inE.push(re);
  }
  // Test code: test files, and helpers only test code imports (a package of
  // test fixtures is not a dependency of the code it tests).
  for (const n of raw) {
    n.testish = !!n.test || (n.inE.length > 0 && n.inE.every((e) => e.test || e.a.test));
  }

  // Folders are named below the prefix every file shares, so a repository
  // whose code all lives in internal/ is split by internal/<x>/, not lumped
  // into one group.
  const segs = (n) => (n.dir && n.dir !== "." ? n.dir.split("/") : []);
  let common = null;
  for (const n of raw) {
    if (n.external) continue;
    const s = segs(n);
    if (!s.length) continue;
    if (common === null) { common = s.slice(); continue; }
    let i = 0;
    while (i < common.length && i < s.length && common[i] === s[i]) i++;
    common.length = i;
  }
  common = common || [];
  function folderOf(n) {
    if (n.external) return "external";
    const s = segs(n);
    if (!s.length) return "(root)";
    const depth = Math.min(s.length, common.length + 1);
    return s.slice(0, depth).join("/") + "/";
  }
  for (const n of raw) n.folder = folderOf(n);

  // ---- header --------------------------------------------------------------
  const when = data.last_indexed_at ? new Date(data.last_indexed_at * 1000).toLocaleString() : "never";
  const plural = (k, one, many) => `${k} ${k === 1 ? one : many}`;
  const fileCount = raw.reduce((s, n) => s + (n.external ? 0 : (n.files && n.files.length) || 1), 0);
  const pkgCount = raw.filter((n) => n.files && n.files.length).length;
  function renderStats() {
    let text = plural(fileCount, "indexed file", "indexed files");
    if (pkgCount) text += ` (${plural(pkgCount, "package", "packages")})`;
    text += ` · showing ${plural(nodes.length, "node", "nodes")}, ${plural(edges.length, "import", "imports")}`;
    const hiddenTests = showTests ? 0 : raw.filter((n) => n.testish).length;
    if (hiddenTests) text += ` · ${hiddenTests} test ${hiddenTests === 1 ? "node" : "nodes"} hidden`;
    text += ` · indexed ${when}`;
    if (data.focus) text += ` · around ${data.focus} (depth ${data.depth})`;
    $("stats").textContent = text;
  }
  if (data.truncated) {
    $("truncated").hidden = false;
    $("truncated").textContent = `the ${raw.length} most connected of ${data.total_nodes} nodes; use -focus to explore the rest`;
  }

  // ---- color: categorical, three validated slots, the rest folds to Other --
  const css = () => getComputedStyle(document.documentElement);
  let palette = {};
  function readPalette() {
    const s = css();
    const v = (k) => s.getPropertyValue(k).trim();
    palette = {
      surface: v("--surface"), ink: v("--ink"), ink2: v("--ink-2"), muted: v("--muted"),
      edge: v("--edge"), edgeHi: v("--edge-hi"), edgeIn: v("--edge-in"), other: v("--other"), focus: v("--focus"),
      series: [v("--series-1"), v("--series-2"), v("--series-3")],
    };
  }
  readPalette();

  const keyFns = {
    folder: (n) => n.external ? "external" : n.folder,
    language: (n) => n.external ? "external" : (n.language || "unknown"),
    facet: (n) => n.external ? "external" : ((n.facets && n.facets[0]) || "none"),
  };
  // Color by language only when the code is really split between languages:
  // a Go repository with two stray .js files colored by language is one flat
  // color, and its folders say more.
  const langCounts = new Map();
  for (const n of raw) {
    if (n.external || n.testish) continue;
    const l = n.language || "unknown";
    langCounts.set(l, (langCounts.get(l) || 0) + 1);
  }
  const codeNodes = [...langCounts.values()].reduce((s, c) => s + c, 0);
  const dominant = Math.max(0, ...langCounts.values());
  let colorBy = codeNodes && dominant / codeNodes < 0.8 ? "language" : "folder";
  $("color-by").value = colorBy;
  let categories = []; // [{key, label, count, slot}] slot -1 = Other, -2 = external
  let slotOf = new Map();
  let hiddenCats = new Set();

  // Slots are assigned once per "color by", from the code nodes, and never
  // reassigned by a toggle: a filter that changes what is shown must not
  // repaint what stays.
  function buildCategories() {
    const counts = new Map();
    for (const n of raw) {
      if (n.testish && !n.external) continue;
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
  // A display node's category is its first member's: a folder group shares
  // one folder, and a mixed-language group is labelled "mixed" in its panel.
  function catKey(n) {
    const k = keyFns[colorBy](n.members[0]);
    if (k === "external") return "external";
    return slotOf.has(k) ? k : "__other";
  }
  function fillOf(n) {
    const k = keyFns[colorBy](n.members[0]);
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
        if (!$("table-view").hidden) renderTable();
      });
      legend.append(b);
    }
  }

  // ---- display model: the raw graph after the test and folder toggles --------
  let showTests = false, grouped = false;
  const expanded = new Set(); // folders opened while grouped
  let nodes = [], edges = [], byId = new Map();
  const remembered = new Map(); // raw or group id -> {x, y}, kept across rebuilds

  function rebuild() {
    const keep = raw.filter((n) => showTests || !n.testish);
    const folderSize = new Map();
    for (const n of keep) folderSize.set(n.folder, (folderSize.get(n.folder) || 0) + 1);
    const members = new Map();
    const dispOf = new Map();
    for (const n of keep) {
      let id = n.id;
      if (grouped && !n.external && !expanded.has(n.folder) && folderSize.get(n.folder) > 1) id = "group:" + n.folder;
      dispOf.set(n, id);
      if (!members.has(id)) members.set(id, []);
      members.get(id).push(n);
    }

    nodes = [];
    for (const [id, ms] of members) {
      const first = ms[0];
      const isGroup = id.startsWith("group:");
      const langs = new Set(ms.map((m) => m.language || "unknown"));
      const facets = [...new Set(ms.flatMap((m) => m.facets || []))];
      nodes.push({
        id, members: ms, isGroup,
        kind: isGroup ? "folder" : first.external ? "external" : (first.files && first.files.length ? "package" : "file"),
        name: isGroup ? first.folder : first.name,
        folder: first.folder,
        external: !!first.external,
        language: langs.size === 1 ? [...langs][0] : "mixed",
        dir: first.dir,
        files: isGroup ? ms.flatMap((m) => (m.files && m.files.length ? m.files : [m.name])) : first.files,
        symbols: ms.reduce((s, m) => s + (m.symbols || 0), 0),
        facets,
        imports: [], importedBy: [], in: 0, out: 0,
        x: 0, y: 0, vx: 0, vy: 0, tx: 0, ty: 0, fixed: false,
      });
    }
    byId = new Map(nodes.map((n) => [n.id, n]));
    nodes.forEach((n, i) => { n.i = i; });

    const merged = new Map();
    edges = [];
    for (const e of rawEdges) {
      if (!showTests && e.test) continue;
      const a = dispOf.get(e.a), b = dispOf.get(e.b);
      if (a === undefined || b === undefined || a === b) continue;
      const k = a + "\n" + b;
      let de = merged.get(k);
      if (!de) {
        de = { a: byId.get(a), b: byId.get(b), count: 0 };
        merged.set(k, de); edges.push(de);
        de.a.imports.push(de.b); de.b.importedBy.push(de.a);
      }
      de.count++;
    }
    for (const n of nodes) {
      n.in = n.importedBy.length; n.out = n.imports.length;
      n.r = n.isGroup ? 6 + Math.min(16, 2.6 * Math.sqrt(n.members.length)) : 3 + Math.min(15, 2.2 * Math.sqrt(n.in));
    }

    // Start each node where it, or the group it came out of or went into,
    // was last seen, so a toggle moves the picture instead of redrawing it.
    for (const n of nodes) {
      let p = remembered.get(n.id);
      if (!p && n.isGroup) {
        const ps = n.members.map((m) => remembered.get(m.id)).filter(Boolean);
        if (ps.length) p = { x: ps.reduce((s, q) => s + q.x, 0) / ps.length, y: ps.reduce((s, q) => s + q.y, 0) / ps.length };
      }
      if (!p) p = remembered.get("group:" + n.folder);
      if (p) { n.x = p.x + (remembered.has(n.id) ? 0 : (hash(n.id) - 0.5) * 30); n.y = p.y + (remembered.has(n.id) ? 0 : (hash(n.id + "#") - 0.5) * 30); n.placed = true; }
    }

    hubs = new Set([...nodes].sort((a, b) => b.in - a.in).slice(0, 12).filter((n) => n.in > 0));
    if (selected) selected = byId.get(selected.id) || nodes.find((n) => n.members.some((m) => selected.members.includes(m))) || null;
    hovered = null;
    renderStats();
    relayout();
    renderPanel();
    if (!$("table-view").hidden) renderTable();
  }
  function remember() {
    for (const n of nodes) {
      remembered.set(n.id, { x: n.x, y: n.y });
      // A group's members inherit its place, and a member's place moves its group.
      if (n.isGroup) for (const m of n.members) remembered.set(m.id, { x: n.x, y: n.y });
    }
  }

  // ---- layout ----------------------------------------------------------------
  let layout = "layers";
  $("layout").value = layout;
  const hash = (s) => { let h = 2166136261; for (let i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 16777619); } return (h >>> 0) / 4294967296; };

  function relayout() {
    if (layout === "layers") { layoutLayers(); animateTo(); }
    else { seedMissing(); alpha = Math.max(alpha, 0.6); kick(); }
  }

  // Layers: importers above what they import. Cycles are collapsed first
  // (Tarjan), so every member of a cycle shares a layer; each node then sits
  // one layer below the deepest thing that imports it. Within a layer, nodes
  // are ordered by the average position of their neighbours, a few sweeps
  // down and up, which keeps crossings low without a full crossing minimizer.
  const XGAP = 74, YGAP = 120, SUB = 30;
  function layoutLayers() {
    const N = nodes.length;
    if (!N) return;
    // Tarjan's strongly connected components, iteratively.
    const index = new Array(N).fill(-1), low = new Array(N).fill(0), onStack = new Array(N).fill(false);
    const comp = new Array(N).fill(-1), stack = [];
    let counter = 0, comps = 0;
    for (let s = 0; s < N; s++) {
      if (index[s] >= 0) continue;
      const work = [[s, 0]];
      index[s] = low[s] = counter++; stack.push(s); onStack[s] = true;
      while (work.length) {
        const top = work[work.length - 1];
        const v = top[0], out = nodes[v].imports;
        if (top[1] < out.length) {
          const w = out[top[1]++].i;
          if (index[w] < 0) { index[w] = low[w] = counter++; stack.push(w); onStack[w] = true; work.push([w, 0]); }
          else if (onStack[w]) low[v] = Math.min(low[v], index[w]);
          continue;
        }
        if (low[v] === index[v]) {
          let w;
          do { w = stack.pop(); onStack[w] = false; comp[w] = comps; } while (w !== v);
          comps++;
        }
        work.pop();
        if (work.length) { const u = work[work.length - 1][0]; low[u] = Math.min(low[u], low[v]); }
      }
    }
    // Longest path from the top over the condensed graph, in topological order.
    const succ = Array.from({ length: comps }, () => new Set());
    const indeg = new Array(comps).fill(0);
    for (const e of edges) {
      const a = comp[e.a.i], b = comp[e.b.i];
      if (a !== b && !succ[a].has(b)) { succ[a].add(b); indeg[b]++; }
    }
    const level = new Array(comps).fill(0);
    const queue = [];
    for (let c = 0; c < comps; c++) if (!indeg[c]) queue.push(c);
    while (queue.length) {
      const c = queue.shift();
      for (const d of succ[c]) {
        level[d] = Math.max(level[d], level[c] + 1);
        if (--indeg[d] === 0) queue.push(d);
      }
    }
    // Nodes with no edges at all go to a row of their own at the bottom.
    let maxLevel = 0;
    for (const n of nodes) if (n.in || n.out) maxLevel = Math.max(maxLevel, level[comp[n.i]]);
    const rows = [];
    for (const n of nodes) {
      const l = n.in || n.out ? level[comp[n.i]] : maxLevel + 1;
      (rows[l] = rows[l] || []).push(n);
    }
    const layers = rows.filter(Boolean);
    for (const row of layers) row.sort((a, b) => (a.folder < b.folder ? -1 : a.folder > b.folder ? 1 : a.name < b.name ? -1 : 1));
    const place = (row) => row.forEach((n, k) => { n.tx = (k - (row.length - 1) / 2) * XGAP; });
    layers.forEach(place);
    const bary = (n, list) => list.length ? list.reduce((s, m) => s + m.tx, 0) / list.length : n.tx;
    for (let sweep = 0; sweep < 4; sweep++) {
      const order = sweep % 2 ? [...layers].reverse() : layers;
      for (const row of order) {
        const key = new Map(row.map((n) => [n, bary(n, sweep % 2 ? n.imports : n.importedBy)]));
        row.sort((a, b) => key.get(a) - key.get(b));
        place(row);
      }
    }
    // A very wide layer wraps into staggered sub-rows, so the picture keeps a
    // usable aspect ratio.
    const maxRow = Math.max(12, Math.ceil(Math.sqrt(N) * 2.5));
    let y = 0;
    for (const row of layers) {
      const chunks = Math.ceil(row.length / maxRow);
      const width = Math.ceil(row.length / chunks);
      row.forEach((n, k) => {
        const c = Math.floor(k / width), within = k % width;
        const len = Math.min(width, row.length - c * width);
        n.tx = (within - (len - 1) / 2) * XGAP + (c % 2 ? XGAP / 2 : 0);
        n.ty = y + c * SUB;
      });
      y += YGAP + (chunks - 1) * SUB;
    }
  }

  // Move to the layered positions over a short animation.
  let anim = null;
  function animateTo() {
    // The list is captured: a rebuild before the animation ends replaces
    // nodes, and this animation must keep moving only its own.
    const list = nodes;
    const from = list.map((n) => [n.placed ? n.x : n.tx, n.placed ? n.y : n.ty]);
    const t0 = performance.now(), dur = 380;
    if (anim) cancelAnimationFrame(anim);
    const step = (t) => {
      const u = Math.min(1, (t - t0) / dur), e = 1 - Math.pow(1 - u, 3);
      if (list !== nodes) return;
      list.forEach((n, k) => { n.x = from[k][0] + (n.tx - from[k][0]) * e; n.y = from[k][1] + (n.ty - from[k][1]) * e; n.placed = true; });
      draw();
      if (u < 1) anim = requestAnimationFrame(step);
      else { anim = null; remember(); }
    };
    anim = requestAnimationFrame(step);
  }

  // Force: grid-bucketed repulsion, springs along imports, a gentle pull to
  // the center. Unplaced nodes start grouped by folder.
  function seedMissing() {
    const folders = [...new Set(nodes.map((n) => n.folder))].sort();
    const R = 40 * Math.sqrt(nodes.length + 1);
    for (const n of nodes) {
      if (n.placed) continue;
      const a = (folders.indexOf(n.folder) / Math.max(1, folders.length)) * Math.PI * 2;
      n.x = Math.cos(a) * R * 0.6 + (hash(n.id) - 0.5) * R * 0.5;
      n.y = Math.sin(a) * R * 0.6 + (hash(n.id + "#") - 0.5) * R * 0.5;
      n.placed = true;
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
    for (const { a, b } of edges) {
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
  let running = false;
  function kick() {
    if (running || layout !== "force") return;
    running = true;
    const step = () => {
      if (layout !== "force") { running = false; return; }
      const t0 = performance.now();
      while (alpha > 0.02 && performance.now() - t0 < 12) tick();
      draw();
      if (alpha > 0.02) requestAnimationFrame(step); else { running = false; remember(); }
    };
    requestAnimationFrame(step);
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
  function fit(useTargets) {
    if (!nodes.length) return;
    let x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
    for (const n of nodes) {
      const x = useTargets ? n.tx : n.x, y = useTargets ? n.ty : n.y;
      x0 = Math.min(x0, x); y0 = Math.min(y0, y); x1 = Math.max(x1, x); y1 = Math.max(y1, y);
    }
    view.x = (x0 + x1) / 2; view.y = (y0 + y1) / 2;
    view.k = Math.min(2, 0.9 * Math.min(W / Math.max(40, x1 - x0 + 160), H / Math.max(40, y1 - y0 + 60)));
    draw();
  }
  function centerOn(n) { view.x = layout === "layers" ? n.tx : n.x; view.y = layout === "layers" ? n.ty : n.y; view.k = Math.max(view.k, 1.2); draw(); }

  // ---- state ---------------------------------------------------------------
  let selected = null, hovered = null, matches = new Set(), labels = true, hubs = new Set();
  const neighbors = (n) => new Set([n, ...n.imports, ...n.importedBy]);
  function visible(n) { return !hiddenCats.has(catKey(n)); }

  // ---- drawing ---------------------------------------------------------------
  function edgePath(a, b) {
    const [ax, ay] = toScreen(a), [bx, by] = toScreen(b);
    if (layout === "layers" && Math.abs(by - ay) > 1) {
      // A vertical S-curve: leaves the importer downward, arrives from above.
      const my = (ay + by) / 2;
      ctx.moveTo(ax, ay); ctx.bezierCurveTo(ax, my, bx, my, bx, by);
      return [bx, by, 0, Math.sign(by - ay) || 1, Math.hypot(bx - ax, by - ay)];
    }
    ctx.moveTo(ax, ay); ctx.lineTo(bx, by);
    const d = Math.hypot(bx - ax, by - ay) || 1;
    return [bx, by, (bx - ax) / d, (by - ay) / d, d];
  }
  function arrowhead(b, end, color) {
    const [bx, by, ux, uy, len] = end;
    if (len < 24) return;
    const back = radiusOf(b) + 3;
    const tx = bx - ux * back, ty = by - uy * back;
    ctx.fillStyle = color;
    ctx.beginPath(); ctx.moveTo(tx, ty);
    ctx.lineTo(tx - ux * 7 - uy * 3.5, ty - uy * 7 + ux * 3.5);
    ctx.lineTo(tx - ux * 7 + uy * 3.5, ty - uy * 7 - ux * 3.5);
    ctx.closePath(); ctx.fill();
  }
  const radiusOf = (n) => Math.max(2.5, n.r * Math.sqrt(view.k));

  function draw() {
    if (!W) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.fillStyle = palette.surface;
    ctx.fillRect(0, 0, W, H);
    // The focus is the selection, or the node under the pointer when nothing
    // is selected: hovering previews a neighbourhood, clicking keeps it.
    const focus = selected || hovered;
    const near = focus ? neighbors(focus) : null;
    const dimAlpha = selected ? 0.12 : 0.25;
    const lit = (n) => visible(n) && (!near || near.has(n)) && (!matches.size || matches.has(n) || (near && near.has(n)));
    const arrows = view.k >= 0.45;

    // Background edges: hairlines, with arrowheads once they are long enough
    // to carry one.
    ctx.lineWidth = 1;
    ctx.strokeStyle = palette.edge;
    const heads = [];
    ctx.beginPath();
    for (const e of edges) {
      const { a, b } = e;
      if (!visible(a) || !visible(b)) continue;
      if (focus && (a === focus || b === focus)) continue;
      if ((near || matches.size) && !(lit(a) && lit(b))) continue;
      const end = edgePath(a, b);
      if (arrows) heads.push([b, end]);
    }
    ctx.stroke();
    for (const [b, end] of heads) arrowhead(b, end, palette.edge);

    // The focus's edges: what it imports in ink, what imports it in the
    // accent, each with an arrowhead, so direction never depends on layout.
    if (focus) {
      ctx.lineWidth = 1.5;
      for (const e of edges) {
        const { a, b } = e;
        if (a !== focus && b !== focus) continue;
        if (!visible(a) || !visible(b)) continue;
        const color = a === focus ? palette.edgeHi : palette.edgeIn;
        ctx.strokeStyle = color;
        ctx.beginPath();
        const end = edgePath(a, b);
        ctx.stroke();
        arrowhead(b, end, color);
      }
    }

    // Nodes, with a 2px surface ring so overlapping marks stay separable. A
    // folder group is a rounded square, a file or package a circle, an
    // external package a hollow circle.
    for (const n of nodes) {
      if (!visible(n)) continue;
      const [x, y] = toScreen(n);
      const r = radiusOf(n);
      if (x < -r - 4 || y < -r - 4 || x > W + r + 4 || y > H + r + 4) continue;
      ctx.globalAlpha = lit(n) ? 1 : dimAlpha;
      shape(n, x, y, r + 2); ctx.fillStyle = palette.surface; ctx.fill();
      shape(n, x, y, r);
      if (n.external) { ctx.strokeStyle = palette.muted; ctx.lineWidth = 2; ctx.stroke(); }
      else { ctx.fillStyle = fillOf(n); ctx.fill(); }
      if (n === selected || n === hovered || (matches.size && matches.has(n))) {
        shape(n, x, y, r + 4);
        ctx.strokeStyle = palette.ink; ctx.lineWidth = 2; ctx.stroke();
      }
    }
    ctx.globalAlpha = 1;

    // Labels: selective, never on every node. Ink text on a surface halo.
    if (labels) {
      const want = new Set();
      if (near) near.forEach((n) => want.add(n));
      else if (matches.size) matches.forEach((n) => want.add(n));
      else {
        if (view.k > 0.5) hubs.forEach((n) => want.add(n));
        for (const n of nodes) if (n.isGroup) want.add(n);
      }
      ctx.font = "12px system-ui, -apple-system, 'Segoe UI', sans-serif";
      ctx.textBaseline = "middle";
      ctx.lineJoin = "round";
      for (const n of want) {
        if (!visible(n)) continue;
        const [x, y] = toScreen(n);
        const r = radiusOf(n);
        let text = n.name.length > 48 ? "…" + n.name.slice(-47) : n.name;
        if (n.isGroup) text += ` (${n.members.length})`;
        ctx.lineWidth = 4; ctx.strokeStyle = palette.surface; ctx.strokeText(text, x + r + 6, y);
        ctx.fillStyle = n === focus ? palette.ink : palette.ink2; ctx.fillText(text, x + r + 6, y);
      }
    }
  }
  function shape(n, x, y, r) {
    ctx.beginPath();
    if (!n.isGroup) { ctx.arc(x, y, r, 0, Math.PI * 2); return; }
    const c = Math.min(4, r / 2);
    ctx.moveTo(x - r + c, y - r);
    ctx.arcTo(x + r, y - r, x + r, y + r, c); ctx.arcTo(x + r, y + r, x - r, y + r, c);
    ctx.arcTo(x - r, y + r, x - r, y - r, c); ctx.arcTo(x - r, y - r, x + r, y - r, c);
    ctx.closePath();
  }

  // ---- side panel ----------------------------------------------------------
  function el(tag, text, cls) { const e = document.createElement(tag); if (text !== undefined) e.textContent = text; if (cls) e.className = cls; return e; }
  function nodeList(title, list, swatchVar) {
    const frag = document.createDocumentFragment();
    const h = el("h3");
    if (swatchVar) { const s = el("span", undefined, "line-swatch"); s.style.background = `var(${swatchVar})`; h.append(s); }
    h.append(document.createTextNode(`${title} (${list.length})`));
    frag.append(h);
    const ul = el("ul");
    for (const m of [...list].sort((a, b) => (a.name < b.name ? -1 : 1))) {
      const li = el("li");
      if (m.external) { li.append(el("div", m.name, "ext")); }
      else {
        const b = el("button", m.isGroup ? `${m.name} (${m.members.length})` : m.name); b.type = "button";
        b.addEventListener("click", () => { select(m); centerOn(m); });
        li.append(b);
      }
      ul.append(li);
    }
    if (!list.length) ul.append(el("li", "none", "hint"));
    frag.append(ul);
    return frag;
  }
  function action(label, fn) { const b = el("button", label, "action"); b.type = "button"; b.addEventListener("click", fn); return b; }
  function renderPanel() {
    const panel = $("panel");
    panel.replaceChildren();
    if (!selected) {
      panel.append(el("h2", "Dependency graph"),
        el("p", "Each arrow points from a file to what it imports. In layers, importers sit above what they import.", "hint"),
        el("p", "Hover a file to preview its neighbourhood; click to keep it. Drag to pan, scroll to zoom, drag a node to move it. Press / to search and Esc to clear.", "hint"),
        el("p", grouped ? "Folders are grouped: click one and choose Open, or double-click it." : "Group folders to see the shape of the repository first.", "hint"));
      return;
    }
    const n = selected;
    panel.append(el("h2", n.isGroup ? `${n.name} (folder)` : n.name));
    const dl = el("dl");
    const row = (k, v) => dl.append(el("dt", k), el("dd", v));
    if (n.external) row("kind", "external package (not indexed)");
    else {
      row("kind", n.isGroup ? `folder (${plural(n.members.length, "node", "nodes")}, ${plural(n.files.length, "file", "files")})`
        : n.kind === "package" ? `package (${n.files.length} files)` : "file");
      row("language", n.language || "unknown");
      if (!n.isGroup) row("directory", n.dir);
      row("symbols", String(n.symbols));
    }
    row("imported by", String(n.in));
    row("imports", String(n.out));
    if (n.facets && n.facets.length) row("roles", n.facets.join(", "));
    if (n.members.some((m) => m.testish)) row("tests", n.members.every((m) => m.testish) ? "test code only" : "includes test code");
    panel.append(dl);
    if (n.isGroup) panel.append(action("Open folder", () => setExpanded(n.folder, true)));
    else if (grouped && expanded.has(n.folder)) panel.append(action(`Close ${n.folder}`, () => setExpanded(n.folder, false)));
    panel.append(nodeList("Imports", n.imports, "--edge-hi"), nodeList("Imported by", n.importedBy, "--edge-in"));
    if (n.files && n.files.length) {
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
    try { history.replaceState(null, "", n && !n.isGroup ? "#" + encodeURIComponent(n.name) : location.pathname); } catch (_) { /* file:// in some browsers */ }
  }
  function setExpanded(folder, open) {
    remember();
    if (open) expanded.add(folder); else expanded.delete(folder);
    const keep = selected;
    rebuild();
    // After opening, select nothing in particular but keep the view on the folder.
    if (open && keep && keep.isGroup) select(null);
  }

  // ---- table view (the accessible, sortable alternative) ---------------------
  let sortKey = "in", sortDir = -1;
  function renderTable() {
    const body = $("table-body");
    body.replaceChildren();
    const rows = [...nodes].filter(visible).sort((a, b) => {
      const va = a[sortKey], vb = b[sortKey];
      return (va < vb ? -1 : va > vb ? 1 : 0) * sortDir || (a.name < b.name ? -1 : 1);
    });
    for (const n of rows) {
      const tr = el("tr");
      const c = el("td"); const b = el("button", n.isGroup ? `${n.name} (${n.members.length})` : n.name); b.type = "button";
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
    sortDir = sortKey === k ? -sortDir : (k === "name" || k === "language" ? 1 : -1);
    sortKey = k; renderTable();
  }));

  // ---- interaction -----------------------------------------------------------
  function hit(sx, sy) {
    const [wx, wy] = toWorld(sx, sy);
    let best = null, bd = Infinity;
    for (const n of nodes) {
      if (!visible(n)) continue;
      const d = Math.hypot(n.x - wx, n.y - wy);
      const reach = (radiusOf(n) + 6) / view.k; // target bigger than the mark
      if (d < reach && d < bd) { best = n; bd = d; }
    }
    return best;
  }
  function showTooltip(n, sx, sy) {
    if (!n) { tooltip.hidden = true; return; }
    const what = n.external ? "external package"
      : n.isGroup ? `folder · ${plural(n.members.length, "node", "nodes")}`
        : `${n.language || "unknown"} · ${n.symbols} symbols`;
    tooltip.replaceChildren(el("div", n.name), el("div", what, "sub"),
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
        drag.n.x = drag.n.tx = wx; drag.n.y = drag.n.ty = wy; drag.n.fixed = true;
        if (layout === "force") { alpha = Math.max(alpha, 0.15); kick(); } else draw();
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
    if (drag && drag.n && drag.moved) remember();
    drag = null;
  });
  canvas.addEventListener("dblclick", (e) => {
    const n = hit(e.offsetX, e.offsetY);
    if (n && n.isGroup) setExpanded(n.folder, true);
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
    // A package or folder matches by any of its files too, so searching for a
    // file name finds the node it is drawn in.
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
  const pressed = (id, on) => $(id).setAttribute("aria-pressed", String(on));
  $("color-by").addEventListener("change", (e) => { colorBy = e.target.value; buildCategories(); draw(); if (!$("table-view").hidden) renderTable(); });
  $("layout").addEventListener("change", (e) => {
    remember();
    layout = e.target.value;
    for (const n of nodes) n.fixed = false;
    relayout();
    setTimeout(() => fit(layout === "layers"), layout === "layers" ? 0 : 900);
  });
  $("toggle-tests").addEventListener("click", () => { remember(); showTests = !showTests; pressed("toggle-tests", showTests); rebuild(); });
  $("toggle-group").addEventListener("click", () => {
    remember(); grouped = !grouped; expanded.clear(); pressed("toggle-group", grouped); rebuild();
    if (layout === "layers") fit(true);
  });
  $("fit").addEventListener("click", () => fit(false));
  $("toggle-labels").addEventListener("click", (e) => { labels = !labels; e.currentTarget.setAttribute("aria-pressed", String(labels)); draw(); });
  $("toggle-table").addEventListener("click", () => toggleTable($("table-view").hidden));
  window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => { readPalette(); renderLegend(); draw(); });
  new ResizeObserver(resize).observe(canvas);

  // ---- run -------------------------------------------------------------------
  buildCategories();
  rebuild();
  if (layout === "layers") {
    // Start at the final layout: there is nothing to animate in from.
    if (anim) { cancelAnimationFrame(anim); anim = null; }
    for (const n of nodes) { n.x = n.tx; n.y = n.ty; n.placed = true; }
    remember();
  }
  resize();
  fit(layout === "layers");
  if (location.hash.length > 1) {
    const name = decodeURIComponent(location.hash.slice(1));
    let n = nodes.find((m) => m.name === name);
    if (!n) {
      // A test file, or a node inside a group: show what it takes to see it.
      const r = raw.find((m) => m.name === name);
      if (r && r.testish && !showTests) { showTests = true; pressed("toggle-tests", true); rebuild(); }
      n = nodes.find((m) => m.name === name) || null;
    }
    if (n) { select(n); touched = true; setTimeout(() => centerOn(n), 400); }
  }
  // In the force layout, refit once it has spread out unless the user moved the view.
  setTimeout(() => { if (!touched && layout === "force") fit(false); }, 1200);
})();
