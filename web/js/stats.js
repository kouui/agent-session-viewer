"use strict";
// Compact stats pages for the mini and bar window modes. Relies on $, h, fmtTok,
// fmtRel from index.html. Also usable in a browser via #mini / #bar.
const Stats = (() => {
  const HOUR = 3600000;
  const money = v => v >= 100 ? "$" + v.toFixed(0) : v >= 1 ? "$" + v.toFixed(2) : "$" + v.toFixed(3);
  const cfg = {
    page: +localStorage.getItem("av.stats.page") || 0,
    rotate: localStorage.getItem("av.stats.rotate") === "1",
    by: localStorage.getItem("av.stats.by") || "project",
  };
  const save = () => {
    localStorage.setItem("av.stats.page", cfg.page); localStorage.setItem("av.stats.rotate", cfg.rotate ? "1" : "0");
    localStorage.setItem("av.stats.by", cfg.by);
  };

  let mode = null, root = null, data = null, busy = false, last = 0, tRefresh = null, tRotate = null, hover = false;
  let ctxStat = null, ctxKey = "", topEl = null;

  /* ---------- data ---------- */
  async function load(force) {
    if (busy || (!force && Date.now() - last < 4000)) return;
    busy = true;
    try {
      const [u, p] = await Promise.all([fetch("/api/usage").then(r => r.json()), fetch("/api/projects").then(r => r.json())]);
      data = { rows: u.rows, sessions: p.projects.flatMap(pr => pr.sessions.map(s => ({ ...s, project: pr.name, projectKey: pr.key, projectMTime: pr.mtime }))) };
      const cur = pickCurrent();
      const key = cur ? cur.source + cur.id + cur.mtime : "";
      if (cur && key !== ctxKey) {
        ctxKey = key;
        try { ctxStat = await fetch(`/api/stats?src=${encodeURIComponent(cur.source)}&id=${encodeURIComponent(cur.id)}`).then(r => r.json()); } catch { ctxStat = null; }
      }
    } catch {}
    busy = false; last = Date.now(); draw();
    if (mode === "bar" && data && typeof Bar !== "undefined") Bar.update(data.sessions, cfg);
  }
  // the session the context page watches: a running one, else the most recently touched
  function pickCurrent() {
    if (!data || !data.sessions.length) return null;
    return data.sessions.slice().sort((a, b) => (b.running - a.running) || b.mtime - a.mtime)[0];
  }
  const sumRows = (rs, f) => rs.reduce((a, r) => a + f(r), 0);
  const costOf = r => r.cost || 0, tokOf = r => r.in + r.out;
  const hourRows = t => data.rows.filter(r => r.hour === t);

  /* ---------- pages ---------- */
  const PAGES = [
    { id: "today", name: "Today", render() {
      const d0 = new Date(); d0.setHours(0, 0, 0, 0);
      const today = data.rows.filter(r => r.hour + HOUR > d0.getTime());
      const now = Date.now(), base = Math.floor(now / HOUR) * HOUR;
      const bars = Array.from({ length: 12 }, (_, i) => base - (11 - i) * HOUR).map(t => sumRows(hourRows(t), tokOf));
      const mx = Math.max(1, ...bars);
      const inn = sumRows(today, r => r.in + r.cr + r.cw), cr = sumRows(today, r => r.cr);
      return [h("div", {}, h("span", { class: "big" }, money(sumRows(today, costOf))), h("span", { class: "dimt" }, " today · " + fmtTok(sumRows(today, tokOf)) + " tok")),
        h("div", { class: "kv" }, h("span", {}, "this hour"), h("b", {}, fmtTok(sumRows(hourRows(base), tokOf)) + " tok"), h("span", {}, "cache hit"), h("b", {}, inn ? (100 * cr / inn).toFixed(0) + "%" : "–")),
        h("div", { class: "spark", title: "tokens per hour, last 12h" }, bars.map(v => h("i", { style: `height:${Math.max(2, Math.round(100 * v / mx))}%` })))];
    } },
    { id: "burn", name: "Burn rate", render() {
      const now = Date.now(), base = Math.floor(now / HOUR) * HOUR, d0 = new Date(); d0.setHours(0, 0, 0, 0);
      const cur = hourRows(base), prev = hourRows(base - HOUR);
      const l3 = [1, 2, 3].map(i => hourRows(base - i * HOUR));
      const avgC = sumRows(l3.flat(), costOf) / 3, avgT = sumRows(l3.flat(), tokOf) / 3;
      const today = sumRows(data.rows.filter(r => r.hour >= d0.getTime()), costOf);
      const ys = d0.getTime() - 86400000, yUntil = Math.floor((now - 86400000) / HOUR) * HOUR;
      const yest = sumRows(data.rows.filter(r => r.hour >= ys && r.hour <= yUntil), costOf);
      const delta = yest > 0 ? (100 * (today - yest) / yest) : null;
      const kv = (k, v) => h("div", { class: "kv" }, h("span", {}, k), h("b", {}, v));
      return [kv("this hour (so far)", `${money(sumRows(cur, costOf))} · ${fmtTok(sumRows(cur, tokOf))}`),
        kv("last hour", `${money(sumRows(prev, costOf))} · ${fmtTok(sumRows(prev, tokOf))}`),
        kv("avg / hour (3h)", `${money(avgC)} · ${fmtTok(avgT)}`),
        h("div", { class: "kv" }, h("span", {}, "today vs yesterday"), h("b", { class: delta == null ? "" : delta > 0 ? "up" : "down" },
          `${money(today)} / ${money(yest)}` + (delta == null ? "" : ` (${delta > 0 ? "+" : ""}${delta.toFixed(0)}%)`)))];
    } },
    { id: "active", name: "Active sessions", render() {
            let act = data.sessions.filter(s => s.running).sort((a, b) => (b.running - a.running) || b.mtime - a.mtime);
      const dimmed = !act.length;
      if (dimmed) act = data.sessions.slice().sort((a, b) => b.mtime - a.mtime).slice(0, 1);
      const rows = act.slice(0, 5).map(s => h("div", { class: "srow", title: `${s.project} · ${s.title}` },
        h("span", { class: "sdot " + (s.running ? "running" : dimmed ? "" : "recent"), title: s.running ? "running" : "recently active" }),
        h("span", { class: "badge " + s.kind }, s.kind), h("span", { class: "t" }, s.project + " · " + s.title), h("span", { class: "w" }, fmtRel(s.mtime))));
      return [h("div", { class: "dimt" }, dimmed ? `none running — latest:` : `${act.length} running`), ...rows];
    } },
    { id: "context", name: "Context", render() {
      const s = pickCurrent();
      if (!s || !ctxStat) return [h("div", { class: "dimt" }, "no session")];
      const used = ctxStat.contextUsed || 0, win = ctxStat.contextWindow || 0, pct = win ? 100 * used / win : 0;
      const col = pct > 80 ? "var(--err)" : pct > 60 ? "var(--warn)" : "var(--ok)";
      return [h("div", { class: "srow" }, h("span", { class: "sdot " + (s.running ? "running" : "recent") }), h("span", { class: "t" }, s.project + " · " + s.title)),
        h("div", { class: "dimt" }, ctxStat.lastModel || ctxStat.firstModel || ""),
        win ? h("div", { class: "gauge", title: pct.toFixed(1) + "%" }, h("i", { style: `width:${Math.min(100, pct)}%;background:${col}` })) : null,
        h("div", { class: "kv" }, h("span", {}, "context"), h("b", {}, fmtTok(used) + (win ? ` / ${fmtTok(win)} (${pct.toFixed(0)}%)` : " (window unknown)")))];
    } },
    { id: "spenders", name: "Top spenders", render() {
      const d0 = new Date(); d0.setHours(0, 0, 0, 0);
      const today = data.rows.filter(r => r.hour + HOUR > d0.getTime());
      const sessProj = new Map(data.sessions.map(s => [s.source + "\0" + s.id, s.project]));
      const key = r => cfg.by === "model" ? r.model : (r.projectName || "(unknown)");
      const useCost = sumRows(today, costOf) > 0, val = useCost ? costOf : tokOf;
      const m = new Map();
      for (const r of today) m.set(key(r), (m.get(key(r)) || 0) + val(r));
      const top = [...m.entries()].filter(e => e[1] > 0).sort((a, b) => b[1] - a[1]).slice(0, 3);
      const mx = Math.max(1e-9, ...top.map(e => e[1]));
      const ttl = h("div", { class: "ttl" }, "by", h("button", { title: "toggle project / model", onclick: () => { cfg.by = cfg.by === "model" ? "project" : "model"; save(); draw(); } }, cfg.by), useCost ? "cost" : "tokens");
      return [ttl, ...(top.length ? top.map(([k, v], i) => h("div", { class: "srow", title: k },
        h("span", { class: "t", style: "flex:0 0 38%" }, k), h("span", { class: "sbar", style: `width:${(100 * v / mx * 0.45).toFixed(1)}%;background:var(--c${i + 1})` }),
        h("span", { class: "w" }, useCost ? money(v) : fmtTok(v)))) : [h("div", { class: "dimt" }, "no usage today")])];
    } },
  ];

  /* ---------- rendering ---------- */
  function modeBtn(label, title, m) {
    return h("button", { title, onclick: () => requestMode(m) }, label);
  }
  function draw() {
    if (!root) return;
    const native = typeof window.avSetMode === "function";
    const page = PAGES[cfg.page % PAGES.length];
    const nav = h("div", { class: "nav" },
      h("button", { title: "previous page", onclick: () => go(-1) }, "‹"),
      PAGES.map((p, i) => h("button", { class: "dot" + (i === cfg.page ? " on" : ""), title: p.name + " (click to pin)", onclick: () => { cfg.page = i; cfg.rotate = false; save(); draw(); schedule(); } })),
      h("button", { title: "next page", onclick: () => go(1) }, "›"));
    const head = h("div", { class: "shead", onmousedown: e => { if (e.target.tagName !== "BUTTON" && window.avDrag) window.avDrag(); } },
      h("span", { class: "slive", title: "live" }), nav, h("span", { class: "sp" }),
      h("button", { class: "rot" + (cfg.rotate ? " on" : ""), title: "auto-rotate pages", onclick: () => { cfg.rotate = !cfg.rotate; save(); draw(); schedule(); } }, "⟳"),
      mode !== "bar" ? modeBtn("▯", "bar mode", "bar") : null, mode !== "mini" ? modeBtn("▫", "mini mode", "mini") : null,
      modeBtn("▭", "full window", "full"), native ? h("button", { title: "quit", onclick: () => window.avQuit() }, "✕") : null);
    const body = h("div", { class: "spage", onmouseenter: () => hover = true, onmouseleave: () => hover = false },
      h("div", { class: "ttl" }, page.name), ...(data ? page.render() : [h("div", { class: "dimt" }, "loading…")]));
    if (mode === "bar") { // the bar host below keeps its own scroll position and animations
      if (!topEl) topEl = h("div", { class: "bartop" });
      topEl.replaceChildren(head, body);
      if (root.firstChild !== topEl) root.replaceChildren(topEl, Bar.el);
    } else root.replaceChildren(head, body);
  }
  function go(d) { cfg.page = (cfg.page + d + PAGES.length) % PAGES.length; save(); draw(); }
  function schedule() {
    clearInterval(tRotate);
    if (cfg.rotate) tRotate = setInterval(() => { if (!hover) go(1); }, 8000);
  }

  return {
    show(m) {
      mode = m; topEl = null; root = $(m === "bar" ? "#barview" : "#miniview");
      draw(); load(true); schedule();
      clearInterval(tRefresh); tRefresh = setInterval(() => load(true), 30000);
    },
    hide() { clearInterval(tRefresh); clearInterval(tRotate); root = null; mode = null; topEl = null; },
    onChange() { if (root) load(false); },
    setPage(i) { cfg.page = i; draw(); },
  };
})();
