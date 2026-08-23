/* Waypoint timezone picker: a shared, dependency-free component plus the pure
   logic behind the browser-timezone suggestion (issue #139).

   Two responsibilities, deliberately separated so the logic is testable without
   a DOM (ui/tests/tzpicker.test.js drives the pure functions directly):

     - Pure functions — detectTimezone / matchDetectedZone / tzSuggestion (the
       detect-and-validate flow, D1/D2/D3/D4) and filterZones / tzKeyAction (the
       filter + keyboard-navigation logic, D6). No DOM, no globals.
     - createTzPicker — the vanilla-JS combobox that renders those functions as a
       type-ahead, keyboard-navigable list. It ENHANCES an existing native
       control rather than replacing it outright, so if init throws the underlying
       <input>/<select> keeps working (progressive enhancement, D7).

   The combobox now backs two pickers: timezones, and the Weather panel's county
   picker. It was generalized rather than copied because the ARIA contract in
   here — role=combobox with aria-expanded/aria-controls, a role=listbox of
   role=option, aria-activedescendant tracking the keyboard cursor, and the
   mousedown-before-blur commit — is a merge gate, and two implementations of a
   merge gate drift. The generalization is additive: `zones` (an array of
   strings) still behaves exactly as it did, and every timezone call site is
   unchanged.

   The file keeps its name, which is now half a lie. Renaming it means touching
   settings.html, the test file and CI for no behavioural gain, so the honest
   note is here instead of in a rename.

   Two things a caller supplies for a non-timezone list:

     - `items`: [{value, label, sub}] instead of `zones`. `sub` is secondary text
       shown beside the label (the county's SAME code), never the only carrier of
       meaning.
     - `source(query) -> Promise<items>`: an ASYNC list, for a table too large to
       ship to the browser. The county table is 3,269 rows and is searched in Go,
       so the ranking lives in exactly one language instead of two.
     - `freeText`: accept a value the list does not contain. Off by default,
       because for a timezone or a county the list IS the set of valid values.
       The callsign pickers turn it on: the public ID table is an export of who
       has registered, not of who exists. See tzFreeAction.

   Plain script in the same no-build style as app.js/settings.js: it attaches a
   WPTz global for the browser and also exports for CommonJS so the Node test
   runner can require it. */
"use strict";

(function (root, factory) {
  const api = factory();
  if (typeof module !== "undefined" && module.exports) module.exports = api; // node --test
  if (root) root.WPTz = api;                                                 // browser
})(typeof window !== "undefined" ? window : null, function () {
  // detectTimezone returns the browser's IANA zone
  // (Intl.DateTimeFormat().resolvedOptions().timeZone) or null on any exception
  // or when the runtime returns nothing usable (old browsers can return "" or
  // undefined). Client-side only — there is no server round-trip (D1/D5).
  function detectTimezone() {
    try {
      const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
      return typeof tz === "string" && tz ? tz : null;
    } catch (e) {
      return null;
    }
  }

  // matchDetectedZone returns `detected` only if it appears in `zones` by a
  // CASE-SENSITIVE EXACT match, else null. The fetched list is the sole authority
  // on valid zones (D2): alias drift (Asia/Calcutta vs Asia/Kolkata), an
  // undefined detection, or a truncated list all fall through to null, and the
  // caller shows no suggestion UI. No fuzzy matching, no alias table in v1.
  function matchDetectedZone(detected, zones) {
    if (typeof detected !== "string" || !detected) return null;
    const list = Array.isArray(zones) ? zones : [];
    return list.indexOf(detected) !== -1 ? detected : null;
  }

  // tzSuggestion decides what, if anything, to offer given the detected zone, the
  // authoritative list, and the currently CONFIGURED zone. It encodes the gating
  // for D2/D3/D4 in one testable place:
  //   - {kind:"none"}    — nothing valid to suggest, OR the configured zone already
  //                        equals the detected one (D2 fallback / D4 no-op).
  //   - {kind:"prefill"} — no configured zone yet: prefill the picker, labelled (D3).
  //   - {kind:"hint"}    — a configured zone that DIFFERS from the detected one:
  //                        offer a dismissible "use it?" hint, never a silent write (D4).
  function tzSuggestion(detected, zones, configured) {
    const matched = matchDetectedZone(detected, zones);
    if (!matched) return { kind: "none", zone: null };
    const cfg = typeof configured === "string" ? configured.trim() : "";
    if (!cfg) return { kind: "prefill", zone: matched };
    if (cfg === matched) return { kind: "none", zone: matched };
    return { kind: "hint", zone: matched };
  }

  // tzNormalize lowercases and treats '_' as a space so a query typed with either
  // spaces or underscores matches the IANA name, which uses underscores
  // ("new york" and "new_york" both hit America/New_York) — D6.
  function tzNormalize(s) {
    return String(s == null ? "" : s).toLowerCase().replace(/_/g, " ");
  }

  // filterZones returns every zone whose normalized name contains the normalized
  // query as a substring (case-insensitive, underscore-as-space). An empty query
  // returns the whole list so opening the picker shows everything (D6).
  function filterZones(query, zones) {
    const list = Array.isArray(zones) ? zones : [];
    const q = tzNormalize(query).trim();
    if (!q) return list.slice();
    return list.filter((z) => tzNormalize(z).indexOf(q) !== -1);
  }

  // tzKeyAction is the keyboard-navigation reducer for the combobox listbox: a
  // pure (state, key) -> next mapping so arrow/Enter/Escape behaviour is unit
  // testable without a DOM. state = {count, active, open}; it returns the next
  // {active, open} plus `commit` — the index to select, or -1 for none. Enter with
  // no active option commits nothing, which is how "free text that matches nothing
  // selects nothing" (D6) is enforced.
  function tzKeyAction(state, key) {
    const count = (state && state.count) | 0;
    let active = state && typeof state.active === "number" ? state.active : -1;
    let open = !!(state && state.open);
    let commit = -1;
    switch (key) {
      case "ArrowDown":
        if (!count) break;
        if (!open) { open = true; active = 0; }
        else active = Math.min(active < 0 ? 0 : active + 1, count - 1);
        break;
      case "ArrowUp":
        if (!count) break;
        if (!open) { open = true; active = count - 1; }
        else active = Math.max(active - 1, 0);
        break;
      case "Home":
        if (open && count) active = 0;
        break;
      case "End":
        if (open && count) active = count - 1;
        break;
      case "Enter":
        if (open && active >= 0 && active < count) { commit = active; open = false; }
        break;
      case "Escape":
        open = false;
        active = -1;
        break;
      default:
        break;
    }
    return { active: active, open: open, commit: commit };
  }

  const NAV_KEYS = ["ArrowDown", "ArrowUp", "Home", "End", "Enter", "Escape"];

  // tzFreeAction decides what becomes of text that matched no option, when the
  // operator leaves the field or presses Enter with nothing selected.
  //
  // A timezone or a county picker DISCARDS it: those lists are the authority on
  // what a valid value is, so text that matched nothing names nothing and the
  // field goes back to what was last committed. A callsign picker cannot take
  // that position. The public ID table is a third party's export of the people
  // who have registered, not of the people who exist — an operator's own club
  // callsign may simply not be in it, and a picker that silently erased what they
  // typed because RadioID has not heard of them would be worse than no picker.
  //
  // So `freeText` callers accept the typing as the value. Three cases and they
  // are all the same rule, "commit what is there unless there is nothing to do":
  //   - not a freeText picker      -> discard, restore the committed label (D6)
  //   - typing equals what is held -> nothing to commit; no onSelect for a no-op
  //   - anything else, INCLUDING a cleared field -> commit it
  //
  // Clearing counts deliberately: emptying a callsign box means "no callsign",
  // and a picker that refused to let go of the last value would leave the form
  // holding something the operator had visibly deleted.
  function tzFreeAction(typed, committedLabel, freeText) {
    const held = typeof committedLabel === "string" ? committedLabel : "";
    if (!freeText) return { commit: false, value: held };
    const next = (typeof typed === "string" ? typed : "").trim();
    if (next === held.trim()) return { commit: false, value: held };
    return { commit: true, value: next };
  }

  // createTzPicker enhances `mount` (a container that already holds a working
  // native control — the D7 fallback) into a type-ahead combobox. It hides the
  // native control, inserts a role="combobox" input + role="listbox", and drives
  // both from filterZones/tzKeyAction. Returns a handle:
  //   setValue(zone) — commit a value through the SAME path as a manual selection,
  //                    firing onSelect (used by the D4 "use it" accept action).
  //   getValue()     — the committed value.
  //   destroy()      — remove the combobox and un-hide the native control.
  // Callers wrap this in try/catch: if it throws, the native control is untouched
  // and still submits (D7).
  // toItem accepts either a bare string (the timezone case, where the value is
  // also the label) or an object, so both list shapes reach the renderer as one.
  function toItem(x) {
    if (x && typeof x === "object") {
      const v = typeof x.value === "string" ? x.value : "";
      // `data` rides along untouched: the county picker has to store five fields
      // when one row is chosen, and re-fetching the row it just rendered to get
      // them back would be the picker asking the server what it already knows.
      return {
        value: v,
        label: typeof x.label === "string" && x.label ? x.label : v,
        sub: typeof x.sub === "string" ? x.sub : "",
        data: x.data,
      };
    }
    const s = typeof x === "string" ? x : "";
    return { value: s, label: s, sub: "", data: undefined };
  }

  function toItems(list) {
    return (Array.isArray(list) ? list : []).map(toItem);
  }

  // filterItems is filterZones over the item shape: same normalize-both-sides
  // substring rule, matched against the label, the value and the sub so a
  // county is reachable by its name or its code.
  function filterItems(query, items) {
    const q = tzNormalize(query).trim();
    if (!q) return items.slice();
    return items.filter((it) => tzNormalize(it.label + " " + it.value + " " + it.sub).indexOf(q) !== -1);
  }

  function createTzPicker(mount, opts) {
    opts = opts || {};
    const doc = mount.ownerDocument;
    // `items` is the local list; `source` replaces it with an async one. Exactly
    // one of them is in play, decided once here rather than per keystroke.
    const items = opts.items ? toItems(opts.items) : toItems(opts.zones);
    const source = typeof opts.source === "function" ? opts.source : null;
    const debounceMs = typeof opts.debounceMs === "number" ? opts.debounceMs : 150;
    let value = typeof opts.value === "string" ? opts.value : "";
    let valueLabel = typeof opts.valueLabel === "string" && opts.valueLabel ? opts.valueLabel : value;
    let matches = [];
    let active = -1;
    let open = false;
    // Every async fetch carries a sequence number and only the newest may render.
    // Without it a slow response for "sant" lands after a fast one for "santa
    // rosa" and the operator watches the list they were reading get replaced by
    // an older one.
    let seq = 0;
    let timer = null;
    // Trailing text under the options — "25 of 67 shown". Not decoration: a
    // capped list with nothing saying so reads as the whole answer, and an
    // operator whose county is number 40 concludes it is missing.
    let listNote = "";

    // Hide whatever native control already lives in the mount (the progressive-
    // enhancement fallback), keeping it in the DOM so destroy() can restore it.
    const natives = Array.prototype.slice.call(mount.children);
    natives.forEach((ch) => { ch.hidden = true; });

    const idBase = opts.idBase || "tzpick";
    const listId = idBase + "-list";

    const wrap = doc.createElement("div");
    wrap.className = "tz-combo";

    const input = doc.createElement("input");
    input.type = "text";
    input.className = "tz-input";
    input.setAttribute("role", "combobox");
    input.setAttribute("aria-expanded", "false");
    input.setAttribute("aria-autocomplete", "list");
    input.setAttribute("aria-controls", listId);
    input.setAttribute("autocomplete", "off");
    input.setAttribute("autocapitalize", "none");
    input.setAttribute("spellcheck", "false");
    if (opts.ariaLabel) input.setAttribute("aria-label", opts.ariaLabel);
    if (opts.placeholder) input.placeholder = opts.placeholder;
    input.value = valueLabel;

    const list = doc.createElement("ul");
    list.className = "tz-list";
    list.id = listId;
    list.setAttribute("role", "listbox");
    list.hidden = true;

    wrap.appendChild(input);
    wrap.appendChild(list);
    mount.appendChild(wrap);

    function optId(i) { return listId + "-opt-" + i; }

    function renderList() {
      list.innerHTML = "";
      if (!open) {
        list.hidden = true;
        input.setAttribute("aria-expanded", "false");
        input.removeAttribute("aria-activedescendant");
        return;
      }
      list.hidden = false;
      input.setAttribute("aria-expanded", "true");
      if (!matches.length) {
        // A note REPLACES the generic no-match line rather than sitting under it.
        // An empty list has more than one cause and they need different answers:
        // "keep typing, three characters at least" and "this node has never
        // downloaded the list" are not "nobody matches", and printing the generic
        // line for either would send an operator looking for a person who is
        // there. The source says which case it is; only when it has nothing to
        // add does the caller's own noMatchText stand.
        const li = doc.createElement("li");
        li.className = "tz-empty";
        li.id = listId + "-note";
        li.setAttribute("role", "presentation");
        li.textContent = listNote || opts.noMatchText || "No matches";
        list.appendChild(li);
        // Described, not merely drawn: a reason an operator cannot see is not a
        // reason they were given.
        input.setAttribute("aria-describedby", li.id);
        input.removeAttribute("aria-activedescendant");
        return;
      }
      matches.forEach((it, i) => {
        const li = doc.createElement("li");
        li.className = "tz-opt" + (i === active ? " active" : "");
        li.id = optId(i);
        li.setAttribute("role", "option");
        li.setAttribute("aria-selected", i === active ? "true" : "false");
        li.textContent = it.label;
        if (it.sub) {
          // Secondary text, never the only carrier of meaning: the label already
          // names the county, and this is the code beside it. textContent, not
          // innerHTML — these strings come from the server.
          const sub = doc.createElement("span");
          sub.className = "tz-sub";
          sub.textContent = it.sub;
          li.appendChild(sub);
        }
        // mousedown (not click) + preventDefault: commit before the input blurs,
        // so the blur handler's revert-to-committed-value never eats the pick.
        li.addEventListener("mousedown", (e) => { e.preventDefault(); commit(it); });
        list.appendChild(li);
      });
      if (listNote) {
        // role="presentation" so it is not one of the options a screen reader
        // counts or the arrow keys land on; it describes the list, it is not in
        // it. aria-describedby on the input carries it to assistive tech, which
        // a purely visual footer would not.
        const li = doc.createElement("li");
        li.className = "tz-note";
        li.id = listId + "-note";
        li.setAttribute("role", "presentation");
        li.textContent = listNote;
        list.appendChild(li);
        input.setAttribute("aria-describedby", li.id);
      } else {
        input.removeAttribute("aria-describedby");
      }
      if (active >= 0) input.setAttribute("aria-activedescendant", optId(active));
      else input.removeAttribute("aria-activedescendant");
    }

    // show installs a result set and opens the list. `keep` asks to leave the
    // cursor on the committed value if it is present, which is what focusing a
    // filled-in picker should do.
    function show(list, keep, note) {
      matches = list;
      listNote = typeof note === "string" ? note : "";
      open = true;
      let at = -1;
      if (keep) {
        for (let i = 0; i < matches.length; i++) {
          if (matches[i].value === value) { at = i; break; }
        }
      }
      active = at >= 0 ? at : (matches.length ? 0 : -1);
      renderList();
    }

    // fetch runs the async source, debounced, and drops anything but the newest
    // answer. A source that rejects leaves the list as it was rather than
    // blanking it: a dropped request is not evidence that nothing matches, and
    // showing "no matches" for one would be the picker lying about the table.
    function fetch(query, keep) {
      if (timer) { clearTimeout(timer); timer = null; }
      const mine = ++seq;
      timer = setTimeout(() => {
        timer = null;
        Promise.resolve(source(query)).then((res) => {
          if (mine !== seq) return;
          // A source may answer with a bare array, or with {items, note} when it
          // has something to say about the list as a whole.
          const arr = Array.isArray(res) ? res : (res && res.items);
          const note = (res && !Array.isArray(res) && typeof res.note === "string") ? res.note : "";
          show(toItems(arr), keep, note);
        }, () => { /* leave the previous list in place */ });
      }, debounceMs);
    }

    function refilter() {
      if (source) { fetch(input.value, false); return; }
      show(filterItems(input.value, items), false);
    }

    function openForFocus() {
      if (source) {
        // Open immediately on the list already in hand, then replace it when the
        // fetch lands. Waiting for the network to open the dropdown makes a
        // focused picker look broken on a slow node.
        show(matches, true, listNote);
        fetch(input.value, true);
        return;
      }
      show(filterItems(input.value, items), true);
    }

    function close() {
      open = false;
      active = -1;
      renderList();
    }

    // commit is the single point that changes the value — a click, an Enter, or
    // an external setValue() all land here, so D4's accept action is genuinely
    // "the same code path as a manual selection".
    function commit(v) {
      const it = toItem(v);
      value = it.value;
      valueLabel = it.label;
      input.value = it.label;
      close();
      // The first argument stays the plain value, so every timezone call site —
      // which passes a string in and expects a string back — is untouched. The
      // whole item comes second, for callers that need the fields beside it.
      if (typeof opts.onSelect === "function") opts.onSelect(it.value, it);
    }

    input.addEventListener("input", refilter);
    input.addEventListener("focus", () => { if (!open) openForFocus(); });
    input.addEventListener("keydown", (e) => {
      if (NAV_KEYS.indexOf(e.key) === -1) return;
      const res = tzKeyAction({ count: matches.length, active: active, open: open }, e.key);
      // Escape when already closed should let the event through (e.g. close a
      // parent overlay); every other handled key is ours to consume.
      if (!(e.key === "Escape" && !open)) e.preventDefault();
      // Escape discards un-selected filter text in EVERY picker, freeText or not.
      // Escape means "never mind"; it is the one key whose whole job is to undo
      // the typing, so a callsign picker taking it as a value would be reading it
      // backwards.
      if (e.key === "Escape") input.value = valueLabel;
      if (res.commit >= 0 && matches[res.commit] != null) { commit(matches[res.commit]); return; }
      // Enter with nothing selected: a freeText picker takes the typing, which is
      // how a callsign the table has never heard of gets into the field.
      if (e.key === "Enter") {
        const free = tzFreeAction(input.value, valueLabel, !!opts.freeText);
        if (free.commit) { commit(free.value); return; }
      }
      active = res.active;
      open = res.open;
      renderList();
    });
    input.addEventListener("blur", () => {
      // Text that matched no option: discarded here, or kept if the caller asked
      // for freeText. See tzFreeAction for why those are two different answers.
      const free = tzFreeAction(input.value, valueLabel, !!opts.freeText);
      if (free.commit) { commit(free.value); return; }
      input.value = free.value;
      close();
    });

    return {
      el: wrap,
      getValue: () => value,
      setValue: (v) => commit(v),
      destroy: () => {
        // Bump the sequence so an in-flight fetch cannot render into a wrapper
        // that is no longer in the document. The panel re-renders wholesale on
        // every edit, so this happens routinely rather than exceptionally.
        seq++;
        if (timer) { clearTimeout(timer); timer = null; }
        wrap.remove();
        natives.forEach((ch) => { ch.hidden = false; });
      },
    };
  }

  return {
    detectTimezone: detectTimezone,
    matchDetectedZone: matchDetectedZone,
    tzSuggestion: tzSuggestion,
    tzNormalize: tzNormalize,
    filterZones: filterZones,
    filterItems: filterItems,
    toItems: toItems,
    tzKeyAction: tzKeyAction,
    tzFreeAction: tzFreeAction,
    createTzPicker: createTzPicker,
  };
});
