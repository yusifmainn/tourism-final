/* Aze-Tourism — terms page. Renders terms content from the server
   for the current language. Content is managed from the admin panel. */
(function () {
  "use strict";
  const esc = (v) => String(v == null ? "" : v).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

  let terms = null;
  let loadFailed = false;
  function render() {
    const box = document.getElementById("termsContent");
    if (!box) return;
    if (loadFailed) { box.innerHTML = `<p>${esc(window.AzeI18n.t("load.error"))}</p>`; return; }
    if (!terms) return;
    const lang = window.AzeI18n ? window.AzeI18n.lang() : "az";
    const text = terms[lang] || terms.az || "";
    const lines = text.split("\n");
    let html = "";
    lines.forEach((raw) => {
      const line = raw.trim();
      if (!line) return;
      if (line.startsWith("## ")) html += `<h3>${esc(line.slice(3))}</h3>`;
      else if (line.startsWith("### ")) html += `<h4>${esc(line.slice(4))}</h4>`;
      else html += `<p>${esc(line)}</p>`;
    });
    box.innerHTML = html || `<p>${esc(text)}</p>`;
  }
  window.AzeStore.getTerms()
    .then((t) => { terms = t; })
    .catch((err) => { console.error("Terms:", err); loadFailed = true; })
    .then(render);
  window.addEventListener("azelangchange", render);
})();
