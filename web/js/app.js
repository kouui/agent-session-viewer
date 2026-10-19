"use strict";
const $ = s => document.querySelector(s);
function h(tag, attrs, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (k === "class") e.className = v; else if (k.startsWith("on")) e.addEventListener(k.slice(2), v);
    else if (v === true) e.setAttribute(k, ""); else if (v !== false && v != null) e.setAttribute(k, v);
  }
  for (const c of kids.flat()) if (c != null) e.append(c.nodeType ? c : document.createTextNode(String(c)));
  return e;
}
const fmtAbs = t => t ? new Date(t).toLocaleString() : "";
function fmtRel(t) {
  if (!t) return "";
  const s = Math.max(0, (Date.now() - t) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return Math.floor(s / 60) + "m ago";
  if (s < 86400) return Math.floor(s / 3600) + "h ago";
  if (s < 86400 * 30) return Math.floor(s / 86400) + "d ago";
  return new Date(t).toLocaleDateString();
}
const fmtTok = n => n >= 1e9 ? (n / 1e9).toFixed(1) + "B" : n >= 1e6 ? (n / 1e6).toFixed(1) + "M" : n >= 1e3 ? (n / 1e3).toFixed(1) + "k" : String(n || 0);
const skey = (src, id) => src + "\u0000" + id;

// Which agents count in usage plots (Costs page, mini/bar stats). Persisted; default: all on.
const Agents = {
  off: new Set(JSON.parse(localStorage.getItem("av.agents.off") || "[]")),
  on(kind) { return !this.off.has(kind); },
  toggle(kind) {
    if (this.off.has(kind)) this.off.delete(kind); else this.off.add(kind);
    localStorage.setItem("av.agents.off", JSON.stringify([...this.off]));
  },
};

/* =================== theme =================== */
const THEMES = ["auto", "light", "dark"], ICON = { auto: "◐ auto", light: "☀ day", dark: "☾ night" };
let theme = localStorage.getItem("av.theme") || "auto";
const mq = matchMedia("(prefers-color-scheme: light)");
function applyTheme() {
  const t = theme === "auto" ? (mq.matches ? "light" : "dark") : theme;
  document.documentElement.dataset.theme = t;
  $("#theme").textContent = ICON[theme];
}
mq.addEventListener("change", applyTheme);
$("#theme").addEventListener("click", () => {
  theme = THEMES[(THEMES.indexOf(theme) + 1) % THEMES.length];
  localStorage.setItem("av.theme", theme); applyTheme();
});
applyTheme();

/* =================== font size =================== */
let fontSize = +localStorage.getItem("av.fs") || 14;
function applyFont(d) {
  fontSize = Math.min(24, Math.max(10, fontSize + d));
  document.documentElement.style.setProperty("--fs", fontSize + "px");
  localStorage.setItem("av.fs", fontSize);
  $("#fsm").title = $("#fsp").title = "font size " + fontSize + "px";
}
$("#fsm").addEventListener("click", () => applyFont(-1));
$("#fsp").addEventListener("click", () => applyFont(1));
applyFont(0);

/* =================== data =================== */
let projects = [], sources = [];
const sessIndex = new Map(); // skey -> {p, s}
const expanded = new Set(JSON.parse(localStorage.getItem("av.expanded") || "[]"));
let firstLoad = true;

async function refreshProjects() {
  let r;
  try { r = await (await fetch("/api/projects")).json(); } catch { return; }
  projects = r.projects; sources = r.sources;
  sessIndex.clear();
  for (const p of projects) for (const s of p.sessions) sessIndex.set(skey(s.source, s.id), { p, s });
  if (firstLoad) { firstLoad = false; if (!expanded.size && projects[0]) expanded.add(projects[0].key); }
  renderSidebar();
  for (const pane of panes) pane.renderTabs();
  for (const v of allViews()) v.renderHeader();
}

/* =================== sidebar =================== */
function openKeys() { return new Set(allViews().map(v => skey(v.src, v.id))); }
// subagent display: "nested" folds subagents (and workflow runs) under their parent session, "flat" lists everything by time
let subMode = localStorage.getItem("av.submode") || "nested";
const subOpen = new Set(JSON.parse(localStorage.getItem("av.subopen") || "[]"));
function toggleSub(k) {
  subOpen.has(k) ? subOpen.delete(k) : subOpen.add(k);
  localStorage.setItem("av.subopen", JSON.stringify([...subOpen]));
  renderSidebar();
}
function sessRow(s, cls, opened, act) {
  const k = skey(s.source, s.id);
  return h("div", {
    class: "sess" + (act && skey(act.src, act.id) === k ? " sel" : "") + (opened.has(k) ? " open" : "") + cls,
    title: s.directory,
    onclick: e => openSession(s.source, s.id, e.ctrlKey || e.shiftKey || e.metaKey),
  },
    h("div", { class: "t" }, (s.running ? "● " : "") + (s.title || "(untitled)")),
    h("div", { class: "m" }, h("span", { class: "badge " + s.kind }, s.kind), fmtRel(s.mtime),
      s.parentId ? h("span", { class: "badge" }, "sub") : null,
      s.archived ? h("span", { class: "badge" }, "archived") : null),
    h("button", { class: "split", title: "open in new pane", onclick: e => { e.stopPropagation(); openSession(s.source, s.id, true); } }, "split"));
}
function appendNested(box, ss, opened, act) {
  const ids = new Map(ss.map(s => [skey(s.source, s.id), s]));
  const rootOf = s => { // top-most ancestor present in this project
    for (let i = 0; i < 8 && s.parentId; i++) { const p = ids.get(skey(s.source, s.parentId)); if (!p) break; s = p; }
    return s;
  };
  const kids = new Map(), tops = [];
  for (const s of ss) {
    const r = rootOf(s);
    if (r === s) tops.push(s); else (kids.get(skey(r.source, r.id)) || kids.set(skey(r.source, r.id), []).get(skey(r.source, r.id))).push(s);
  }
  const shown = c => (act && act.src === c.source && act.id === c.id) || opened.has(skey(c.source, c.id)); // keep the selected/open session visible
  const fold = (key, label, lvl, open) => h("div", { class: "sfold l" + lvl, "data-k": key, onclick: () => toggleSub(key) }, (open ? "▾ " : "▸ ") + label);
  const runs = l => { const n = l.filter(c => c.running).length; return n ? " · ● " + n + " running" : ""; };
  for (const s of tops) {
    box.append(sessRow(s, s.parentId ? " sub" : "", opened, act));
    const ch = kids.get(skey(s.source, s.id));
    if (!ch) continue;
    const key = skey(s.source, s.id), open = subOpen.has(key) || ch.some(shown);
    box.append(fold(key, "subagents · " + ch.length + runs(ch), 1, open));
    if (!open) continue;
    const groups = new Map();
    for (const c of ch) {
      if (!c.group) { box.append(sessRow(c, " sub", opened, act)); continue; }
      (groups.get(c.group) || groups.set(c.group, []).get(c.group)).push(c);
    }
    for (const [g, list] of groups) {
      const gk = key + "|" + g, gopen = subOpen.has(gk) || list.some(shown);
      box.append(fold(gk, "⚙ " + (list[0].groupName || g) + " · " + list.length + " agents" + runs(list), 2, gopen));
      if (gopen) for (const c of list) box.append(sessRow(c, " sub2", opened, act));
    }
  }
}
// expand the sidebar so the agents of one workflow run (launched from `view`) are visible
function revealGroup(view, group) {
  const top = view.id.split("~")[0], f = sessIndex.get(skey(view.src, top));
  if (!f) return;
  subMode = "nested"; localStorage.setItem("av.submode", subMode);
  document.querySelectorAll("#submode [data-sm]").forEach(x => x.classList.toggle("on", x.dataset.sm === subMode));
  $("#filter").value = "";
  expanded.add(f.p.key); localStorage.setItem("av.expanded", JSON.stringify([...expanded]));
  const key = skey(view.src, top);
  subOpen.add(key); subOpen.add(key + "|" + group);
  localStorage.setItem("av.subopen", JSON.stringify([...subOpen]));
  renderSidebar();
  document.querySelector(`.sfold[data-k="${CSS.escape(key + "|" + group)}"]`)?.scrollIntoView({ block: "center" });
}
document.querySelectorAll("#submode [data-sm]").forEach(b => {
  b.classList.toggle("on", b.dataset.sm === subMode);
  b.addEventListener("click", () => {
    subMode = b.dataset.sm; localStorage.setItem("av.submode", subMode);
    document.querySelectorAll("#submode [data-sm]").forEach(x => x.classList.toggle("on", x.dataset.sm === subMode));
    renderSidebar();
  });
});
function renderSidebar() {
  const q = $("#filter").value.trim().toLowerCase();
  const list = $("#list"), top = list.scrollTop;
  const opened = openKeys(), act = activeView();
  const frag = document.createDocumentFragment();
  for (const p of projects) {
    const pm = (p.name + " " + p.path).toLowerCase().includes(q);
    const ss = q && !pm ? p.sessions.filter(s => s.title.toLowerCase().includes(q)) : p.sessions;
    if (!ss.length) continue;
    const open = q ? true : expanded.has(p.key);
    const head = h("div", { class: "ph", title: p.path, onclick: () => {
      expanded.has(p.key) ? expanded.delete(p.key) : expanded.add(p.key);
      localStorage.setItem("av.expanded", JSON.stringify([...expanded]));
      renderSidebar();
    } },
      h("span", { class: "arrow" }, open ? "▾" : "▸"),
      h("span", { class: "pn" }, p.name),
      h("span", { class: "pc" }, ss.length + " · " + fmtRel(p.mtime)));
    const box = h("div", { class: "proj" }, head);
    if (open) {
      if (subMode === "nested" && !q) appendNested(box, ss, opened, act);
      else for (const s of ss) box.append(sessRow(s, s.parentId ? " sub" : "", opened, act));
    }
    frag.append(box);
  }
  list.replaceChildren(frag);
  list.scrollTop = top;
  const stale = sources.filter(s => s.stale).map(s => s.label);
  $("#srcwarn").textContent = stale.length ? "⚠ " + stale.join(", ") + ": opened immutable — recent (WAL) writes not visible." : "";
  $("#srcerr").textContent = sources.filter(s => s.error).map(s => s.label + ": " + s.error).join("\n");
}
$("#filter").addEventListener("input", renderSidebar);

/* =================== session view =================== */
class View {
  constructor(src, id) {
    this.src = src; this.id = id; this.cursor = ""; this.msgEls = new Map(); this.partEls = new Map(); this.partData = new Map();
    this.syncing = false; this.again = false; this.dirty = true; this.visible = false;
    this.title = h("div", { class: "title" }); this.meta = h("div", { class: "meta" });
    this.statsEl = h("div", { class: "stats" });
    this.vh = h("div", { class: "vh" }, this.title, this.meta, this.statsEl);
    this.msgs = h("div", { class: "msgs" });
    this.el = h("div", { class: "view", hidden: true }, this.vh, this.msgs);
    this.first = true;
    this.renderHeader();
  }
  info() { return sessIndex.get(skey(this.src, this.id)); }
  renderHeader() {
    const f = this.info();
    if (!f) { this.title.textContent = this.id; this.meta.textContent = this.src; return; }
    const { p, s } = f;
    this.title.textContent = s.title || "(untitled)";
    let m = `${s.kind} · ${p.name} · ${s.directory || p.path} · created ${fmtAbs(s.timeCreated)} · updated ${fmtAbs(s.mtime)}`;
    this.meta.textContent = m;
    this.renderStats();
  }
  async loadStats() {
    if (this.statsBusy) { this.statsAgain = true; return; }
    this.statsBusy = true;
    try {
      const r = await fetch(`/api/stats?src=${encodeURIComponent(this.src)}&id=${encodeURIComponent(this.id)}`);
      if (r.ok) { this.stats = await r.json(); this.renderStats(); }
    } catch {} finally {
      this.statsBusy = false;
      if (this.statsAgain) { this.statsAgain = false; this.loadStats(); }
    }
  }
  renderStats() {
    const st = this.stats;
    if (!st) { this.statsEl.replaceChildren(); return; }
    const u = st.usage || {}, input = (u.input || 0) + (u.cacheRead || 0) + (u.cacheWrite || 0);
    const chip = (k, v, tip) => h("span", { class: "chip", title: tip || "" }, h("span", { class: "k" }, k), " ", v);
    const models = st.firstModel && st.lastModel && st.firstModel !== st.lastModel ? `${st.firstModel} → ${st.lastModel}` : (st.lastModel || st.firstModel || "?");
    const chips = [
      chip("model", models, "first → latest model in this session"),
      chip("in", fmtTok(u.input), "new (uncached) input; total input = in + cache read + cache write"),
      chip("cache read", fmtTok(u.cacheRead)),
      chip("cache write", fmtTok(u.cacheWrite)),
      chip("out", fmtTok(u.output), u.reasoning ? `includes/plus reasoning ${u.reasoning}` : ""),
      chip("hit", input ? (100 * (u.cacheRead || 0) / input).toFixed(1) + "%" : "–", "cache read / (in + cache read + cache write)"),
    ];
    if (st.contextUsed) chips.push(chip("ctx", fmtTok(st.contextUsed) + (st.contextWindow ? ` / ${fmtTok(st.contextWindow)} (${(100 * st.contextUsed / st.contextWindow).toFixed(0)}%)` : ""), "prompt size of the latest request / context window"));
    chips.push(chip("price", st.cost == null ? "n/a" : "$" + st.cost.toFixed(4), st.cost == null ? "this agent does not record price" : ""));
    this.statsEl.replaceChildren(...chips);
  }
  setVisible(v) {
    this.visible = v; this.el.hidden = !v;
    if (v && this.dirty) this.sync();
  }
  markDirty() { this.dirty = true; if (this.visible) this.sync(); }
  async sync() {
    if (this.syncing) { this.again = true; return; }
    this.syncing = true; this.dirty = false;
    try {
      for (;;) {
        const r = await fetch(`/api/session?src=${encodeURIComponent(this.src)}&id=${encodeURIComponent(this.id)}&cursor=${encodeURIComponent(this.cursor)}`);
        if (!r.ok) throw new Error(await r.text());
        const d = await r.json();
        if (this.closed) return;
        const stick = this.first || this.msgs.scrollHeight - this.msgs.scrollTop - this.msgs.clientHeight < 80;
        this.apply(d);
        if (stick) this.msgs.scrollTop = this.msgs.scrollHeight;
        this.first = false;
        if (!d.more) break;
      }
      this.loadStats();
    } catch (e) {
      this.msgs.replaceChildren(h("div", { class: "empty" }, "Failed to load: " + e.message));
    } finally {
      this.syncing = false;
      if (this.again) { this.again = false; this.sync(); }
    }
  }
  apply(d) {
    if (d.reset) { this.msgs.replaceChildren(); this.msgEls.clear(); this.partEls.clear(); this.partData.clear(); }
    this.cursor = d.cursor;
    for (const m of d.messages) {
      let el = null;
      for (const p of m.parts || []) {
        const old = this.partEls.get(p.id);
        if (old) { this.upsertPart(old, p); continue; }
        if (!el) el = this.ensureMsg(m);
        this.upsertPart(null, p, el);
      }
      if (m.role) this.ensureMsg(m);
    }
    this.msgs.querySelector(".empty")?.remove();
    if (!this.msgEls.size) this.msgs.append(h("div", { class: "empty" }, "No messages"));
  }
  upsertPart(old, p, msgEl) {
    // shallow merge: absent fields keep previous values
    const prev = this.partData.get(p.id);
    const data = prev ? Object.assign(prev, p) : p;
    this.partData.set(p.id, data);
    const el = renderPart(data, this);
    if (old) {
      const od = old.querySelector("details"), nd = el.querySelector("details");
      if (od && nd && od.open) nd.open = true;
      el.dataset.k = old.dataset.k;
      old.replaceWith(el);
    } else insertSorted(msgEl.querySelector(".parts"), el, p.id);
    this.partEls.set(p.id, el);
  }
  ensureMsg(m) {
    let el = this.msgEls.get(m.id);
    const key = String(m.time || 9e15).padStart(16, "0") + m.id;
    if (!el) {
      el = h("div", { class: "msg" }, h("div", { class: "mhd" }), h("div", { class: "parts" }));
      this.msgEls.set(m.id, el);
      insertSorted(this.msgs, el, key);
    } else if (m.time && el.dataset.k !== key) { el.dataset.k = key; }
    if (m.role) {
      el.dataset.role = m.role;
      el.firstChild.replaceChildren(...[h("span", { class: "role" }, m.role),
        m.agent ? h("span", {}, m.agent) : null, m.model ? h("span", {}, m.model) : null,
        m.finish ? h("span", {}, "finish: " + m.finish) : null,
        m.error ? h("span", { style: "color:var(--err)" }, "error: " + m.error) : null,
        h("span", { class: "when" }, fmtAbs(m.time))].filter(Boolean));
    }
    this.fixCont(el); if (el.nextElementSibling) this.fixCont(el.nextElementSibling);
    return el;
  }
  // consecutive assistant messages render as one visual card
  fixCont(el) {
    const prev = el.previousElementSibling;
    el.className = "msg " + (el.dataset.role || "") + (prev && prev.dataset.role === "assistant" && el.dataset.role === "assistant" ? " cont" : "");
  }
  close() { this.closed = true; this.el.remove(); }
}
function insertSorted(parent, el, key) {
  el.dataset.k = key;
  for (let c = parent.lastElementChild; c; c = c.previousElementSibling) {
    if (c.dataset.k <= key) { c.after(el); return; }
  }
  parent.prepend(el);
}

/* ---- part rendering ---- */
const CAP = 20000;
function pre(v) {
  const text = typeof v === "string" ? v : JSON.stringify(v, null, 2);
  return h("pre", {}, text.length > CAP ? text.slice(0, CAP) + `\n… [${text.length - CAP} more chars truncated]` : text);
}
function toolHint(i) {
  if (!i) return "";
  if (typeof i === "string") return i.replace(/\s+/g, " ").slice(0, 120);
  const v = i.command || i.cmd || i.filePath || i.path || i.pattern || i.url || i.query || i.description || "";
  return String(Array.isArray(v) ? v.join(" ") : v).replace(/\s+/g, " ").slice(0, 120);
}
function renderPart(p, view) {
  switch (p.kind) {
    case "text":
      return h("div", { class: "part text" }, h("div", { class: "body" }, p.text || ""));
    case "reasoning":
      return h("div", { class: "part reason" }, h("details", { class: "fold" },
        h("summary", {}, "thinking", h("span", { class: "ttl" }, (p.text || "").slice(0, 100).replace(/\s+/g, " "))),
        h("div", { class: "inner" }, p.text || "")));
    case "tool": {
      const inner = h("div", { class: "inner" });
      if (p.input !== undefined) inner.append(h("div", { class: "lbl" }, "input"), pre(p.input));
      if (p.output) inner.append(h("div", { class: "lbl" }, "output"), pre(p.output));
      if (p.error) inner.append(h("div", { class: "lbl" }, "error"), pre(p.error));
      return h("div", { class: "part tool" }, h("details", { class: "fold", open: p.status === "running" },
        h("summary", {}, h("span", { class: "name" }, p.tool || "tool"), h("span", { class: "ttl" }, p.title || toolHint(p.input)),
          h("span", { class: "st " + (p.status || "") }, p.status || "?"),
          p.link ? h("button", { class: "jump", title: p.link.group ? "show this workflow's agents in the sidebar" : "open the subagent session (shift/ctrl: new pane)", onclick: e => {
            e.preventDefault(); e.stopPropagation();
            if (p.link.group) revealGroup(view, p.link.group); else openSession(view.src, p.link.sessionId, e.shiftKey || e.ctrlKey || e.metaKey);
          } }, p.link.group ? "agents ↗" : "open ↗") : "",
          p.durationMs ? (p.durationMs / 1000).toFixed(1) + "s" : ""), inner));
    }
    case "patch":
      return h("div", { class: "part" }, h("details", { class: "fold" },
        h("summary", {}, h("span", { class: "name" }, "patch"), h("span", { class: "ttl" }, (p.files || []).length + " file(s)")),
        h("div", { class: "inner files" }, (p.files || []).join("\n"))));
    case "step": {
      const u = p.usage || {};
      return h("div", { class: "fin" }, `step ${p.text || ""} · in ${fmtTok(u.input)} out ${fmtTok(u.output)}` +
        (u.reasoning ? ` reason ${fmtTok(u.reasoning)}` : "") + (u.cacheRead || u.cacheWrite ? ` cache r${fmtTok(u.cacheRead)}/w${fmtTok(u.cacheWrite)}` : "") +
        (p.cost ? ` · $${p.cost.toFixed(4)}` : ""));
    }
    default:
      return h("div", { class: "part" }, h("details", { class: "fold" },
        h("summary", {}, h("span", { class: "name" }, p.tool || p.kind || "?")), h("div", { class: "inner" }, pre(p.raw !== undefined ? p.raw : p))));
  }
}

/* =================== panes / tabs / layout =================== */
class Pane {
  constructor() {
    this.tabs = []; // Views
    this.active = null;
    this.tabsEl = h("div", { class: "tabs" });
    this.viewsEl = h("div", { class: "views" });
    this.el = h("div", { class: "pane", onmousedown: () => focusPane(this) }, this.tabsEl, this.viewsEl);
  }
  add(view) {
    this.tabs.push(view); this.viewsEl.append(view.el); this.activate(view);
  }
  activate(view) {
    this.active = view;
    for (const v of this.tabs) v.setVisible(v === view);
    this.renderTabs();
  }
  closeTab(view) {
    const i = this.tabs.indexOf(view);
    if (i < 0) return;
    this.tabs.splice(i, 1); view.close();
    if (this.active === view) { this.active = null; if (this.tabs.length) this.activate(this.tabs[Math.min(i, this.tabs.length - 1)]); }
    if (!this.tabs.length) removePane(this); else this.renderTabs();
    afterChange();
  }
  renderTabs() {
    this.tabsEl.replaceChildren(...[...this.tabs.map(v => {
      const f = v.info();
      return h("div", { class: "tab" + (v === this.active ? " act" : ""), title: f ? f.s.title : v.id,
        onclick: () => { this.activate(v); focusPane(this); afterChange(); } },
        f ? h("span", { class: "badge " + f.s.kind }, f.s.kind) : null,
        h("span", { class: "tt" }, f ? (f.s.title || "(untitled)") : v.id),
        h("span", { class: "x", title: "close tab", onclick: e => { e.stopPropagation(); this.closeTab(v); } }, "×"));
    }), h("span", { class: "fill" }),
      this.tabs.length > 1 ? h("button", { class: "pbtn", title: "move active tab into a new pane", onclick: e => { e.stopPropagation(); splitOut(this); } }, "split out") : null].filter(Boolean));
  }
}
let panes = [], focused = null, layout = localStorage.getItem("av.layout") || "cols";
const allViews = () => panes.flatMap(p => p.tabs);
const activeView = () => focused && focused.active;

function focusPane(p) {
  focused = p;
  for (const x of panes) x.el.classList.toggle("focus", x === p);
  renderSidebar();
}
function removePane(p) {
  panes = panes.filter(x => x !== p); p.el.remove();
  if (focused === p) focused = panes[panes.length - 1] || null;
  layoutPanes();
}
function newPane() {
  const p = new Pane(); panes.push(p); $("#ws").append(p.el); layoutPanes(); return p;
}
function splitOut(pane) {
  const v = pane.active; if (!v || pane.tabs.length < 2) return;
  pane.tabs.splice(pane.tabs.indexOf(v), 1);
  pane.active = null; pane.activate(pane.tabs[pane.tabs.length - 1]);
  const np = newPane(); np.tabs.push(v); np.viewsEl.append(v.el); np.activate(v);
  focusPane(np); afterChange();
}
function layoutPanes() {
  const ws = $("#ws"), n = panes.length;
  ws.classList.toggle("empty-ws", !n);
  if (!n) { ws.style.gridTemplate = ""; ws.replaceChildren("Select a session in the sidebar"); return; }
  if (ws.firstChild && ws.firstChild.nodeType === 3) ws.firstChild.remove();
  let cols = n, rows = 1;
  if (layout === "rows") { cols = 1; rows = n; }
  else if (layout === "grid") { cols = Math.ceil(Math.sqrt(n)); rows = Math.ceil(n / cols); }
  ws.style.gridTemplateColumns = `repeat(${cols}, minmax(0, 1fr))`;
  ws.style.gridTemplateRows = `repeat(${rows}, minmax(0, 1fr))`;
  document.querySelectorAll("#bar [data-layout]").forEach(b => b.classList.toggle("on", b.dataset.layout === layout));
  $("#count").textContent = n + " pane" + (n > 1 ? "s" : "") + " · " + allViews().length + " session" + (allViews().length > 1 ? "s" : "");
}
document.querySelectorAll("#bar [data-layout]").forEach(b => b.addEventListener("click", () => {
  layout = b.dataset.layout; localStorage.setItem("av.layout", layout); layoutPanes();
}));

function openSession(src, id, inNewPane) {
  if (mode !== "sessions") setMode("sessions");
  // already open somewhere? just focus it
  for (const p of panes) for (const v of p.tabs) if (v.src === src && v.id === id) {
    p.activate(v); focusPane(p); afterChange(); return;
  }
  let pane = inNewPane || !focused ? newPane() : focused;
  const v = new View(src, id);
  pane.add(v); focusPane(pane); afterChange();
}
function afterChange() {
  saveState(); renderSidebar(); layoutPanes();
  const a = activeView();
  if (a) location.hash = encodeURIComponent(a.src) + "/" + encodeURIComponent(a.id);
}
function saveState() {
  localStorage.setItem("av.panes", JSON.stringify(panes.map(p => ({
    tabs: p.tabs.map(v => [v.src, v.id]), active: p.active ? p.tabs.indexOf(p.active) : 0, focus: p === focused }))));
}
function restoreState() {
  try {
    const st = JSON.parse(localStorage.getItem("av.panes") || "[]");
    for (const ps of st) {
      if (!ps.tabs.length) continue;
      const p = newPane();
      ps.tabs.forEach(([s, i]) => p.tabs.push(new View(s, i)));
      p.tabs.forEach(v => p.viewsEl.append(v.el));
      p.activate(p.tabs[Math.min(ps.active || 0, p.tabs.length - 1)]);
      if (ps.focus) focused = p;
    }
  } catch {}
  if (!focused && panes.length) focused = panes[0];
  if (focused) focusPane(focused);
}

/* =================== window modes: full / mini / bar =================== */
let uiMode = "full";
// In the native window Go switches the window geometry and then calls setUIMode;
// in a browser (#mini / #bar, for development) the view just changes in place.
function requestMode(m) { if (typeof window.avSetMode === "function") window.avSetMode(m); else setUIMode(m); }
function setUIMode(m) {
  uiMode = m;
  document.body.classList.toggle("mini", m === "mini"); document.body.classList.toggle("bar", m === "bar");
  if (m === "full") { Stats.hide(); applyFont(0); renderSidebar(); }
  else { document.documentElement.style.setProperty("--fs", "13px"); Stats.show(m); } // compact windows ignore the font setting
}
$("#modectl").hidden = typeof window.avSetMode !== "function"; // only inside the native window
$("#m-mini").addEventListener("click", () => requestMode("mini"));
$("#m-bar").addEventListener("click", () => requestMode("bar"));

/* =================== mode: sessions / costs =================== */
let mode = "sessions";
function setMode(m) {
  mode = m;
  $("#ws").hidden = m !== "sessions"; $("#cost").hidden = m !== "costs"; $("#layoutctl").hidden = m !== "sessions";
  $("#count").hidden = m !== "sessions";
  $("#m-sessions").classList.toggle("on", m === "sessions"); $("#m-costs").classList.toggle("on", m === "costs");
  if (m === "costs") { location.hash = "costs"; Cost.show(); }
  else { const a = activeView(); location.hash = a ? encodeURIComponent(a.src) + "/" + encodeURIComponent(a.id) : ""; }
}
$("#m-sessions").addEventListener("click", () => setMode("sessions"));
$("#m-costs").addEventListener("click", () => setMode("costs"));

/* =================== realtime =================== */
let timer = null;
function onChange(ev) {
  let d = {}; try { d = JSON.parse(ev.data); } catch {}
  clearTimeout(timer);
  timer = setTimeout(() => {
    refreshProjects();
    Cost.onChange(); Stats.onChange();
    for (const v of allViews()) if (!d.src || d.src === v.src) v.markDirty();
  }, 250);
}
function connect() {
  const es = new EventSource("/api/events");
  es.onopen = () => { $("#live").classList.add("on"); refreshProjects(); allViews().forEach(v => v.markDirty()); };
  es.onerror = () => $("#live").classList.remove("on");
  es.addEventListener("change", onChange);
}
setInterval(renderSidebar, 30000); // refresh relative times

(async function init() {
  restoreState();
  layoutPanes();
  await refreshProjects();
  const m = location.hash.slice(1).split("/");
  if (location.hash === "#mini" || location.hash === "#bar") setUIMode(location.hash.slice(1));
  else if (location.hash === "#costs") setMode("costs");
  else if (m.length === 2 && m[0] && m[1]) openSession(decodeURIComponent(m[0]), decodeURIComponent(m[1]), false);
  connect();
})();
