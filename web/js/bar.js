"use strict";
// Bar mode body: user-picked projects, their active sessions, and the latest N
// messages of each session. Rendered below the stats pages (see stats.js).
// Cards and feed lines are keyed and reused, so new messages animate in and old
// ones fade out instead of the whole list being redrawn.
const Bar = (() => {
  const LS = "av.bar.";
  const cfg = {
    projects: JSON.parse(localStorage.getItem(LS + "projects") || "[]"), // project keys, in display order
    n: Math.min(20, Math.max(1, +localStorage.getItem(LS + "n") || 4)),
  };
  const FS_MIN = 10, FS_MAX = 44;
  cfg.fs = Math.min(FS_MAX, Math.max(FS_MIN, +localStorage.getItem(LS + "fs") || 26)); // font size (px) of the projects pane
  const save = () => { localStorage.setItem(LS + "projects", JSON.stringify(cfg.projects)); localStorage.setItem(LS + "n", cfg.n); localStorage.setItem(LS + "fs", cfg.fs); };
  const applyFs = () => el.style.setProperty("--bfs", cfg.fs + "px");

  const el = h("div", { class: "barbody" });
  const projsEl = h("div", { class: "projs" }); // persistent: keeps its scroll position across updates
  let sessions = [], picking = false;
  const cards = new Map(); // src|id -> { el, feedEl, meta, key, ... }
  const fetched = new Map(); // src|id -> "mtime|n" last fetched

  const sk = s => s.source + "|" + s.id;
  // active = the agent is working on it right now (last message unfinished); see Session.Running
const isActive = s => s.running;

  /* ---------- feed lines ---------- */
  function lineEl(it) {
    const e = h("div", { class: "fl " + it.kind, "data-id": it.id });
    fillLine(e, it);
    return e;
  }
  function fillLine(e, it) {
    e.className = e.className.replace(/\s(pending|running|completed|error)\b/g, "") + (it.kind === "tool" && it.status ? " " + it.status : "");
    const icon = it.kind === "user" ? "›" : it.kind === "tool" ? (it.status === "error" ? "✗" : it.status === "completed" ? "✓" : "…") : "•";
    e.replaceChildren(h("span", { class: "fi" }, icon),
      it.kind === "tool" ? h("span", { class: "ft" }, h("b", {}, it.tool || "tool"), it.text ? " " + it.text : "") : h("span", { class: "ft" }, it.text));
  }
  // The feed has a fixed height of N slots (see .feed in stats.css), so a card never changes
  // size. Lines are keyed by id: new lines fade in at the bottom, lines that drop out of the
  // newest N leave a ghost that fades out, and the surviving lines slide up one slot (FLIP).
  function reconcile(feedEl, items) {
    const first = !feedEl.dataset.init;
    feedEl.dataset.init = "1";
    const live = new Map(), top = new Map();
    for (const c of [...feedEl.children]) if (!c.classList.contains("ghost")) { live.set(c.dataset.id, c); top.set(c.dataset.id, c.offsetTop); }
    const want = new Set(items.map(i => i.id));
    for (const [id, node] of live) {
      if (want.has(id)) continue;
      if (!first) { // ghost at the old position, out of flow
        const g = node.cloneNode(true);
        g.classList.add("ghost");
        g.classList.remove("in");
        g.style.top = node.offsetTop + "px";
        feedEl.append(g);
        setTimeout(() => g.remove(), 450);
      }
      node.remove();
      live.delete(id);
    }
    let prev = null;
    const moved = [];
    for (const it of items) {
      let node = live.get(it.id);
      if (node) { fillLine(node, it); moved.push([node, top.get(it.id)]); }
      else { node = lineEl(it); if (!first) node.classList.add("in"); }
      const pos = prev ? prev.nextSibling : feedEl.firstChild;
      if (pos !== node) feedEl.insertBefore(node, pos);
      prev = node;
    }
    if (first) return;
    for (const [node, was] of moved) {
      const dy = was - node.offsetTop;
      if (!dy) continue;
      node.style.transition = "none"; node.style.transform = `translateY(${dy}px)`;
      node.getBoundingClientRect(); // commit the start position
      node.style.transition = "transform .3s ease"; node.style.transform = "";
      setTimeout(() => { node.style.transition = ""; }, 340);
    }
  }

  /* ---------- session cards ---------- */
  function card(s) {
    const k = sk(s);
    let c = cards.get(k);
    if (!c) {
      c = { el: h("div", { class: "scard" }), feedEl: h("div", { class: "feed" }), head: h("div", { class: "shd" }), meta: h("div", { class: "smeta" }) };
      c.el.append(c.head, c.meta, c.feedEl);
      cards.set(k, c);
    }
    c.el.classList.toggle("idle", !isActive(s));
    c.feedEl.style.setProperty("--n", cfg.n);
    c.head.replaceChildren(h("span", { class: "sdot " + (s.running ? "running" : isActive(s) ? "recent" : "") }), h("span", { class: "badge " + s.kind }, s.kind),
      h("span", { class: "t", title: s.title }, s.title), h("span", { class: "w" }, fmtRel(s.mtime)));
    const key = s.mtime + "|" + cfg.n;
    if (fetched.get(k) !== key) {
      fetched.set(k, key);
      refresh(s, c);
    }
    return c.el;
  }
  async function refresh(s, c) {
    const q = `src=${encodeURIComponent(s.source)}&id=${encodeURIComponent(s.id)}`;
    try {
      const [f, st] = await Promise.all([fetch(`/api/feed?${q}&n=${cfg.n}`).then(r => r.json()), fetch(`/api/stats?${q}`).then(r => r.json())]);
      reconcile(c.feedEl, f.items);
      const used = st.contextUsed || 0, win = st.contextWindow || 0, pct = win ? 100 * used / win : 0;
      const col = pct > 80 ? "var(--err)" : pct > 60 ? "var(--warn)" : "var(--ok)";
      c.meta.replaceChildren(h("span", { class: "mdl" }, st.lastModel || st.firstModel || ""),
        win ? h("span", { class: "cg", title: `context ${fmtTok(used)} / ${fmtTok(win)}` }, h("i", { style: `width:${Math.min(100, pct).toFixed(0)}%;background:${col}` })) : null,
        h("span", { class: "w" }, win ? pct.toFixed(0) + "% ctx" : used ? fmtTok(used) + " ctx" : ""));
    } catch {}
  }

  /* ---------- projects ---------- */
  function projectEl(key) {
    const all = sessions.filter(s => s.projectKey === key).sort((a, b) => b.mtime - a.mtime);
    const name = all[0] ? all[0].project : key;
    let shown = all.filter(isActive);
    const none = !shown.length && all.length;
    if (none) shown = all.slice(0, 1);
    return h("div", { class: "proj" + (none ? " dim" : "") },
      h("div", { class: "phd" }, h("span", { class: "pn", title: all[0] ? all[0].project : key }, name),
        h("span", { class: "w" }, none ? "idle" : shown.length + " running"),
        h("button", { class: "x", title: "remove from the bar", onclick: () => { cfg.projects = cfg.projects.filter(k => k !== key); save(); render(); } }, "✕")),
      shown.length ? shown.slice(0, 6).map(card) : h("div", { class: "dimt" }, "no sessions"));
  }
  function picker() {
    const byKey = new Map();
    for (const s of sessions) if (!byKey.has(s.projectKey)) byKey.set(s.projectKey, { key: s.projectKey, name: s.project, mtime: s.projectMTime || s.mtime });
    const opts = [...byKey.values()].filter(p => !cfg.projects.includes(p.key)).sort((a, b) => b.mtime - a.mtime).slice(0, 30);
    return h("div", { class: "picker" }, opts.length ? opts.map(p => h("div", { class: "popt", onclick: () => { cfg.projects.push(p.key); picking = false; save(); render(); } },
      h("span", { class: "t" }, p.name), h("span", { class: "w" }, fmtRel(p.mtime)))) : h("div", { class: "dimt" }, "all projects added"));
  }
  function render() {
    const stepper = h("span", { class: "stp", title: "messages shown per session" },
      h("button", { onclick: () => { cfg.n = Math.max(1, cfg.n - 1); save(); render(); } }, "−"), h("span", {}, cfg.n + " msgs"),
      h("button", { onclick: () => { cfg.n = Math.min(20, cfg.n + 1); save(); render(); } }, "+"));
    const fsBtn = (label, title, d) => h("button", { title, onclick: () => { cfg.fs = Math.min(FS_MAX, Math.max(FS_MIN, cfg.fs + d)); save(); applyFs(); } }, label);
    const bar = h("div", { class: "pbars" },
      h("div", { class: "bbar" }, h("span", { class: "ttl" }, "Projects"), h("span", { class: "sp" }),
        h("button", { class: picking ? "on" : "", title: "add a project", onclick: () => { picking = !picking; render(); } }, "+ project")),
      h("div", { class: "bbar" }, stepper, h("span", { class: "sp" }), h("span", { class: "stp" }, fsBtn("A−", "smaller text", -2), fsBtn("A+", "larger text", 2))));
    const top = projsEl.scrollTop;
    projsEl.replaceChildren(...(cfg.projects.length ? cfg.projects.map(projectEl) : [h("div", { class: "dimt pad" }, sessions.length ? "Press “+ project” to follow a project." : "loading…")]));
    el.replaceChildren(...[bar, picking ? picker() : null, projsEl].filter(Boolean));
    projsEl.scrollTop = top;
    // drop cards that are no longer displayed so stale feeds do not pile up
    const live = new Set([...el.querySelectorAll(".scard")]);
    for (const [k, c] of cards) if (!live.has(c.el)) { cards.delete(k); fetched.delete(k); }
  }

  applyFs();
  return {
    el,
    update(list) { sessions = list; render(); },
  };
})();
