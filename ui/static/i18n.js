/* Waypoint i18n: flat-JSON message catalogs, resolved in the browser at load
   time. Plain JS in the same style as app.js/settings.js — the UI has no build
   step, so there is deliberately no extraction/compile toolchain here either.

   A catalog is one file per language under locales/, a flat map of
   dot-namespaced key -> English-shaped string, plus a reserved "_meta" object:

     { "_meta": { "name": "Deutsch", "tag": "de-DE", "reviewed": false },
       "status.connected": "verbunden", ... }

   "_meta.name" is the NATIVE-language display name — it is what the picker
   shows, so a German speaker looking for their language finds "Deutsch", not
   "German". locales/index.json is generated from those _meta blocks by
   tools/genlocaleindex; it is never hand-edited. That is what makes adding a
   language a catalog file plus a regenerated index, and nothing else (#23).

   en-US.json is the base: every key originates there, and lookup falls back to
   it key by key, so a partial translation renders as translated-where-known and
   English elsewhere — never as a blank. A key missing from BOTH catalogs
   renders as the key itself: visible in the UI and greppable in the source,
   which is what you want from a bug you are trying to find.

   Translations apply after the catalogs land, so the page paints its English
   markup first. That flash is the price of no build step; the catalogs are a
   few KB from the same origin, and the alternative (blocking first paint on a
   fetch) is worse on a Pi Zero. */
"use strict";

const WPI18n = (function () {
  const STORAGE_KEY = "wp-lang"; // sits alongside "wp-theme"/"wp-mode"
  const BASE_TAG = "en-US";
  const DIR = "locales/";

  let languages = [];    // [{tag, name, reviewed}], from locales/index.json
  let baseMsgs = {};     // the en-US catalog — the fallback for every lookup
  let msgs = {};         // the active catalog (=== baseMsgs when active is en-US)
  let activeTag = BASE_TAG;

  async function getJSON(url) {
    // no-cache revalidates rather than refetches (304 on an unchanged file), so
    // a catalog edited by an update is picked up on the next load instead of
    // living in the browser cache until a hard reload.
    const r = await fetch(url, { cache: "no-cache" });
    if (!r.ok) throw new Error(url + ": HTTP " + r.status);
    return r.json();
  }

  // negotiate picks the language to render in: an explicit stored choice wins,
  // then the browser's ordered preferences (exact tag first, then any catalog
  // sharing the primary subtag — a "de" browser gets de-DE), then the base.
  function negotiate(available) {
    const byLower = new Map(available.map((t) => [t.toLowerCase(), t]));
    const stored = localStorage.getItem(STORAGE_KEY);
    if (stored && byLower.has(stored.toLowerCase())) return byLower.get(stored.toLowerCase());
    const wanted = navigator.languages || (navigator.language ? [navigator.language] : []);
    for (const want of wanted) {
      if (!want) continue;
      const lower = String(want).toLowerCase();
      if (byLower.has(lower)) return byLower.get(lower);
      const primary = lower.split("-")[0];
      const hit = available.find((t) => t.toLowerCase().split("-")[0] === primary);
      if (hit) return hit;
    }
    return byLower.get(BASE_TAG.toLowerCase()) || available[0] || BASE_TAG;
  }

  // lookup returns a catalog's string for key, or null. Non-strings ("_meta")
  // are not messages, so they can never be returned as one.
  function lookup(catalog, key) {
    const v = catalog[key];
    return typeof v === "string" ? v : null;
  }

  const PARAM = /\{([A-Za-z0-9_]+)\}/g;

  // t resolves a key: active catalog, then en-US, then the key itself.
  // {placeholder} runs are substituted from params; a placeholder with no
  // matching param is left verbatim, so a translator who kept {version} but the
  // caller who dropped it are both visible rather than silently blank.
  function t(key, params) {
    const s = lookup(msgs, key) ?? lookup(baseMsgs, key);
    if (s === null) return key;
    if (!params) return s;
    return s.replace(PARAM, (whole, name) =>
      Object.prototype.hasOwnProperty.call(params, name) ? String(params[name]) : whole);
  }

  // applyTranslations rewrites static markup in place:
  //   data-i18n="key"                           -> textContent
  //   data-i18n-html="key"                      -> innerHTML
  //   data-i18n-attr="title:key;aria-label:key" -> attributes
  // Dynamically rendered markup (the settings.js template-literal pattern) calls
  // t() directly instead; this walk only covers what is in the HTML.
  //
  // data-i18n-html exists for the handful of sentences that wrap an element
  // mid-phrase — "arrives over <code>/api/events</code>" — where splitting the
  // sentence into fragments around the tag would hand translators a phrase they
  // cannot reorder. Catalog values are shipped source, reviewed in the same diff
  // as any other file, and the settings page already renders catalog-supplied
  // markup through note(); it is not a channel for untrusted input. Prefer
  // data-i18n unless the markup is genuinely inside the sentence.
  function applyTranslations(root) {
    const scope = root || document;
    const each = (sel, fn) => {
      if (scope.matches && scope.matches(sel)) fn(scope);
      scope.querySelectorAll(sel).forEach(fn);
    };
    each("[data-i18n]", (el) => { el.textContent = t(el.getAttribute("data-i18n")); });
    each("[data-i18n-html]", (el) => { el.innerHTML = t(el.getAttribute("data-i18n-html")); });
    each("[data-i18n-attr]", (el) => {
      for (const pair of el.getAttribute("data-i18n-attr").split(";")) {
        const sep = pair.indexOf(":");
        if (sep < 0) continue;
        const attr = pair.slice(0, sep).trim();
        const key = pair.slice(sep + 1).trim();
        if (attr && key) el.setAttribute(attr, t(key));
      }
    });
  }

  async function loadCatalog(tag) {
    if (tag === BASE_TAG) {
      msgs = baseMsgs;
      activeTag = BASE_TAG;
    } else {
      try {
        msgs = await getJSON(DIR + tag + ".json");
        activeTag = tag;
      } catch {
        // The index promised a catalog that will not load. Degrade to English
        // rather than to a page of bare keys.
        msgs = baseMsgs;
        activeTag = BASE_TAG;
      }
    }
    // Screen readers and hyphenation need the real language of the rendered text.
    document.documentElement.lang = activeTag;
  }

  // setLanguage persists the choice, swaps catalogs, re-applies static markup,
  // and announces the change so dynamically rendered pages can re-render.
  async function setLanguage(tag) {
    if (!tag || tag === activeTag) return;
    try { localStorage.setItem(STORAGE_KEY, tag); } catch { /* storage blocked — this session only */ }
    await loadCatalog(tag);
    applyTranslations(document);
    window.dispatchEvent(new CustomEvent("wp-lang-changed", { detail: { tag: activeTag } }));
  }

  function currentLanguage() { return activeTag; }
  function availableLanguages() { return languages.slice(); }

  // renderPicker fills a container with the language <select>. It lives here
  // rather than in app.js so the settings page can mount the same control
  // without a second copy of it. Hidden while only one catalog ships: an
  // English-only node should not grow a one-option dropdown.
  function renderPicker(container) {
    if (!container) return;
    container.textContent = "";
    if (languages.length < 2) { container.hidden = true; return; }
    container.hidden = false;

    const label = document.createElement("label");
    label.className = "theme-head";
    label.setAttribute("for", "lang-select");
    label.textContent = t("chrome.language");

    const sel = document.createElement("select");
    sel.id = "lang-select";
    sel.className = "lang-select";
    for (const l of languages) {
      const o = document.createElement("option");
      o.value = l.tag;
      // An unreviewed catalog is machine-assisted and awaiting a native-speaker
      // pass; say so where the operator chooses it, not in a release note.
      o.textContent = l.reviewed ? l.name : l.name + " " + t("chrome.unreviewed");
      o.selected = l.tag === activeTag;
      sel.appendChild(o);
    }
    sel.addEventListener("change", () => setLanguage(sel.value));

    container.appendChild(label);
    container.appendChild(sel);
  }

  function whenDOMReady(fn) {
    if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", fn, { once: true });
    else fn();
  }

  // Kick the loads off at parse time so they overlap with the rest of the page.
  const ready = (async function init() {
    try {
      const idx = await getJSON(DIR + "index.json");
      if (idx && Array.isArray(idx.languages)) languages = idx.languages;
    } catch { /* no index — English only, below */ }
    try {
      baseMsgs = await getJSON(DIR + BASE_TAG + ".json");
    } catch {
      // Without the base catalog every key falls through to itself. The UI is
      // ugly but functional, which beats a blank page.
      baseMsgs = {};
    }
    if (!languages.length) {
      const m = baseMsgs._meta || {};
      languages = [{ tag: BASE_TAG, name: m.name || "English (US)", reviewed: true }];
    }
    await loadCatalog(negotiate(languages.map((l) => l.tag)));
    whenDOMReady(() => applyTranslations(document));
  })();

  return { ready, t, setLanguage, currentLanguage, availableLanguages, applyTranslations, renderPicker };
})();
