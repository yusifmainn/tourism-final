/* Aze-Tourism — page-view beacon for the admin "Giriş loqları" tab.
   Sends one POST /api/visit per page and browser tab (sessionStorage) with
   screen, language and device hints; the server adds the IP address, the
   internet provider and the parsed device. Only our own server is contacted.
   It never throws and never blocks the page. */
(function () {
  "use strict";

  function cut(v, n) { return v == null ? undefined : String(v).slice(0, n || 300); }
  function num(v) { return typeof v === "number" && isFinite(v) ? v : undefined; }

  // Browser brand list without the random "Not A Brand" placeholder entries.
  function brandList(list) {
    var out = [];
    if (!list || !list.length) return out;
    for (var i = 0; i < list.length && out.length < 6; i++) {
      var b = list[i] || {};
      if (!b.brand || /not.?a.?brand/i.test(b.brand)) continue;
      out.push({ brand: cut(b.brand, 60), version: cut(b.version, 40) });
    }
    return out;
  }

  // Client hints (Chromium browsers). cb is called exactly once, with null when unavailable.
  function collectHints(cb) {
    var uad = navigator.userAgentData;
    if (!uad) { cb(null); return; }
    var hints = { platform: cut(uad.platform, 40), mobile: !!uad.mobile, brands: brandList(uad.brands) };
    var done = false;
    var finish = function () { if (!done) { done = true; cb(hints); } };
    try {
      var p = uad.getHighEntropyValues && uad.getHighEntropyValues(["platformVersion", "model", "architecture", "bitness", "fullVersionList"]);
      if (!p || typeof p.then !== "function") { finish(); return; }
      setTimeout(finish, 1500); // do not wait for ever
      p.then(function (h) {
        h = h || {};
        hints.platformVersion = cut(h.platformVersion, 40);
        hints.model = cut(h.model, 80);
        hints.architecture = cut(h.architecture, 20);
        hints.bitness = cut(h.bitness, 10);
        var full = brandList(h.fullVersionList);
        if (full.length) hints.brands = full;
        finish();
      }, finish);
    } catch (e) { finish(); }
  }

  function send(data) {
    try {
      fetch("/api/visit", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(data),
        keepalive: true
      }).catch(function () { /* logging must never bother the visitor */ });
    } catch (e) { /* ignore */ }
  }

  try {
    if (!window.fetch || !window.JSON) return;
    var path = String(location.pathname || "/");

    // One beacon per page per tab session.
    var key = "aze_visit:" + path;
    try {
      if (window.sessionStorage.getItem(key)) return;
      window.sessionStorage.setItem(key, "1");
    } catch (e) { /* storage blocked: send anyway */ }

    var nav = window.navigator || {};
    var conn = nav.connection || {};
    var referrer, tz;
    // Only origin + path: query strings may carry booking references.
    try { if (document.referrer) { var u = new URL(document.referrer); referrer = u.origin + u.pathname; } } catch (e) { /* ignore */ }
    try { tz = Intl.DateTimeFormat().resolvedOptions().timeZone; } catch (e) { /* ignore */ }

    var data = {
      path: cut(path),
      referrer: cut(referrer),
      lang: cut(nav.language, 40),
      tz: cut(tz, 60),
      screen: window.screen ? cut(window.screen.width + "x" + window.screen.height, 20) : undefined,
      dpr: num(window.devicePixelRatio),
      touch: num(nav.maxTouchPoints),
      cores: num(nav.hardwareConcurrency),
      memory: num(nav.deviceMemory),
      conn: cut(conn.effectiveType, 20)
    };

    collectHints(function (hints) {
      if (hints) data.hints = hints;
      send(data);
    });
  } catch (e) { /* ignore */ }
})();
