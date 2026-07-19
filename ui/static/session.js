/* Shared session client for the authenticated app pages (dashboard, settings).
   RFC-0002 sessions expire on idle and can be revoked (logout, reset-claim); when
   that happens a gated API call comes back 401. This centralises the response so no
   page has to check for it at every call site: any 401 routes the operator to the
   login screen, remembering where they were so login can return them.

   Loaded ONLY by the in-app pages — never by the pre-auth screen (auth.html), where
   a 401 from POST /api/session means "wrong password", not "session expired". */
"use strict";

(function () {
  // One redirect per page life. location.assign unloads the page, but in-flight
  // callers may still resolve first; the flag stops a burst of 401s from each
  // stacking a navigation.
  var redirecting = false;

  // toLogin sends the operator to the pre-auth screen, preserving the current
  // location (path + query + hash) as ?next so a successful login returns them to
  // exactly where the session died — including the settings tab in the hash.
  function toLogin() {
    if (redirecting) return;
    redirecting = true;
    var here = location.pathname + location.search + location.hash;
    location.assign("/?next=" + encodeURIComponent(here));
  }

  // Wrap fetch so every gated call funnels its 401 through toLogin(). The response
  // is still returned, so existing callers keep working on the success path; on a
  // 401 the navigation is already scheduled and their error branch is moot.
  var rawFetch = window.fetch.bind(window);
  window.fetch = function (input, init) {
    return rawFetch(input, init).then(function (resp) {
      if (resp.status === 401) toLogin();
      return resp;
    });
  };

  // The SSE stream (EventSource) cannot read its own 401 — the browser hides the
  // status and just fires onerror, then silently retries. So on a stream error the
  // dashboard calls this to probe a gated route: if the session is gone the probe
  // 401s and the wrapped fetch above routes to login; a transient blip returns ok
  // and the EventSource reconnect is left to carry on. This is what turns an expired
  // session into a visible re-auth instead of a dead, never-updating dashboard.
  function reauthCheck() {
    return rawFetch("/api/config").then(function (resp) {
      if (resp.status === 401 || resp.status === 403) { toLogin(); return false; }
      return resp.ok;
    }).catch(function () { return true; }); // network blip, not an auth failure
  }

  // Logout: revoke the session server-side (DELETE actually deletes the record, it
  // does not merely drop the cookie — RFC-0002), then go to the login screen. Wired
  // to the persistent-nav control on every in-app page.
  function logout() {
    return rawFetch("/api/session", { method: "DELETE" }).catch(function () {})
      .then(function () { redirecting = true; location.assign("/"); });
  }

  function wireLogout() {
    var btn = document.getElementById("logout-btn");
    if (btn) btn.addEventListener("click", logout);
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", wireLogout);
  } else {
    wireLogout();
  }

  window.wpSession = { toLogin: toLogin, reauthCheck: reauthCheck, logout: logout };
})();
