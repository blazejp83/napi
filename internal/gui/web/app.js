"use strict";

const api = () => window.go.gui.App;
const $ = (id) => document.getElementById(id);
const el = (tag, cls, text) => {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text != null) e.textContent = text;
  return e;
};

window.addEventListener("DOMContentLoaded", async () => {
  // Wails injects window.go shortly after the page loads.
  for (let i = 0; i < 100 && !(window.go && window.go.gui); i++) await new Promise((r) => setTimeout(r, 20));
  const mode = await api().Mode();
  if (mode === "settings") initSettings();
  else initPanel();
});

/* ---------------- corner card ---------------- */

const icons = { ok: "i-ok", partial: "i-warn", none: "i-info", error: "i-err", cancelled: "i-warn" };
let expanded = false;
let closeTimer = null;
let lastState = null;

function initPanel() {
  $("panel").hidden = false;
  $("p-close").onclick = () => api().Close();
  $("p-settings").onclick = () => api().OpenSettings();
  window.runtime.EventsOn("state", render);
  api().State().then((s) => s && s.phase && render(s));
  const panel = $("panel");
  panel.addEventListener("mouseenter", () => pauseClose(true));
  panel.addEventListener("mouseleave", () => pauseClose(false));
}

function render(s) {
  lastState = s;
  const panel = $("panel");
  const done = s.phase === "done";
  const asking = s.phase === "ask";
  panel.classList.toggle("done", done || asking);
  $("p-title").textContent = s.title || "";
  $("p-detail").textContent = done || asking ? s.message || "" : s.detail || "";

  const icon = $("p-icon");
  icon.toggleAttribute("hidden", !done && !asking);
  if (done || asking) {
    const kind = asking ? "none" : s.kind;
    icon.setAttribute("class", "status " + kind);
    icon.firstElementChild.setAttribute("href", "#" + (icons[kind] || "i-info"));
  }

  const bar = $("p-progress");
  const busy = s.busy || !s.total;
  bar.classList.toggle("busy", busy);
  bar.firstElementChild.style.width = busy ? "" : Math.round((100 * s.done) / s.total) + "%";
  $("p-count").textContent = s.total > 1 ? (done ? files(s.total) : `${s.done} z ${s.total}`) : "";

  const buttons = $("p-buttons");
  buttons.replaceChildren();
  const add = (label, fn, primary) => {
    const b = el("button", primary ? "primary" : "", label);
    b.onclick = fn;
    buttons.append(b);
  };
  if (asking) {
    $("p-count").textContent = "";
    for (const c of s.choices || []) add(c.label, () => api().Answer(c.id), c.primary);
  } else if (!done) {
    add("Anuluj", () => api().Cancel());
  } else {
    const pages = s.pages || [];
    if (pages.length === 1) add("Szukaj na napiprojekt.pl", () => api().SearchNapi(pages[0].title), true);
    if (s.output) add("Pokaż plik", () => api().ShowFile(s.output), pages.length === 0);
    if ((s.results || []).length > 1) add(expanded ? "Zwiń" : "Szczegóły", toggleDetails, pages.length > 1);
    add("Zamknij", () => api().Close());
  }
  renderResults(s);
  scheduleClose(s);
}

function renderResults(s) {
  const list = $("p-results");
  list.replaceChildren();
  for (const r of s.results || []) {
    const li = el("li", r.status);
    li.append(el("span", "dot"), el("span", "name", r.name));
    if (r.page) {
      const a = el("a", "", "Szukaj na napiprojekt.pl");
      a.onclick = () => api().SearchNapi(r.page);
      li.append(a);
    } else if (r.output && (r.status === "ok" || r.status === "synced")) {
      const a = el("a", "", "Pokaż");
      a.onclick = () => api().ShowFile(r.output);
      li.append(a);
    } else li.append(el("span"));
    if (r.text) {
      const why = el("span", "why", r.text);
      why.title = r.text;
      li.append(why);
    }
    list.append(li);
  }
  list.hidden = !expanded;
}

// files formats a Polish count: 1 plik, 3 pliki, 5 plików, 22 pliki.
function files(n) {
  const d = n % 10, t = n % 100;
  if (n === 1) return "1 plik";
  if (d >= 2 && d <= 4 && (t < 12 || t > 14)) return n + " pliki";
  return n + " plików";
}

function toggleDetails() {
  expanded = !expanded;
  render(lastState);
  if (!expanded) {
    api().Resize(156);
    return;
  }
  // Size the card to its content: everything above the list plus the list.
  requestAnimationFrame(() => {
    const list = $("p-results");
    api().Resize(Math.ceil(list.offsetTop + list.scrollHeight + 14));
  });
}

let closeLeft = 0;
let paused = false;

function scheduleClose(s) {
  if (!s.autoClose || closeTimer) return;
  closeLeft = s.autoClose * 10;
  const bar = $("p-countdown");
  bar.hidden = false;
  closeTimer = setInterval(() => {
    if (paused || expanded) return;
    closeLeft--;
    bar.firstElementChild.style.width = (closeLeft / (s.autoClose * 10)) * 100 + "%";
    if (closeLeft <= 0) {
      clearInterval(closeTimer);
      api().Close();
    }
  }, 100);
}

function pauseClose(p) {
  paused = p;
}

/* ---------------- settings ---------------- */

let current = null;

async function initSettings() {
  $("settings").hidden = false;
  $("s-version").textContent = "napi " + (await api().Version());
  document.querySelectorAll("[data-url]").forEach((a) => (a.onclick = (e) => {
    e.preventDefault();
    window.runtime.BrowserOpenURL(a.dataset.url);
  }));
  $("s-folder").onclick = (e) => {
    e.preventDefault();
    api().ConfigFolder();
  };
  $("s-cancel").onclick = () => window.runtime.Quit();
  $("s-save").onclick = save;
  $("s-test").onclick = test;
  $("s-menu").onchange = async (e) => {
    try {
      await api().SetMenu(e.target.checked);
      flash($("s-msg"), e.target.checked ? "Dodano do menu Eksploratora." : "Usunięto z menu.", "ok");
    } catch (err) {
      e.target.checked = !e.target.checked;
      flash($("s-msg"), String(err), "err");
    }
  };
  try {
    current = await api().GetSettings();
  } catch (err) {
    flash($("s-msg"), String(err), "err");
    return;
  }
  fill(current);
}

function fill(s) {
  const c = s.config;
  const langs = (c.languages || ["PL"]).join(",");
  $("s-langs").value = [...$("s-langs").options].some((o) => o.value === langs) ? langs : "PL";
  $("s-format").value = c.format;
  $("s-encoding").value = c.encoding;
  $("s-naming").value = c.naming;
  $("s-overwrite").checked = c.overwrite;
  $("s-recursive").checked = c.recursive;
  $("s-sync").checked = c.sync;
  $("s-apikey").value = c.openSubtitles.apiKey || "";
  $("s-user").value = c.openSubtitles.username || "";
  $("s-pass").value = c.openSubtitles.password || "";
  $("s-menu").checked = s.menuInstalled;
  $("s-jobs").value = c.jobs;
  $("s-fps").value = c.defaultFps;
  $("s-client").value = c.client;
}

function collect() {
  const c = structuredClone(current.config);
  c.languages = $("s-langs").value.split(",");
  c.format = $("s-format").value;
  c.encoding = $("s-encoding").value;
  c.naming = $("s-naming").value;
  c.overwrite = $("s-overwrite").checked;
  c.recursive = $("s-recursive").checked;
  c.sync = $("s-sync").checked;
  c.openSubtitles.apiKey = $("s-apikey").value.trim();
  c.openSubtitles.username = $("s-user").value.trim();
  c.openSubtitles.password = $("s-pass").value;
  c.jobs = parseInt($("s-jobs").value, 10) || 4;
  c.defaultFps = parseFloat($("s-fps").value) || 23.976;
  c.client = $("s-client").value.trim();
  return c;
}

async function save() {
  try {
    const c = collect();
    await api().SaveSettings(c);
    current.config = c;
    flash($("s-msg"), "Zapisano.", "ok");
  } catch (err) {
    flash($("s-msg"), String(err), "err");
  }
}

async function test() {
  const msg = $("s-test-msg");
  const btn = $("s-test");
  btn.disabled = true;
  msg.className = "msg";
  msg.textContent = "Łączę się…";
  try {
    msg.textContent = await api().TestOpenSubtitles(collect().openSubtitles);
    msg.className = "msg ok";
  } catch (err) {
    msg.textContent = String(err);
    msg.className = "msg err";
  } finally {
    btn.disabled = false;
  }
}

function flash(node, text, kind) {
  node.textContent = text;
  node.className = "msg " + (kind || "");
  clearTimeout(node._t);
  if (kind === "ok") node._t = setTimeout(() => (node.textContent = ""), 4000);
}
