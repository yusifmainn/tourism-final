/* Aze-Tourism — home page interactions */
(function () {
  "use strict";
  const $ = (s, c = document) => c.querySelector(s);
  const $$ = (s, c = document) => Array.from(c.querySelectorAll(s));
  const esc = (v) => String(v == null ? "" : v).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

  // Reveal on scroll
  const reveals = $$(".reveal");
  if ("IntersectionObserver" in window && reveals.length) {
    const io = new IntersectionObserver((entries) => { entries.forEach((e) => { if (e.isIntersecting) { e.target.classList.add("in"); io.unobserve(e.target); } }); }, { threshold: 0.12, rootMargin: "0px 0px -40px 0px" });
    reveals.forEach((el) => io.observe(el));
  } else { reveals.forEach((el) => el.classList.add("in")); }

  // FAQ from the server (current language)
  let faq = [];
  let loadFailed = false;
  function renderFaq() {
    const list = $("#faqList");
    if (!list) return;
    if (loadFailed) { list.innerHTML = `<p>${esc(window.AzeI18n.t("load.error"))}</p>`; return; }
    const lang = window.AzeI18n ? window.AzeI18n.lang() : "az";
    list.innerHTML = faq.map((it) => {
      const q = (it.q && (it.q[lang] || it.q.az)) || "";
      const a = (it.a && (it.a[lang] || it.a.az)) || "";
      return `<details class="faq__item"><summary>${esc(q)}</summary><p>${esc(a)}</p></details>`;
    }).join("");
  }
  window.AzeStore.getFaq()
    .then((items) => { faq = items; })
    .catch((err) => { console.error("FAQ:", err); loadFailed = true; })
    .then(renderFaq);
  window.addEventListener("azelangchange", renderFaq);

  // Trust strip figures, edited in the admin panel ("Statistika"). Until they load (or if the
  // request fails) the figures written in index.html stay. An empty value hides that position.
  let stats = null;
  function renderStats() {
    if (!stats) return;
    const lang = window.AzeI18n ? window.AzeI18n.lang() : "az";
    stats.forEach((st, i) => {
      const item = $(`[data-stat="${i + 1}"]`);
      if (!item) return;
      const value = (st && st.value) || "";
      item.hidden = !value;
      if (!value) return;
      const label = (st.label && (st.label[lang] || st.label.az)) || "";
      $("b", item).textContent = value;
      const span = $("span", item);
      span.removeAttribute("data-i18n"); // the text now comes from the server, not i18n.js
      span.textContent = label;
    });
  }
  window.AzeStore.getStats()
    .then((items) => { if (Array.isArray(items)) { stats = items; renderStats(); } })
    .catch((err) => console.error("Stats:", err));
  window.addEventListener("azelangchange", renderStats);
})();
