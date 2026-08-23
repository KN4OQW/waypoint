/* Waypoint shell chrome: the sidebar every page shares — accent theme, dark/light
   mode, language picker, callsign chip.

   It lives here rather than in app.js because there is now more than one page
   wearing this shell, and two copies of a theme toggle drift. An IIFE rather than
   top-level functions: a classic script's top-level `const $` is global-lexical,
   so declaring one in two scripts on the same page is a redeclaration error.

   The pre-paint theme application stays inline in each page's <head>, where it has
   to be to avoid a light-mode reload flashing dark. */
"use strict";

window.WPChrome = (function () {
  const $ = (sel) => document.querySelector(sel);
  const t = (key, params) => WPI18n.t(key, params);

  // Theme is shared with the settings page via localStorage "wp-theme".
  const THEMES = [
    { key: "phosphor", color: "#35d07f", attr: "" },
    { key: "amber",    color: "#f0a935", attr: "amber" },
    { key: "ice",      color: "#4db8ff", attr: "ice" },
  ];
  function applyTheme(key) {
    const th = THEMES.find((x) => x.key === key) || THEMES[0];
    if (th.attr) document.documentElement.setAttribute("data-theme", th.attr);
    else document.documentElement.removeAttribute("data-theme");
  }
  // Dark is the default; "light" is a mode that composes with the accent theme
  // (RFC-0009). Persisted separately so both survive a reload.
  function currentMode() {
    const m = localStorage.getItem("wp-mode");
    if (m) return m;
    return (window.matchMedia && matchMedia("(prefers-color-scheme: light)").matches) ? "light" : "dark";
  }
  function applyMode(mode) {
    if (mode === "light") document.documentElement.setAttribute("data-mode", "light");
    else document.documentElement.removeAttribute("data-mode");
  }
  function renderThemes() {
    const box = $("#swatches");
    box.innerHTML = "";
    const cur = localStorage.getItem("wp-theme") || "phosphor";
    const mode = currentMode();
    // Dark/Light toggle first, then the accent swatches.
    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "swatch mode-toggle" + (mode === "light" ? " light" : "");
    toggle.title = mode === "light" ? t("theme.switchToDark") : t("theme.switchToLight");
    toggle.setAttribute("aria-label", t("theme.toggleLight"));
    toggle.setAttribute("aria-pressed", String(mode === "light"));
    toggle.textContent = mode === "light" ? t("theme.light") : t("theme.dark");
    toggle.onclick = () => {
      const next = currentMode() === "light" ? "dark" : "light";
      localStorage.setItem("wp-mode", next);
      applyMode(next);
      renderThemes();
    };
    box.appendChild(toggle);
    THEMES.forEach((th) => {
      const s = document.createElement("button");
      s.type = "button";
      s.className = "swatch" + (th.key === cur ? " on" : "");
      const themeName = t("theme." + th.key);
      s.title = themeName;
      s.setAttribute("aria-label", t("theme.swatchLabel", { theme: themeName }));
      s.setAttribute("aria-pressed", String(th.key === cur));
      s.innerHTML = `<span class="dot" style="background:${th.color}" aria-hidden="true"></span>`;
      s.onclick = () => { applyTheme(th.key); localStorage.setItem("wp-theme", th.key); renderThemes(); };
      box.appendChild(s);
    });
  }

  // whoami is the shell's identity read: who is signed in, what they may do, and
  // the station callsign for the sidebar chip.
  //
  // It used to be GET /api/config, which meant a read-only account had to be handed
  // every network name, address and port on the node to paint one chip. RFC-0002
  // Amendment 1 denies viewer that route and added this one instead: three fields,
  // all of which the caller is already entitled to — their own name, their own
  // role, and the identity this node transmits in the clear on every transmission.
  //
  // The role is cached for role-aware rendering. It REPORTS what the server will
  // enforce and grants nothing: a client that lies to itself about its role still
  // gets 403s. Hiding a control the caller cannot use is a courtesy, not a check.
  let me = { username: "", role: "", callsign: "" };
  async function loadCallsign() {
    try {
      const r = await fetch("/api/whoami");
      if (!r.ok) return;                       // gate will have redirected the page
      me = await r.json();
      if (me.callsign) $("#side-callsign").textContent = me.callsign;
    } catch { /* offline — leave the placeholder */ }
  }
  // whoami is the cached answer. Callers must tolerate the empty role: the shell
  // paints before the fetch resolves, and a panel that assumed otherwise would
  // hide its controls for one frame on every load.
  function whoami() { return me; }
  function mountLanguagePicker() { WPI18n.renderPicker($("#lang-pick")); }

  // setConn paints the connection state everywhere the shell shows it: the status
  // bar LED and the sidebar chip. Every page has all four elements, and a page
  // that painted only its own would leave the sidebar reading CONNECTING forever.
  function setConn(up) {
    const led = $("#conn-led");
    if (led) led.className = "conn-led " + (up ? "up" : "down");
    const txt = $("#conn-txt");
    if (txt) txt.textContent = up ? t("status.connected") : t("status.disconnected");
    const sled = $("#side-led");
    if (sled) sled.className = "led" + (up ? "" : " down");
    const sonline = $("#side-online");
    if (sonline) sonline.textContent = up ? t("sidebar.online") : t("sidebar.offline");
  }

  // applySavedTheme is the accent the operator last chose, or the default.
  function applySavedTheme() { applyTheme(localStorage.getItem("wp-theme") || "phosphor"); }

  return {
    applyTheme: applyTheme,
    applySavedTheme: applySavedTheme,
    currentMode: currentMode,
    applyMode: applyMode,
    renderThemes: renderThemes,
    mountLanguagePicker: mountLanguagePicker,
    setConn: setConn,
    loadCallsign: loadCallsign,
    whoami: whoami,
  };
})();
