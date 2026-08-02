/* Public node page — design 1A.
 *
 * The rule this file is built around: render what the API returned, and nothing
 * else. There is no local model of what a node "has", no defaults filled in for
 * missing fields, and no element created for a key that is absent. A toggle the
 * operator turned off removes the field from the response server-side (D2), and
 * the only correct client behaviour is to notice it is not there.
 *
 * That is why almost everything below is `if (has(x)) show(...) else hide(...)`
 * rather than a template with blanks. A template would render an empty row for a
 * field the operator declined to publish, which tells a visitor the field exists
 * and that this node has nothing in it — a smaller disclosure than the value, but
 * a disclosure, and not one anybody chose to make.
 *
 * Everything is set with textContent. The only innerHTML in the file is the
 * QR library's own SVG output and the server-sanitised narrative, both marked.
 */
(function () {
  "use strict";

  var POLL_MS = 5000;      // status poll; deliberately polling, not SSE (D5)
  var SLOW_POLL_MS = 30000; // backoff once the page is hidden or erroring
  var LAST_HEARD_LIMIT = 25;

  var $ = function (id) { return document.getElementById(id); };
  var has = function (v) { return v !== undefined && v !== null && v !== ""; };

  function show(el) { if (el) el.hidden = false; }
  function hide(el) { if (el) el.hidden = true; }
  function text(el, v) { if (el) el.textContent = v == null ? "" : String(v); }

  function el(tag, cls, txt) {
    var n = document.createElement(tag);
    if (cls) n.className = cls;
    if (txt != null) n.textContent = String(txt);
    return n;
  }

  /* ---------------------------------------------------------------- fetching */

  function getJSON(path) {
    return fetch(path, { credentials: "omit", headers: { Accept: "application/json" } })
      .then(function (r) {
        // 404 is a real answer here, not an error: it is how the server says a
        // module is switched off (D2). The caller hides that module.
        if (r.status === 404) return null;
        if (!r.ok) throw new Error(path + " -> " + r.status);
        return r.json();
      });
  }

  /* ------------------------------------------------------------- formatting */

  // Frequencies arrive in Hz as configured, which is the daemons' unit and no
  // use to a human reading a sign. 438900000 -> "438.90000".
  function mhz(hz) {
    var n = Number(hz);
    if (!isFinite(n) || n <= 0) return String(hz);
    return (n / 1e6).toFixed(5);
  }

  function offsetMHz(rx, tx) {
    var a = Number(rx), b = Number(tx);
    if (!isFinite(a) || !isFinite(b) || !a || !b) return "";
    var d = (b - a) / 1e6;
    if (Math.abs(d) < 1e-6) return "simplex";
    return (d > 0 ? "+" : "−") + Math.abs(d).toFixed(3) + " MHz";
  }

  function ago(iso) {
    var t = Date.parse(iso);
    if (!isFinite(t)) return "";
    var s = Math.max(0, Math.floor((Date.now() - t) / 1000));
    if (s < 45) return "just now";
    var m = Math.round(s / 60);
    if (m < 60) return m + " min ago";
    var h = Math.floor(m / 60);
    if (h < 24) return h + " h " + (m % 60) + " min ago";
    return Math.floor(h / 24) + " d ago";
  }

  function minutesAgo(n) {
    if (n < 1) return "just now";
    if (n < 60) return n + " min ago";
    var h = Math.floor(n / 60);
    if (h < 24) return h + " h ago";
    return Math.floor(h / 24) + " d ago";
  }

  var TAG_LABELS = {
    personal_hotspot: "PERSONAL HOTSPOT",
    club_net: "CLUB NET",
    emcomm: "EMCOMM",
    net_control: "NET CONTROL",
    experimental: "EXPERIMENTAL",
    demo_event: "DEMO / EVENT"
  };

  /* ------------------------------------------------------------------ toast */

  var toastTimer = null;
  function toast(msg) {
    var t = $("toast");
    text(t, msg);
    show(t);
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { hide(t); }, 1800);
  }

  function copyable(label, value, accent) {
    var b = el("button", "stat" + (accent ? "" : ""));
    b.type = "button";
    b.appendChild(el("div", "stat-k", label));
    b.appendChild(el("div", "stat-v" + (accent ? " on" : ""), value));
    b.addEventListener("click", function () {
      // navigator.clipboard needs a secure context; a node served over plain
      // HTTP on a LAN has none, so fall back rather than throwing.
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(value).then(
          function () { toast("COPIED " + value); },
          function () { toast(value); }
        );
      } else {
        toast(value);
      }
    });
    return b;
  }

  function staticStat(label, value) {
    var d = el("div", "stat");
    d.appendChild(el("div", "stat-k", label));
    d.appendChild(el("div", "stat-v", value));
    return d;
  }

  /* ------------------------------------------------------------- reach card */

  function renderNode(n) {
    if (!n) return;

    // Identity. The brand line falls back to the callsign, because a node that
    // has not been branded still has to say who it is.
    if (has(n.callsign)) {
      text($("callsign"), n.callsign);
      show($("callsign-chip"));
      text($("brand"), n.callsign);
      text($("foot-id"), n.callsign);
      document.title = n.callsign + " · Waypoint";
    }

    var sub = [];
    if (has(n.grid)) sub.push(n.grid);
    if (Array.isArray(n.modes) && n.modes.length) sub.push(n.modes.join(" · "));
    text($("brand-sub"), sub.join(" · "));

    // Hero identity block.
    var band = [];
    if (Array.isArray(n.modes) && n.modes.length) band.push(n.modes.join(" / "));
    if (has(n.slots)) band.push(n.slots);
    text($("band"), band.join(" · "));

    if (has(n.rx_frequency)) {
      text($("freq-big"), mhz(n.rx_frequency));
      var line = [];
      line.push("RX " + mhz(n.rx_frequency));
      if (has(n.tx_frequency)) {
        line.push("TX " + mhz(n.tx_frequency));
        var off = offsetMHz(n.rx_frequency, n.tx_frequency);
        if (off) line.push(off);
      }
      if (has(n.color_code)) line.push("CC " + n.color_code);
      text($("freq-line"), line.join(" · "));
    } else if (has(n.callsign)) {
      // No frequency published: the hero still needs a subject.
      text($("freq-big"), n.callsign);
      text($("freq-line"), "");
    }

    // Purpose tags.
    var tags = $("tags");
    tags.replaceChildren();
    (n.purpose_tags || []).forEach(function (t, i) {
      tags.appendChild(el("span", "tag" + (i === 0 ? " hot" : ""), TAG_LABELS[t] || String(t).toUpperCase()));
    });
    if (has(n.purpose_freetext)) {
      text($("purpose-text"), n.purpose_freetext);
      show($("purpose-text"));
    }

    // Reach grid.
    var grid = $("reach-grid");
    grid.replaceChildren();
    if (has(n.rx_frequency)) grid.appendChild(copyable("RX / OUTPUT", mhz(n.rx_frequency), true));
    if (has(n.tx_frequency)) grid.appendChild(copyable("TX / INPUT", mhz(n.tx_frequency), false));
    var off2 = offsetMHz(n.rx_frequency, n.tx_frequency);
    if (off2) grid.appendChild(staticStat("OFFSET", off2));
    if (has(n.color_code)) grid.appendChild(staticStat("COLOR CODE", n.color_code));
    if (has(n.slots)) grid.appendChild(staticStat("TIMESLOTS", n.slots));
    if (has(n.talkgroup)) grid.appendChild(copyable("ACTIVE NOW", n.talkgroup, true));
    if (has(n.grid)) grid.appendChild(copyable("GRID", n.grid, false));
    if (grid.childElementCount) show($("reach"));

    var modesRow = $("modes-row");
    if (Array.isArray(n.modes) && n.modes.length) {
      modesRow.replaceChildren();
      n.modes.forEach(function (m) { modesRow.appendChild(el("span", "tag", m.toUpperCase())); });
      show(modesRow);
    }

    // Power / antenna: D5 says one free-text line, and that is all this is.
    if (has(n.power_line)) {
      text($("power-line"), n.power_line);
      show($("power"));
    }

    // Nets.
    if (Array.isArray(n.nets) && n.nets.length) {
      var list = $("nets-list");
      list.replaceChildren();
      n.nets.forEach(function (net) {
        var row = el("div", "net");
        row.appendChild(el("span", "net-when", net.schedule_text || ""));
        var mid = el("div", "net-name");
        mid.appendChild(document.createTextNode(net.name || ""));
        if (has(net.note)) mid.appendChild(el("div", "net-note", net.note));
        row.appendChild(mid);
        if (has(net.target)) row.appendChild(el("span", "net-target", net.target));
        list.appendChild(row);
      });
      show($("nets"));
    }

    // External links. The server validated the scheme at write time (http/https
    // only); rel is set here because the target is a site the node does not
    // control.
    if (Array.isArray(n.links) && n.links.length) {
      var lg = $("links-grid");
      lg.replaceChildren();
      n.links.forEach(function (l) {
        var a = el("a", "link", (l.label || l.url) + " ↗");
        a.href = l.url;
        a.rel = "noopener noreferrer";
        a.target = "_blank";
        lg.appendChild(a);
      });
      show($("links"));
    }

    renderBranding(n);
    renderQR(n);
  }

  /* -------------------------------------------------------------- branding */

  function renderBranding(n) {
    if (n.has_logo) {
      var img = $("logo");
      img.src = "/public/assets/logo";
      // The alt text is the node's own callsign, not "logo": a screen reader
      // announcing "logo" has told the listener nothing.
      img.alt = n.callsign ? n.callsign + " logo" : "";
      img.onload = function () { show(img); hide($("logo-ph")); };
      img.onerror = function () { hide(img); show($("logo-ph")); };
    }

    // Already rendered from Markdown and already sanitised, server-side, by
    // goldmark + bluemonday. This is the one place the page assigns innerHTML
    // from data, and it is safe for a reason that is worth stating: the string
    // never contained script by the time it left the node, the page's CSP has no
    // script-src exception that would let any that survived run, and the
    // alternative — shipping Markdown and rendering it here — would put both a
    // renderer and a sanitiser in the browser where neither can be trusted.
    if (has(n.narrative_html)) {
      $("narrative").innerHTML = n.narrative_html;
      show($("narrative"));
    }

    // The custom block is loaded through its own endpoint into a sandbox, never
    // inserted into this document. allow-scripts without allow-same-origin gives
    // the frame a unique opaque origin: it can run the operator's code and reach
    // nothing of ours.
    if (n.has_custom_block) {
      var frame = document.createElement("iframe");
      frame.className = "custom-frame";
      frame.setAttribute("sandbox", "allow-scripts");
      frame.setAttribute("loading", "lazy");
      frame.setAttribute("referrerpolicy", "no-referrer");
      frame.title = "Operator content";
      frame.src = "/public/custom-block";
      $("custom-body").replaceChildren(frame);
      show($("custom"));
    }
  }

  /* -------------------------------------------------------------------- QR */

  function renderQR(n) {
    if (typeof qrcode !== "function") return;
    var box = $("qr-code");
    if (!box) return;
    var url = window.location.origin + "/public/";
    try {
      var q = qrcode(0, "M");
      q.addData(url);
      q.make();
      // The library emits an SVG string. It is generated from a URL this page
      // built from its own origin, not from anything the operator or a visitor
      // typed, so there is no untrusted input in it.
      box.innerHTML = q.createSvgTag({ cellSize: 4, margin: 0, scalable: true });
      text($("qr-url"), url);
      show($("qr"));
    } catch (e) {
      hide($("qr"));
    }
  }

  /* ----------------------------------------------------------------- status */

  function renderStatus(s) {
    var live = $("live");
    if (!s) { hide(live); hide($("onair")); return; }

    if (s.state === "transmitting") {
      live.classList.add("tx");
      text($("live-text"), "TRANSMITTING");
      show(live);
      // D5: no callsign, no duration, no talkgroup — the fact of activity only.
      text($("onair-badge"), "RF");
      text($("onair-text"), "A station is transmitting");
      show($("onair"));
      return;
    }

    live.classList.remove("tx");
    hide($("onair"));
    if (typeof s.last_activity_minutes === "number") {
      text($("live-text"), "IDLE · LAST ACTIVITY " + minutesAgo(s.last_activity_minutes).toUpperCase());
    } else {
      text($("live-text"), "IDLE");
    }
    show(live);
  }

  /* ------------------------------------------------------------- last heard */

  function renderLastHeard(res) {
    if (!res) { hide($("lastheard")); return; }
    var body = $("lh-body");
    body.replaceChildren();

    // The station database is missing or corrupt. The server withheld the list
    // rather than serving the fraction that resolves without it, so the page
    // shows one blank record saying so and stops there.
    if (res.available === false) {
      var tr = el("tr");
      var td = el("td", "empty", res.notice || "station database unavailable");
      td.colSpan = 3;
      tr.appendChild(td);
      body.appendChild(tr);
      text($("lastheard-note"), "");
      show($("lastheard"));
      return;
    }

    var rows = res.entries || [];
    if (!rows.length) {
      var tr2 = el("tr");
      // "on RF" distinguishes this from the status line, which counts network
      // traffic as activity too. Both can be true at once, and without the
      // qualifier the pair reads as a contradiction.
      var td2 = el("td", "empty", "No stations heard on RF in the retention window.");
      td2.colSpan = 3;
      tr2.appendChild(td2);
      body.appendChild(tr2);
    }
    rows.forEach(function (h) {
      var tr3 = el("tr");
      var c1 = el("td");
      c1.appendChild(el("span", "call", h.callsign));
      tr3.appendChild(c1);
      tr3.appendChild(el("td", null, h.mode || ""));
      tr3.appendChild(el("td", "when", ago(h.at)));
      body.appendChild(tr3);
    });
    show($("lastheard"));
  }

  /* --------------------------------------------------------------- counters */

  function renderCounters(res) {
    if (!res) { hide($("counters")); return; }
    var box = $("counters-body");
    box.replaceChildren();

    if (res.available === false) {
      text($("counters-h"), "ACTIVITY");
      box.appendChild(el("div", "counter-k", res.notice || "unavailable"));
      show($("counters"));
      return;
    }
    var c = res.counters || {};
    text($("counters-h"), "HEARD LAST " + (c.window_hours || 24) + " H");

    [["STATIONS", c.callsigns, false], ["TRANSMISSIONS", c.transmissions, true]].forEach(function (p) {
      var d = el("div");
      d.appendChild(el("div", "counter-v" + (p[2] ? " on" : ""), p[1] == null ? "—" : p[1]));
      d.appendChild(el("div", "counter-k", p[0]));
      box.appendChild(d);
    });
    show($("counters"));
  }

  /* ------------------------------------------------------------------ boot */

  var pollTimer = null;
  function schedule(ms) {
    clearTimeout(pollTimer);
    pollTimer = setTimeout(tick, ms);
  }

  /* ------------------------------------------------------------------- map */

  var map = null, markers = null;

  // The map plots grid CENTRES, not received positions. The server snapped them
  // before they left the node — the raw fix is not in the response and never was
  // — so there is nothing for this code to be careful with, which is the point of
  // doing the snap server-side rather than here.
  function renderMap(res) {
    var ph = $("map-ph");
    if (!res || !Array.isArray(res.stations)) return;

    if (!res.stations.length) {
      // Say why the map is empty. A grey rectangle reads as broken.
      text($("map-ph-sub"), res.window_hours
        ? "No positions heard in the last " + res.window_hours + " h. Stations appear here when a mesh transport hears them."
        : "Locally-heard positions appear here once a mesh transport is attached.");
      show(ph);
      return;
    }
    if (typeof L !== "object" || !L || !L.map) return;

    if (!map) {
      hide(ph);
      map = L.map($("map"), {
        zoomControl: true,
        // scrollWheelZoom off: the map is a hero band inside a scrolling page,
        // and a wheel that zooms instead of scrolling traps the reader.
        scrollWheelZoom: false,
        attributionControl: true
      });
      // OpenStreetMap's tile usage policy is a condition of using these, not a
      // courtesy: attribution shown, no prefetching, ordinary caching, and no
      // bulk downloading. maxZoom 18 is theirs; detectRetina is deliberately off
      // because it doubles tile fetches for a decorative gain.
      L.tileLayer("https://tile.openstreetmap.org/{z}/{x}/{y}.png", {
        maxZoom: 18,
        attribution: '&copy; <a href="https://www.openstreetmap.org/copyright" rel="noopener noreferrer">OpenStreetMap</a> contributors'
      }).addTo(map);
      markers = L.layerGroup().addTo(map);
    }

    markers.clearLayers();
    var pts = [];
    res.stations.forEach(function (st) {
      if (typeof st.lat !== "number" || typeof st.lon !== "number") return;
      pts.push([st.lat, st.lon]);
      L.marker([st.lat, st.lon], {
        icon: L.divIcon({ className: "", iconSize: [12, 12], iconAnchor: [6, 6], html: '<div class="wp-pin"></div>' }),
        // The callsign is the accessible name; without it a screen reader
        // announces an unlabelled marker.
        alt: st.callsign || st.station || "station",
        title: (st.station || "") + " · " + (st.grid || "")
      }).addTo(markers).bindPopup(
        // The popup says the grid, not a coordinate, because the grid is what
        // this position actually is — a 5 x 3 km square, not a point.
        '<div class="wp-pop"><b>' + esc(st.station) + "</b><br>" +
        esc(st.grid) + "<br>" + esc(ago(st.heard_at)) + "</div>"
      );
    });
    if (pts.length === 1) {
      map.setView(pts[0], 9);
    } else if (pts.length) {
      map.fitBounds(L.latLngBounds(pts).pad(0.25));
    }
    setTimeout(function () { map.invalidateSize(); }, 60);
  }

  // The popup takes an HTML string, so its inputs are escaped here. They are
  // server-constrained already (callsigns are A-Z0-9, grids are validated), but
  // the escaping is unconditional rather than relying on a guarantee made in
  // another process.
  function esc(v) {
    return String(v == null ? "" : v).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  function tick() {
    // Polling rather than SSE, deliberately: the public surface stays as small as
    // possible, and a long-lived stream per anonymous visitor is a bigger one.
    Promise.all([
      getJSON("/api/public/status").catch(function () { return null; }),
      getJSON("/api/public/lastheard?limit=" + LAST_HEARD_LIMIT).catch(function () { return null; }),
      getJSON("/api/public/counters").catch(function () { return null; }),
      getJSON("/api/public/map").catch(function () { return null; })
    ]).then(function (r) {
      renderStatus(r[0]);
      renderLastHeard(r[1]);
      renderCounters(r[2]);
      renderMap(r[3]);
      schedule(document.hidden ? SLOW_POLL_MS : POLL_MS);
    }).catch(function () {
      schedule(SLOW_POLL_MS);
    });
  }

  /* --------------------------------------------------- placeholder modules */

  // Modules whose backing feature is not built yet. They render as visibly inert
  // panels that say so, rather than being hidden, because a club setting this page
  // up should be able to see what is coming and what is missing without reading a
  // roadmap.
  //
  // The operator notice banner is deliberately NOT among them. It has no content
  // source until an operator can author one, and a hardcoded activation banner —
  // the design mocks it up as "SKYWARN ACTIVATED" — would be a page announcing a
  // weather emergency that nobody declared. A placeholder that lies about an
  // emergency is worse than no placeholder.
  function renderPlaceholders() {
    show($("codeplug"));
    show($("nodes"));
  }

  function boot() {
    renderPlaceholders();
    getJSON("/api/public/node").then(renderNode).catch(function () { /* leave the shell */ });
    tick();
    // A backgrounded tab polls slowly; coming back to it refreshes at once, so
    // the first thing a returning visitor sees is current rather than stale.
    document.addEventListener("visibilitychange", function () {
      if (!document.hidden) { schedule(0); }
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", boot);
  } else {
    boot();
  }
})();
