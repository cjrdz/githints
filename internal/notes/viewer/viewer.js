"use strict";
// githints notes viewer. Self-contained and offline: no network, no libraries,
// and every string from a note reaches the page through textContent only,
// never as markup. Note bodies are shown as plain text on purpose: rendering
// markdown would mean building HTML from text another agent wrote.
(function () {
  const data = JSON.parse(document.getElementById("notes-data").textContent);
  const $ = (id) => document.getElementById(id);
  const notes = data.notes;
  const byName = new Map(notes.map((n) => [n.name, n]));
  const state = { query: "", tag: "", selected: "" };

  $("notice").textContent = data.notice;
  $("stats").textContent = notes.length + (notes.length === 1 ? " note" : " notes");

  // Which notes link to each note, from the links in their bodies.
  const linkedFrom = new Map();
  for (const n of notes) {
    for (const l of n.links) {
      const target = l.endsWith(".md") ? l.slice(0, -3) : l;
      if (target !== n.name && byName.has(target)) {
        if (!linkedFrom.has(target)) linkedFrom.set(target, []);
        linkedFrom.get(target).push(n.name);
      }
    }
  }

  const haystack = new Map(notes.map((n) => [n.name,
    [n.name, n.title, n.tags.join(" "), n.files.join(" "), n.body].join("\n").toLowerCase()]));

  function visible() {
    const words = state.query.toLowerCase().split(/\s+/).filter(Boolean);
    return notes.filter((n) =>
      (!state.tag || n.tags.includes(state.tag)) &&
      words.every((w) => haystack.get(n.name).includes(w)));
  }

  function el(tag, props, children) {
    const e = document.createElement(tag);
    if (props) for (const k of Object.keys(props)) {
      if (k === "text") e.textContent = props[k]; else e.setAttribute(k, props[k]);
    }
    for (const c of children || []) e.appendChild(c);
    return e;
  }

  function select(name) {
    state.selected = name;
    if (name) history.replaceState(null, "", "#" + encodeURIComponent(name));
    render();
  }

  function renderTags() {
    const counts = new Map();
    for (const n of notes) for (const t of n.tags) counts.set(t, (counts.get(t) || 0) + 1);
    const box = $("tags");
    box.replaceChildren();
    for (const t of [...counts.keys()].sort()) {
      const chip = el("button", { type: "button", class: "chip", text: t + " " + counts.get(t),
        "aria-pressed": String(state.tag === t) });
      chip.addEventListener("click", () => { state.tag = state.tag === t ? "" : t; render(); });
      box.appendChild(chip);
    }
    box.hidden = counts.size === 0;
  }

  function renderList(list) {
    const ul = $("list");
    ul.replaceChildren();
    if (list.length === 0) {
      ul.appendChild(el("li", { class: "empty", text: notes.length ? "No notes match." : "No notes yet." }));
      return;
    }
    for (const n of list) {
      const b = el("button", { type: "button", "aria-current": String(n.name === state.selected) }, [
        el("div", { text: n.title }),
        el("div", { class: "name", text: n.name }),
      ]);
      b.addEventListener("click", () => select(n.name));
      ul.appendChild(el("li", null, [b]));
    }
  }

  function meta(label, children) {
    return el("div", { class: "meta" }, [el("span", { class: "label", text: label + ": " }), ...children]);
  }

  function renderDetail() {
    const box = $("detail");
    box.replaceChildren();
    const n = byName.get(state.selected);
    if (!n) { box.appendChild(el("p", { class: "empty", text: "Select a note." })); return; }
    box.appendChild(el("h2", { text: n.title }));
    box.appendChild(meta("name", [document.createTextNode(n.name)]));
    box.appendChild(meta("updated", [document.createTextNode(new Date(n.updated * 1000).toISOString().slice(0, 10))]));
    if (n.tags.length) box.appendChild(meta("tags", [document.createTextNode(n.tags.join(", "))]));
    if (n.files.length) box.appendChild(meta("related files", [document.createTextNode(n.files.join(", "))]));
    if (n.changes.length) box.appendChild(meta("related changes", [document.createTextNode(n.changes.map((c) => "#" + c).join(", "))]));
    const from = linkedFrom.get(n.name) || [];
    if (from.length) {
      box.appendChild(meta("linked from", from.flatMap((f, i) => {
        const b = el("button", { type: "button", class: "link", text: f });
        b.addEventListener("click", () => select(f));
        return i ? [document.createTextNode(", "), b] : [b];
      })));
    }
    if (n.links.length) {
      box.appendChild(meta("links", n.links.flatMap((l, i) => {
        const target = l.endsWith(".md") ? l.slice(0, -3) : l;
        let node;
        if (byName.has(target)) {
          node = el("button", { type: "button", class: "link", text: l });
          node.addEventListener("click", () => select(target));
        } else {
          node = document.createTextNode(l);
        }
        return i ? [document.createTextNode(", "), node] : [node];
      })));
    }
    box.appendChild(el("pre", { text: n.omitted ? "[body left out: the page size limit was reached; run `githints note get " + n.name + "`]" : n.body }));
  }

  function render() {
    const list = visible();
    renderTags();
    renderList(list);
    renderDetail();
  }

  $("search").addEventListener("input", (e) => { state.query = e.target.value; render(); });
  document.addEventListener("keydown", (e) => {
    if (e.key === "/" && document.activeElement !== $("search")) { e.preventDefault(); $("search").focus(); }
  });

  const start = decodeURIComponent(location.hash.slice(1));
  state.selected = byName.has(start) ? start : (notes[0] ? notes[0].name : "");
  render();
})();
