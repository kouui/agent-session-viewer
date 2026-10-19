"use strict";
// Mini view: a compact stats panel (used in the small always-on-top GUI window,
// also reachable in any browser with #mini). Relies on $, h, fmtTok from index.html.
const Mini = (() => {
  let root = null, timer = null, last = 0, busy = false, data = null;
  const HOUR = 3600000;
  const money = v => v >= 100 ? "$" + v.toFixed(0) : v >= 1 ? "$" + v.toFixed(2) : "$" + v.toFixed(3);

  async function load(force) {
    if (busy || (!force && Date.now() - last < 4000)) return;
    busy = true;
    try {
      const [u, p] = await Promise.all([fetch("/api/usage").then(r => r.json()), fetch("/api/projects").then(r => r.json())]);
      data = { rows: u.rows, projects: p.projects };
    } catch {}
    busy = false; last = Date.now(); draw();
  }

  function draw() {
    if (!root) return;
    const gui = typeof window.avRestore === "function";
    const head = h("div", { class: "mhead", onmousedown: e => { if (e.target.tagName !== "BUTTON" && window.avDrag) window.avDrag(); } },
      h("b", {}, "agent-viewer"), h("span", { class: "mlive", title: "live" }),
      h("span", { style: "flex:1" }),
      gui ? h("button", { title: "restore", onclick: () => window.avRestore() }, "⤢") : h("button", { title: "full view", onclick: () => setMini(false) }, "⤢"),
      gui ? h("button", { title: "quit", onclick: () => window.avQuit() }, "✕") : null);
    const body = h("div", { class: "mbody" });
    if (!data) body.append(h("div", { class: "mdim" }, "loading…"));
    else {
      const now = Date.now(), d0 = new Date(); d0.setHours(0, 0, 0, 0);
      const today = data.rows.filter(r => r.hour + HOUR > d0.getTime());
      const cost = today.reduce((a, r) => a + (r.cost || 0), 0);
      const tok = today.reduce((a, r) => a + r.in + r.out, 0);
      const inn = today.reduce((a, r) => a + r.in + r.cr + r.cw, 0), cr = today.reduce((a, r) => a + r.cr, 0);
      const lastH = data.rows.filter(r => r.hour >= Math.floor(now / HOUR) * HOUR);
      const active = data.projects.flatMap(p => p.sessions).filter(s => now - s.mtime < 5 * 60000).length;
      // tokens per hour, last 12 hours
      const bars = Array.from({ length: 12 }, (_, i) => Math.floor(now / HOUR) * HOUR - (11 - i) * HOUR)
        .map(t => data.rows.filter(r => r.hour === t).reduce((a, r) => a + r.in + r.out, 0));
      const mx = Math.max(1, ...bars);
      body.append(
        h("div", { class: "mmain" }, h("span", { class: "mcost" }, money(cost)), h("span", { class: "mdim" }, " today · " + fmtTok(tok) + " tok")),
        h("div", { class: "mrow" }, h("span", {}, "this hour ", h("b", {}, fmtTok(lastH.reduce((a, r) => a + r.in + r.out, 0)))),
          h("span", {}, "hit ", h("b", {}, inn ? (100 * cr / inn).toFixed(0) + "%" : "–")),
          h("span", {}, "active ", h("b", {}, active))),
        h("div", { class: "mspark", title: "tokens per hour, last 12h" }, bars.map(v => h("i", { style: `height:${Math.max(2, Math.round(100 * v / mx))}%` }))));
    }
    root.replaceChildren(head, body);
  }

  return {
    show() { root = $("#miniview"); load(true); clearInterval(timer); timer = setInterval(() => load(true), 30000); },
    hide() { clearInterval(timer); },
    onChange() { if (root && document.body.classList.contains("mini")) load(false); },
  };
})();
