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

  // The callsign chip mirrors the settings sidebar; sourced from the config API.
  async function loadCallsign() {
    try {
      const c = await (await fetch("/api/config")).json();
      const cs = (c.general && c.general.callsign) || "";
      if (cs) $("#side-callsign").textContent = cs;
    } catch { /* offline — leave the placeholder */ }
  }
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
  };
})();
