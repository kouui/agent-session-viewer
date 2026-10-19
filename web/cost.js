"use strict";
// Cost page: per-project cost/token summary and per-hour charts, by agent+model.
// Relies on helpers from index.html: $, h, fmtTok.
const Cost = (() => {
  const SLOTS = 8, HOUR = 3600000;
  const st = {
    range: localStorage.getItem("av.cost.range") || "7d",
    bucket: localStorage.getItem("av.cost.bucket") || "auto",
    tokens: localStorage.getItem("av.cost.tok") || "io",
    metric: localStorage.getItem("av.cost.metric") || "cost",
    project: null, expanded: new Set(), showAll: false, table: { cost: false, tokens: false },
  };
  const save = () => { for (const k of ["range", "bucket", "tokens", "metric"]) localStorage.setItem("av.cost." + (k === "tokens" ? "tok" : k), st[k]); };
  const RANGES = { "24h": 24 * HOUR, "7d": 7 * 24 * HOUR, "30d": 30 * 24 * HOUR, all: 0 };

  let rows = null, rank = new Map(), root = null, body = null, busy = false, lastFetch = 0, err = "";

  /* ---------- data ---------- */
  async function fetchRows(force) {
    if (busy || (!force && Date.now() - lastFetch < 5000)) return;
    busy = true; root.classList.add("loading");
    try {
      const r = await fetch("/api/usage");
      if (!r.ok) throw new Error(await r.text());
      rows = (await r.json()).rows; err = "";
      computeRank();
    } catch (e) { err = e.message; }
    busy = false; lastFetch = Date.now(); root.classList.remove("loading");
    render();
  }
  const skey = r => r.kind + "|" + r.model;
  // Color follows the entity: slots are assigned by ALL-TIME volume, so filters never repaint series.
  function computeRank() {
    const tot = new Map();
    for (const r of rows) tot.set(skey(r), (tot.get(skey(r)) || 0) + r.in + r.out + r.cr + r.cw);
    rank = new Map([...tot.entries()].sort((a, b) => b[1] - a[1]).map(([k], i) => [k, i]));
  }
  const slotOf = r => Math.min(rank.get(skey(r)) ?? SLOTS, SLOTS); // SLOTS = "other"
  const color = slot => slot >= SLOTS ? "var(--cother)" : `var(--c${slot + 1})`;
  const tokOf = r => r.in + r.out + (st.tokens === "all" ? r.cr + r.cw : 0);
  const costOf = r => r.cost || 0;

  function rangeRows() {
    const since = RANGES[st.range] ? Date.now() - RANGES[st.range] : 0;
    return since ? rows.filter(r => r.hour + HOUR > since) : rows;
  }

  /* ---------- formatting ---------- */
  function fmtCost(v) {
    if (v >= 1000) return "$" + (v / 1000).toFixed(1) + "K";
    if (v >= 100) return "$" + v.toFixed(0);
    if (v >= 1) return "$" + v.toFixed(2);
    return "$" + v.toFixed(4);
  }
  const two = n => String(n).padStart(2, "0");
  const fmtDay = t => { const d = new Date(t); return `${two(d.getMonth() + 1)}/${two(d.getDate())}`; };
  const fmtHr = t => `${two(new Date(t).getHours())}:00`;
  const fmtFull = (t, day) => day ? `${new Date(t).getFullYear()}-${fmtDay(t).replace("/", "-")}` : `${fmtDay(t)} ${fmtHr(t)}`;

  /* ---------- tooltip ---------- */
  let tip = null;
  function showTip(e, title, items, total) {
    if (!tip) { tip = h("div", { class: "ctip" }); document.body.append(tip); }
    const kids = [title ? h("div", { class: "tt" }, title) : null];
    for (const it of items) kids.push(h("div", { class: "row" },
      h("span", { class: "ln", style: `background:${it.color}` }), h("span", { class: "nm" }, it.label), h("b", {}, it.value)));
    if (total != null) kids.push(h("div", { class: "tot" }, "total", h("b", {}, total)));
    tip.replaceChildren(...kids.filter(Boolean));
    tip.hidden = false;
    const w = tip.offsetWidth, hh = tip.offsetHeight;
    let x = e.clientX + 14, y = e.clientY + 14;
    if (x + w > innerWidth - 6) x = e.clientX - w - 14;
    if (y + hh > innerHeight - 6) y = e.clientY - hh - 14;
    tip.style.left = Math.max(4, x) + "px"; tip.style.top = Math.max(4, y) + "px";
  }
  const hideTip = () => { if (tip) tip.hidden = true; };

  /* ---------- aggregation ---------- */
  function seriesInfo(slot, sample) { // label for a slot
    if (slot >= SLOTS) return { label: "Other models", kind: "" };
    return { label: sample.model, kind: sample.kind };
  }
  const sliceInfo = new Map(); // slot -> {label, kind}; filled while aggregating
  function noteSlot(r) {
    const s = slotOf(r);
    if (!sliceInfo.has(s)) sliceInfo.set(s, seriesInfo(s, r));
    return s;
  }

  function byProject(rs) {
    const m = new Map();
    for (const r of rs) {
      let p = m.get(r.project);
      if (!p) m.set(r.project, p = { key: r.project, name: r.projectName, cost: 0, tokens: 0, sessions: new Set(), by: new Map() });
      const s = noteSlot(r);
      let b = p.by.get(s);
      if (!b) p.by.set(s, b = { slot: s, cost: 0, tokens: 0, in: 0, cr: 0, cw: 0, out: 0, sessions: new Set(), priced: false });
      const c = costOf(r), t = tokOf(r);
      p.cost += c; p.tokens += t; p.sessions.add(r.src + r.sid);
      b.cost += c; b.tokens += t; b.in += r.in; b.cr += r.cr; b.cw += r.cw; b.out += r.out; b.sessions.add(r.src + r.sid);
      if (r.cost != null) b.priced = true;
    }
    return [...m.values()];
  }

  /* ---------- rendering: shell ---------- */
  function btn(label, on, fn, title) { return h("button", { class: on ? "on" : "", title: title || "", onclick: fn }, label); }
  function seg(opts, cur, set) { return h("div", { class: "seg" }, opts.map(([v, l, t]) => btn(l, cur === v, () => { set(v); save(); render(); }, t))); }

  function render() {
    if (!root) return;
    const top = root.scrollTop;
    const wrap = h("div", { class: "cwrap" });
    wrap.append(h("div", { class: "cfilters" },
      h("div", { class: "grp" }, h("span", {}, "range"), seg([["24h", "24h"], ["7d", "7 days"], ["30d", "30 days"], ["all", "All"]], st.range, v => st.range = v)),
      h("div", { class: "grp" }, h("span", {}, "chart bucket"), seg([["auto", "auto"], ["hour", "hour"], ["day", "day"]], st.bucket, v => st.bucket = v)),
      h("div", { class: "grp" }, h("span", {}, "tokens"), seg([["io", "in + out", "new input + output"], ["all", "+ cache", "also count cache read/write"]], st.tokens, v => st.tokens = v))));
    body = h("div", { class: "cbody" });
    wrap.append(body);
    if (err) body.append(h("div", { class: "cnote", style: "color:var(--err)" }, "Failed to load usage: " + err));
    else if (!rows) body.append(h("div", { class: "cnote" }, "Loading usage… (the first load scans all history and can take a few seconds)"));
    else renderBody();
    root.replaceChildren(wrap);
    root.scrollTop = top;
  }

  function renderBody() {
    sliceInfo.clear();
    const rs = rangeRows();
    const projects = byProject(rs);
    const anyUnpriced = rs.some(r => r.cost == null);
    const totCost = rs.reduce((a, r) => a + costOf(r), 0), totTok = rs.reduce((a, r) => a + tokOf(r), 0);
    const inn = rs.reduce((a, r) => a + r.in + r.cr + r.cw, 0), cr = rs.reduce((a, r) => a + r.cr, 0);
    const sessions = new Set(rs.map(r => r.src + r.sid)).size;
    const kinds = {};
    for (const r of rs) { const k = kinds[r.kind] || (kinds[r.kind] = { cost: 0, tok: 0, priced: false }); k.cost += costOf(r); k.tok += tokOf(r); if (r.cost != null) k.priced = true; }

    body.append(h("div", { class: "tiles" },
      tile("Total cost", fmtCost(totCost), Object.entries(kinds).map(([k, v]) => `${k} ${v.priced ? fmtCost(v.cost) : "n/a"}`).join(" · ") + (anyUnpriced ? " (no price recorded for some agents)" : "")),
      tile(st.tokens === "all" ? "Tokens (incl. cache)" : "Tokens (in + out)", fmtTok(totTok), Object.entries(kinds).map(([k, v]) => `${k} ${fmtTok(v.tok)}`).join(" · ")),
      tile("Cache hit rate", inn ? (100 * cr / inn).toFixed(1) + "%" : "–", "cache read ÷ total input"),
      tile("Sessions with usage", String(sessions), projects.length + " projects")));

    body.append(projectSection(projects));
    body.append(chartsSection(rs));
    body.append(h("div", { class: "cnote" }, "Hour buckets use UTC hour boundaries, displayed in local time. Cost is attributed to the hour a message was created."));
  }
  const tile = (l, v, sub) => h("div", { class: "tile" }, h("div", { class: "lbl2" }, l), h("div", { class: "val" }, v), h("div", { class: "sub" }, sub || ""));

  /* ---------- project table ---------- */
  function projectSection(projects) {
    const metric = st.metric, val = p => metric === "cost" ? p.cost : p.tokens;
    projects.sort((a, b) => val(b) - val(a) || b.tokens - a.tokens);
    const max = Math.max(1e-9, ...projects.map(val));
    const total = { name: "All projects", by: new Map(), cost: 0, tokens: 0, sessions: new Set() };
    for (const p of projects) {
      total.cost += p.cost; total.tokens += p.tokens; p.sessions.forEach(s => total.sessions.add(s));
      for (const [s, b] of p.by) {
        let t = total.by.get(s); if (!t) total.by.set(s, t = { slot: s, cost: 0, tokens: 0, in: 0, cr: 0, cw: 0, out: 0, sessions: new Set(), priced: false });
        t.cost += b.cost; t.tokens += b.tokens; t.in += b.in; t.cr += b.cr; t.cw += b.cw; t.out += b.out; b.sessions.forEach(x => t.sessions.add(x)); t.priced ||= b.priced;
      }
    }
    const shown = st.showAll ? projects : projects.slice(0, 20);
    const used = [...total.by.keys()].sort((a, b) => a - b);

    const sec = h("div", { class: "csec" },
      h("h2", {}, "By project", h("span", { class: "push" }, seg([["cost", "Cost"], ["tokens", "Tokens"]], st.metric, v => st.metric = v))),
      h("div", { class: "desc" }, "Bars share one scale and split by agent · model. Click a row for the breakdown; it also scopes the hourly charts below."),
      legend(used));
    const tbl = h("table", { class: "ptable" }, h("thead", {}, h("tr", {},
      h("th", {}, "Project"), h("th", { class: "r" }, "Sessions"), h("th", { class: "r" }, "Cost"), h("th", { class: "r" }, "Tokens"), h("th", {}, metric === "cost" ? "Cost by agent · model" : "Tokens by agent · model"))));
    const tb = h("tbody", {});
    tb.append(projRow(total, max * 1.0001, metric, true, projects.length ? max : 1));
    for (const p of shown) {
      tb.append(projRow(p, max, metric, false));
      if (st.expanded.has(p.key)) tb.append(h("tr", { class: "det" }, h("td", { colspan: 5 }, detailTable(p))));
    }
    tbl.append(tb);
    sec.append(tbl);
    if (projects.length > shown.length) sec.append(h("div", { class: "more" }, btn(`show all ${projects.length} projects`, false, () => { st.showAll = true; render(); })));
    else if (st.showAll && projects.length > 20) sec.append(h("div", { class: "more" }, btn("show top 20", false, () => { st.showAll = false; render(); })));
    return sec;
  }
  function projRow(p, max, metric, isTotal) {
    const sel = !isTotal && st.project === p.key;
    const tr = h("tr", { class: (isTotal ? "tot" : "prow") + (sel ? " sel" : ""), title: isTotal ? "" : p.name,
      onclick: isTotal ? null : () => {
        st.expanded.has(p.key) ? st.expanded.delete(p.key) : st.expanded.add(p.key);
        st.project = sel ? null : p.key; render();
      } },
      h("td", {}, h("div", { class: "pname" }, p.name)),
      h("td", { class: "r" }, p.sessions.size),
      h("td", { class: "r" }, [...p.by.values()].some(b => b.priced) ? fmtCost(p.cost) : "n/a"),
      h("td", { class: "r" }, fmtTok(p.tokens)),
      h("td", { class: "barcell" }, bar(p, max, metric, isTotal)));
    return tr;
  }
  function bar(p, max, metric, isTotal) {
    const v = metric === "cost" ? p.cost : p.tokens;
    if (!(v > 0)) return h("div", { class: "cnote", style: "margin:0" }, metric === "cost" ? "no priced usage" : "–");
    const scale = isTotal ? v : max; // total row fills the cell so it shows the overall mix
    const el = h("div", { class: "pbar" });
    for (const s of [...p.by.keys()].sort((a, b) => a - b)) {
      const b = p.by.get(s), x = metric === "cost" ? b.cost : b.tokens;
      if (!(x > 0)) continue;
      const seg2 = h("div", { class: "seg2", style: `background:${color(s)};width:max(0px,calc(${(100 * x / scale).toFixed(3)}% - 2px))` });
      const info = sliceInfo.get(s);
      seg2.addEventListener("pointermove", e => showTip(e, p.name, [{ color: color(s), label: info.label + (info.kind ? " · " + info.kind : ""),
        value: metric === "cost" ? fmtCost(x) : fmtTok(x) }], (100 * x / v).toFixed(1) + "% of " + (metric === "cost" ? fmtCost(v) : fmtTok(v))));
      seg2.addEventListener("pointerleave", hideTip);
      el.append(seg2);
    }
    return el;
  }
  function detailTable(p) {
    const items = [...p.by.values()].sort((a, b) => b.cost - a.cost || b.tokens - a.tokens);
    return h("table", { class: "ptable sub" }, h("thead", {}, h("tr", {}, ["agent · model", "sessions", "in", "cache read", "cache write", "out", "cost", "share"].map((t, i) => h("th", { class: i ? "r" : "" }, t)))),
      h("tbody", {}, items.map(b => {
        const info = sliceInfo.get(b.slot), share = st.metric === "cost" ? (p.cost ? b.cost / p.cost : 0) : (p.tokens ? b.tokens / p.tokens : 0);
        return h("tr", {}, h("td", {}, h("span", { class: "sw", style: `background:${color(b.slot)};display:inline-block;margin-right:6px` }), info.label, info.kind ? h("span", { class: "badge " + info.kind, style: "margin-left:6px" }, info.kind) : null),
          h("td", { class: "r" }, b.sessions.size), h("td", { class: "r" }, fmtTok(b.in)), h("td", { class: "r" }, fmtTok(b.cr)), h("td", { class: "r" }, fmtTok(b.cw)), h("td", { class: "r" }, fmtTok(b.out)),
          h("td", { class: "r" }, b.priced ? fmtCost(b.cost) : "n/a"), h("td", { class: "r" }, (100 * share).toFixed(1) + "%"));
      })));
  }
  function legend(slots) {
    return h("div", { class: "legend" }, slots.map(s => {
      const info = sliceInfo.get(s);
      return h("span", { class: "it" }, h("span", { class: "sw", style: `background:${color(s)}` }), info.label, info.kind ? h("span", { class: "badge " + info.kind }, info.kind) : null);
    }));
  }

  /* ---------- hourly charts ---------- */
  const startOf = (t, day) => { if (!day) return Math.floor(t / HOUR) * HOUR; const d = new Date(t); d.setHours(0, 0, 0, 0); return d.getTime(); };
  const nextOf = (t, day) => { if (!day) return t + HOUR; const d = new Date(t); d.setDate(d.getDate() + 1); return d.getTime(); };

  function chartsSection(rs) {
    const scoped = st.project ? rs.filter(r => r.project === st.project) : rs;
    const pname = st.project ? (rs.find(r => r.project === st.project) || {}).projectName || st.project : null;
    const day = st.bucket === "day" || (st.bucket === "auto" && (st.range === "30d" || st.range === "all"));
    const now = Date.now();
    const minT = st.range === "all" ? Math.min(now, ...rs.map(r => r.hour)) : now - RANGES[st.range];
    const buckets = [];
    for (let t = startOf(minT, day); t <= now && buckets.length < 5000; t = nextOf(t, day)) buckets.push({ t, v: new Map() });
    const idx = new Map(buckets.map((b, i) => [b.t, i]));
    for (const r of scoped) {
      const b = buckets[idx.get(startOf(r.hour, day))];
      if (!b) continue;
      const s = noteSlot(r);
      const c = b.v.get(s) || (b.v.set(s, { cost: 0, tokens: 0 }), b.v.get(s));
      c.cost += costOf(r); c.tokens += tokOf(r);
    }
    const slots = new Set();
    for (const b of buckets) for (const [s, c] of b.v) if (c.cost > 0 || c.tokens > 0) slots.add(s);
    const used = [...slots].sort((a, b) => a - b);
    const sec = h("div", { class: "csec" },
      h("h2", {}, `Per ${day ? "day" : "hour"}`, h("span", { class: "desc", style: "margin:0" }, "scope: " + (pname || "all projects")),
        st.project ? h("span", { class: "chip2", onclick: () => { st.project = null; render(); } }, "clear ✕") : null),
      legend(used));
    const charts = h("div", { class: "charts" });
    charts.append(chartCard("cost", "Cost per " + (day ? "day" : "hour"), buckets, used, day, fmtCost, scoped.some(r => r.cost != null)));
    charts.append(chartCard("tokens", "Tokens per " + (day ? "day" : "hour") + (st.tokens === "all" ? " (incl. cache)" : " (in + out)"), buckets, used, day, fmtTok, true));
    sec.append(charts);
    return sec;
  }

  function niceTicks(max) {
    if (!(max > 0)) return [0, 1];
    const raw = max / 4, p = Math.pow(10, Math.floor(Math.log10(raw))), f = raw / p;
    const step = (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * p, ticks = [];
    for (let v = 0, i = 0; i < 12; v += step, i++) { ticks.push(v); if (v >= max) break; }
    return ticks;
  }
  const NS = "http://www.w3.org/2000/svg";
  function sv(tag, attrs, ...kids) {
    const e = document.createElementNS(NS, tag);
    for (const [k, v] of Object.entries(attrs || {})) e.setAttribute(k, v);
    for (const c of kids) if (c != null) e.append(c.nodeType ? c : document.createTextNode(c));
    return e;
  }

  function chartCard(metric, title, buckets, used, day, fmt, hasData) {
    const card = h("div", { class: "chart" });
    const showTbl = st.table[metric];
    card.append(h("h3", {}, title, h("button", { title: "toggle table view", onclick: () => { st.table[metric] = !showTbl; render(); } }, showTbl ? "chart" : "table")));
    const val = (b, s) => (b.v.get(s) || {})[metric] || 0;
    const totalOf = b => used.reduce((a, s) => a + val(b, s), 0);
    const max = Math.max(0, ...buckets.map(totalOf));
    if (!hasData || !(max > 0)) {
      card.append(h("div", { class: "none" }, metric === "cost" ? "No priced usage in this range/scope (codex does not record price — see tokens)." : "No usage in this range/scope."));
      return card;
    }
    if (showTbl) {
      const rowsT = buckets.filter(b => totalOf(b) > 0).reverse().slice(0, 500);
      card.append(h("div", { class: "tblwrap" }, h("table", { class: "ptable sub" },
        h("thead", {}, h("tr", {}, h("th", {}, day ? "day" : "hour"), used.map(s => h("th", { class: "r" }, sliceInfo.get(s).label)), h("th", { class: "r" }, "total"))),
        h("tbody", {}, rowsT.map(b => h("tr", {}, h("td", {}, fmtFull(b.t, day)), used.map(s => h("td", { class: "r" }, val(b, s) ? fmt(val(b, s)) : "")), h("td", { class: "r" }, fmt(totalOf(b)))))))));
      return card;
    }
    const W = 640, H = 200, L = 46, R = 6, T = 8, B = 24, PW = W - L - R, PH = H - T - B;
    const ticks = niceTicks(max), yMax = ticks[ticks.length - 1];
    const svg = sv("svg", { viewBox: `0 0 ${W} ${H}`, role: "img", "aria-label": title });
    const axis = sv("g", { class: "axis" });
    for (const t of ticks) {
      const y = T + PH - (t / yMax) * PH;
      if (t > 0) axis.append(sv("line", { class: "gridl", x1: L, x2: W - R, y1: y, y2: y }));
      axis.append(sv("text", { x: L - 6, y: y + 4, "text-anchor": "end" }, t === 0 ? "0" : fmt(t)));
    }
    svg.append(axis);
    const n = buckets.length, slot = PW / n, barW = Math.max(1, Math.min(24, slot - 2));
    const hl = sv("rect", { class: "hl", y: T, height: PH, width: slot, visibility: "hidden" });
    svg.append(hl);
    const stepLbl = Math.max(1, Math.ceil(n / Math.max(2, Math.floor(PW / 84))));
    buckets.forEach((b, i) => {
      const x0 = L + i * slot, xb = x0 + (slot - barW) / 2;
      let y = T + PH, first = true;
      const present = used.filter(s => val(b, s) > 0);
      present.forEach((s, j) => {
        const hgt = (val(b, s) / yMax) * PH, top = y - hgt, last = j === present.length - 1;
        const gap = first ? 0 : 2, hh = Math.max(0.5, hgt - (hgt > 4 ? gap : 0));
        const fill = `fill:${color(s)}`;
        if (last && barW >= 6 && hh >= 5) {
          const r = 4, x1 = xb + barW, yt = top;
          svg.append(sv("path", { style: fill, d: `M${xb},${yt + hh} V${yt + r} Q${xb},${yt} ${xb + r},${yt} H${x1 - r} Q${x1},${yt} ${x1},${yt + r} V${yt + hh} Z` }));
        } else svg.append(sv("rect", { style: fill, x: xb, y: top, width: barW, height: hh }));
        y = top; first = false;
      });
      if (i % stepLbl === 0) {
        const lbl = day ? fmtDay(b.t) : (i === 0 || new Date(b.t).getHours() === 0 ? `${fmtDay(b.t)} ${fmtHr(b.t)}` : fmtHr(b.t));
        axis.append(sv("text", { x: x0 + slot / 2, y: H - 6, "text-anchor": "middle" }, lbl));
      }
      const hit = sv("rect", { x: x0, y: T, width: slot, height: PH, fill: "transparent" });
      hit.addEventListener("pointermove", e => {
        hl.setAttribute("x", x0); hl.setAttribute("visibility", "visible");
        if (!present.length) return hideTip();
        showTip(e, fmtFull(b.t, day), present.slice().reverse().map(s => ({ color: color(s), label: sliceInfo.get(s).label + (sliceInfo.get(s).kind ? " · " + sliceInfo.get(s).kind : ""), value: fmt(val(b, s)) })), fmt(totalOf(b)));
      });
      hit.addEventListener("pointerleave", () => { hl.setAttribute("visibility", "hidden"); hideTip(); });
      svg.append(hit);
    });
    svg.append(sv("line", { class: "base", x1: L, x2: W - R, y1: T + PH, y2: T + PH }));
    card.append(svg);
    return card;
  }

  /* ---------- lifecycle ---------- */
  return {
    show() {
      if (!root) root = $("#cost");
      render(); fetchRows(true);
    },
    onChange() { if (root && !root.hidden) fetchRows(false); },
    rerender() { if (root && !root.hidden) render(); },
  };
})();
