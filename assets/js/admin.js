/* Aze-Tourism — admin panel. Talks to the Go backend.
   The server checks the login and enforces the roles; the checks here only
   decide what to show:
     admin    -> everything: bookings, full detail + payment + card, ID document, FAQ, Terms, Statistika, Giriş loqları
     finance  -> bookings + detail incl. payment (deposit) info (no ID document)
     operator -> bookings + detail with ID document (no payment), FAQ, Terms, Statistika
   The full card number and CVV never reach the server; the server sends the card's last
   4 digits and expiry to the admin role only (b.card). Until Kapital Bank is connected
   (GET /api/config says onlinePayment:false) no money is taken online, so a pending
   deposit is shown to staff as "Alınmayıb" (not received).
   Every value from the server goes through esc() before it is put into HTML. */
(function () {
  "use strict";

  const $ = (s, c = document) => c.querySelector(s);
  const $$ = (s, c = document) => Array.from(c.querySelectorAll(s));
  const esc = (v) => String(v == null ? "" : v).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));

  const LABELS = { admin: "Admin", finance: "Maliyyə", operator: "İşçi" };
  const STATUSES = ["Yeni", "Təsdiqləndi", "İmtina"];
  // Deposit payment status -> [label, badge tone]
  const PAY_LABELS = { paid: ["Ödənilib", "ok"], pending: ["Gözləyir", "warn"], failed: ["Uğursuz", "fail"] };
  const MANUAL_METHOD = "Əl ilə";
  let role = null;
  let onlinePayment = false; // from GET /api/config on login; "not connected" is the safe default
  // Without the bank nothing is collected online, so "pending" really means "not received".
  const payLabel = (s) => (s === "pending" && !onlinePayment ? ["Alınmayıb", "warn"] : (PAY_LABELS[s] || PAY_LABELS.pending));
  const canContent = () => role === "admin" || role === "operator";
  const canPayment = () => role === "admin" || role === "finance";
  const canDocument = () => role === "admin" || role === "operator";
  const canLogs = () => role === "admin";

  const loginView = $("#loginView"), dashView = $("#dashView");

  const DATE_OPTS = { day: "2-digit", month: "2-digit", year: "numeric", hour: "2-digit", minute: "2-digit" };
  const fmtDate = (iso, withSeconds) => {
    if (!iso) return "—";
    const d = new Date(iso);
    if (isNaN(d.getTime())) return String(iso);
    try { return d.toLocaleString("az-AZ", withSeconds ? Object.assign({ second: "2-digit" }, DATE_OPTS) : DATE_OPTS); } catch (e) { return d.toISOString(); }
  };
  const api = () => window.AzeStore.admin;
  const row = (l, v) => `<div class="detail__row"><span>${l}</span><b>${v}</b></div>`;
  const dash = (v) => (v == null || v === "" ? "—" : esc(v));
  const payBadge = (s) => { const p = payLabel(PAY_LABELS[s] ? s : "pending"); return `<span class="badge badge--${p[1]}">${p[0]}</span>`; };

  // An expired session sends the user back to the login form.
  function handleError(err) {
    if (err && err.status === 401) { showLogin(); return; }
    alert((err && err.message) || "Xəta baş verdi.");
  }

  /* ================= BOOKINGS ================= */
  async function renderBookings() {
    let rows;
    try { rows = await api().bookings(); } catch (err) { handleError(err); return; }
    rows = Array.isArray(rows) ? rows : [];
    const now = new Date();
    const total = rows.length;
    const month = rows.filter((r) => { const d = new Date(r.ts); return d.getMonth() === now.getMonth() && d.getFullYear() === now.getFullYear(); }).length;
    const neu = rows.filter((r) => (r.status || "Yeni") === "Yeni").length;
    const paidSum = rows.filter((r) => r.paymentStatus === "paid").reduce((a, r) => a + (+r.mebleg || 0), 0);
    $("#stTotal").textContent = total;
    $("#stMonth").textContent = month;
    $("#stNew").textContent = neu;
    $("#stSum").textContent = paidSum.toLocaleString("az-AZ") + " ₼";
    $("#stSumTile").hidden = !canPayment();
    $("#bookingStats").classList.toggle("stats--3", !canPayment());
    $$("[data-paycol]").forEach((th) => { th.hidden = !canPayment(); });
    $("#countBadge").textContent = total + " qeyd";

    const tbody = $("#tbody"), empty = $("#emptyState"), table = $("#dataTable");
    if (!total) { table.hidden = true; empty.hidden = false; tbody.innerHTML = ""; return; }
    table.hidden = false; empty.hidden = true;
    tbody.innerHTML = rows.map((r) => `
      <tr>
        <td><strong>${esc(r.ref)}</strong></td>
        <td>${esc(r.ad)} ${esc(r.soyad)}</td>
        <td>${esc(r.tarix)}</td>
        <td><span class="badge">${esc(r.status || "Yeni")}</span></td>
        ${canPayment() ? `<td>${payBadge(r.paymentStatus)}</td>` : ""}
        <td class="nowrap">${esc(fmtDate(r.ts))}</td>
        <td><button type="button" class="linkcell" data-detail="${esc(r.ref)}">Detal</button></td>
      </tr>`).join("");
  }

  /* ================= DETAIL ================= */
  const modal = $("#detailModal");
  let detailReturnFocus = null;

  // Deposit section (admin/finance). Staff can mark a deposit received outside the
  // website, and undo only such a manual mark (the server refuses bank-confirmed ones).
  function paymentSection(b) {
    const p = b.payment || {};
    const status = PAY_LABELS[p.status] ? p.status : "pending";
    const manual = p.method === MANUAL_METHOD;
    const amount = +p.amount || +b.mebleg || 0;
    const actions =
      (status !== "paid" ? `<button type="button" class="btn btn--primary btn--sm" data-paymark="paid">Depoziti ödənilmiş kimi qeyd et</button>` : "") +
      (manual && status === "paid" ? `<button type="button" class="btn btn--danger btn--sm" data-paymark="pending">Əl ilə qeydi ləğv et</button>` : "");
    return `<div class="detail__sec">
          <h4>Ödəniş (depozit)</h4>
          <div class="detail__pay">
            ${row("Status", payBadge(status) + (status === "pending" && !onlinePayment ? " <small class=\"detail__hint\">onlayn ödəniş hələ qoşulmayıb</small>" : ""))}
            ${row("Metod", dash(p.method))}
            ${row("Məbləğ", esc(amount) + " ₼")}
            ${row("Bank sifarişi №", dash(p.bankOrderId))}
            ${b.card ? row("Kart", "•••• •••• •••• " + esc(b.card.last4)) + row("Kartın bitmə tarixi", dash(b.card.exp)) : ""}
            ${row("Ödəniş tarixi", p.paidAt ? esc(fmtDate(p.paidAt)) : "—")}
          </div>
          ${actions ? `<div class="detail__actions">${actions}</div>` : ""}
          <p class="detail__note">${onlinePayment
            ? "Kart məlumatları saytda saxlanılmır — ödəniş Kapital Bank səhifəsində aparılır."
            : "Onlayn ödəniş hələ qoşulmayıb: saytda kartdan pul çəkilmir. Tam kart nömrəsi və CVV saxlanılmır; kartın son 4 rəqəmi və bitmə tarixini yalnız Admin görür. Depozit başqa yolla alınıbsa, yuxarıdakı düymə ilə qeyd edin."}</p>
        </div>`;
  }

  async function openDetail(ref) {
    if (!role) return;
    let b = null;
    try { b = await api().booking(ref); } catch (err) { if (err.status !== 404) { handleError(err); return; } }
    const body = $("#detailBody");
    if (!b) { body.innerHTML = "<p>Qeyd tapılmadı.</p>"; }
    else {
      const opts = STATUSES.map((s) => `<option value="${s}" ${((b.status || "Yeni") === s) ? "selected" : ""}>${s}</option>`).join("");
      body.innerHTML = `
        <div class="detail__sec">
          <h4>Şəxsi məlumatlar</h4>
          ${row("Ad Soyad", esc(b.ad) + " " + esc(b.soyad))}
          ${row("Ata adı", esc(b.ata) || "—")}
          ${row("FİN", esc(b.fin))}
          ${row("E-poçt", esc(b.email))}
          ${row("Telefon", esc(b.tel))}
        </div>
        ${canDocument() ? `<div class="detail__sec">
          <h4>Şəxsiyyət vəsiqəsi (giriş icazəsi üçün)</h4>
          ${b.idImage ? `<a href="${esc(b.idImage)}" target="_blank" rel="noopener"><img src="${esc(b.idImage)}" alt="Şəxsiyyət vəsiqəsi" class="detail__idimg"></a><p class="detail__note">${esc(b.idName || "")} — böyütmək üçün şəklə klikləyin.</p>` : `<p style="color:var(--muted);font-size:.92rem">Sənəd şəkli yoxdur.</p>`}
        </div>` : ""}
        <div class="detail__sec">
          <h4>Tur</h4>
          ${row("Referans", esc(b.ref))}
          ${row("Tur tarixi", esc(b.tarix))}
          ${row("Qeydiyyat", esc(fmtDate(b.ts)))}
          <div class="detail__status"><span style="color:var(--muted)">Status:</span> <select id="stSel">${opts}</select></div>
        </div>
        <div class="detail__sec">
          <h4>Hesab (billing)</h4>
          ${row("Ünvan", esc(b.unvan) || "—")}
          ${row("Şəhər", esc(b.seher) || "—")}
          ${row("Poçt indeksi", esc(b.postal) || "—")}
        </div>
        ${canPayment() ? paymentSection(b) : `<div class="detail__sec"><h4>Ödəniş (depozit)</h4><p style="color:var(--muted);font-size:.92rem">Bu məlumatlar yalnız Admin və Maliyyə rolu üçün görünür.</p></div>`}`;
      const sel = $("#stSel");
      sel && sel.addEventListener("change", async () => {
        try { await api().setStatus(ref, sel.value); renderBookings(); } catch (err) { handleError(err); }
      });
      $$("[data-paymark]", body).forEach((btn) => btn.addEventListener("click", async () => {
        const to = btn.getAttribute("data-paymark");
        const question = to === "paid"
          ? "Depozitin sayt xaricində (nağd və ya köçürmə ilə) alındığını təsdiqləyirsiniz? Rezervasiya ödənilmiş kimi qeyd olunacaq."
          : "Əl ilə edilmiş ödəniş qeydi ləğv edilsin? Depozit yenidən “" + payLabel("pending")[0] + "” statusunda olacaq.";
        if (!confirm(question)) return;
        btn.disabled = true;
        try { await api().setPaymentStatus(ref, to); } catch (err) { btn.disabled = false; handleError(err); return; }
        renderBookings();
        openDetail(ref); // show the new payment state
      }));
    }
    if (modal.hidden) {
      detailReturnFocus = document.activeElement;
      modal.hidden = false; document.body.style.overflow = "hidden";
      $(".modal__close", modal).focus();
    }
  }
  function closeDetail() {
    if (modal.hidden) return;
    modal.hidden = true; document.body.style.overflow = "";
    if (detailReturnFocus && document.contains(detailReturnFocus)) detailReturnFocus.focus();
    detailReturnFocus = null;
  }
  $$("[data-dclose]", modal).forEach((el) => el.addEventListener("click", closeDetail));
  document.addEventListener("keydown", (e) => { if (e.key === "Escape") { closeDetail(); closeLog(); } });
  $("#tbody").addEventListener("click", (e) => { const b = e.target.closest("[data-detail]"); if (b) openDetail(b.getAttribute("data-detail")); });

  /* ================= GİRİŞ LOQLARI (admin only) ================= */
  const KIND_LABELS = { visit: ["Sayt girişi", ""], login_ok: ["Admin girişi — uğurlu", "ok"], login_fail: ["Admin girişi — uğursuz", "fail"] };
  const DEVICE_LABELS = { desktop: "Kompüter", mobile: "Mobil", tablet: "Planşet", bot: "Bot", unknown: "Naməlum" };
  const deviceLabel = (t) => DEVICE_LABELS[t] || DEVICE_LABELS.unknown;
  const kindBadge = (k) => { const l = KIND_LABELS[k] || [k || "—", ""]; return `<span class="badge${l[1] ? " badge--" + l[1] : ""}">${esc(l[0])}</span>`; };
  const asnText = (v) => { const a = String(v == null ? "" : v).trim(); return a && !/^AS/i.test(a) ? "AS" + a : a; };
  const vState = { page: 1, size: 50, kind: "all", q: "", items: [], total: 0 };
  let vSeq = 0; // ignore answers to older requests
  let vSearchTimer = null;

  async function renderVisits() {
    if (!canLogs()) return;
    const seq = ++vSeq;
    let res;
    try { res = await api().visits({ page: vState.page, size: vState.size, kind: vState.kind, q: vState.q }); }
    catch (err) { if (seq === vSeq) handleError(err); return; }
    if (seq !== vSeq || !res) return;
    const st = res.stats || {};
    const n = (v) => (Number(v) || 0).toLocaleString("az-AZ");
    $("#vToday").textContent = n(st.today);
    $("#vUnique").textContent = n(st.todayUniqueIPs);
    $("#vWeek").textContent = n(st.week);
    $("#vFailed").textContent = n(st.failedLogins24h);
    $("#vFailedTile").classList.toggle("stat--alert", (Number(st.failedLogins24h) || 0) > 0);

    vState.items = Array.isArray(res.items) ? res.items : [];
    vState.total = Number(res.total) || 0;
    if (Number(res.page) > 0) vState.page = Number(res.page);
    const pages = Math.max(1, Math.ceil(vState.total / vState.size));
    $("#vCount").textContent = vState.total.toLocaleString("az-AZ") + " qeyd";
    $("#vPageInfo").textContent = "Səhifə " + vState.page + " / " + pages;
    $("#vPrev").disabled = vState.page <= 1;
    $("#vNext").disabled = vState.page >= pages;

    const table = $("#vTable"), empty = $("#vEmpty"), pager = $("#vPager"), tbody = $("#vBody");
    if (!vState.items.length) { tbody.innerHTML = ""; table.hidden = true; empty.hidden = false; pager.hidden = vState.page <= 1; return; }
    table.hidden = false; empty.hidden = true; pager.hidden = false;
    tbody.innerHTML = vState.items.map((v, i) => {
      const net = [asnText(v.asn), v.country].filter(Boolean).map(esc).join(" · ");
      return `
      <tr>
        <td class="nowrap">${esc(fmtDate(v.ts, true))}</td>
        <td>${kindBadge(v.kind)}${v.username ? `<small class="cellsub">${esc(v.username)}</small>` : ""}</td>
        <td class="mono">${dash(v.ip)}</td>
        <td>${dash(v.provider)}${net ? `<small class="cellsub">${net}</small>` : ""}</td>
        <td>${v.device ? `${esc(v.device)}<small class="cellsub">${esc(deviceLabel(v.deviceType))}</small>` : esc(deviceLabel(v.deviceType))}</td>
        <td>${esc([v.os, v.browser].filter(Boolean).join(" · ")) || "—"}</td>
        <td class="logs__path">${dash(v.path)}</td>
        <td><button type="button" class="linkcell" data-vidx="${i}">Detal</button></td>
      </tr>`;
    }).join("");
  }
  function reloadVisits(firstPage) { if (firstPage) vState.page = 1; renderVisits(); }

  $("#vFilters").addEventListener("submit", (e) => { e.preventDefault(); clearTimeout(vSearchTimer); vState.q = $("#vSearch").value.trim(); reloadVisits(true); });
  $("#vSearch").addEventListener("input", () => {
    clearTimeout(vSearchTimer);
    vSearchTimer = setTimeout(() => { const q = $("#vSearch").value.trim(); if (q !== vState.q) { vState.q = q; reloadVisits(true); } }, 400);
  });
  $("#vKind").addEventListener("change", () => { vState.kind = $("#vKind").value; reloadVisits(true); });
  $("#vRefresh").addEventListener("click", () => reloadVisits(false));
  $("#vPrev").addEventListener("click", () => { if (vState.page > 1) { vState.page--; renderVisits(); } });
  $("#vNext").addEventListener("click", () => { vState.page++; renderVisits(); });

  // Detail modal: every field of one log row, incl. the raw User-Agent.
  const logModal = $("#logModal");
  let logReturnFocus = null;
  function openLog(v) {
    if (!v) return;
    const isLogin = v.kind === "login_ok" || v.kind === "login_fail";
    $("#logBody").innerHTML = `
      <div class="detail__sec">
        <h4>Hadisə</h4>
        ${row("Vaxt", esc(fmtDate(v.ts, true)))}
        ${row("Növ", kindBadge(v.kind))}
        ${isLogin || v.username ? row("İstifadəçi adı", dash(v.username)) : ""}
        ${row("Səhifə", dash(v.path))}
        ${row("Haradan gəlib", dash(v.referrer))}
      </div>
      <div class="detail__sec">
        <h4>Şəbəkə</h4>
        ${row("IP ünvanı", `<span class="mono">${dash(v.ip)}</span>`)}
        ${row("Provayder", dash(v.provider))}
        ${row("ASN", dash(asnText(v.asn)))}
        ${row("Ölkə", dash(v.country))}
        ${row("Host adı (PTR)", dash(v.hostname))}
      </div>
      <div class="detail__sec">
        <h4>Cihaz</h4>
        ${row("Cihaz tipi", esc(deviceLabel(v.deviceType)))}
        ${row("Cihaz", dash(v.device))}
        ${row("Əməliyyat sistemi", dash(v.os))}
        ${row("Brauzer", dash(v.browser))}
        ${row("Ekran", dash(v.screen))}
        ${row("Avadanlıq", dash(v.hardware))}
        ${row("Bağlantı", dash(v.connection))}
        ${row("Dil", dash(v.language))}
        ${row("Saat qurşağı", dash(v.timezone))}
      </div>
      <div class="detail__sec">
        <h4>User-Agent</h4>
        <code class="logdetail__ua">${dash(v.userAgent)}</code>
      </div>`;
    if (logModal.hidden) {
      logReturnFocus = document.activeElement;
      logModal.hidden = false; document.body.style.overflow = "hidden";
      $(".modal__close", logModal).focus();
    }
  }
  function closeLog() {
    if (logModal.hidden) return;
    logModal.hidden = true; document.body.style.overflow = "";
    if (logReturnFocus && document.contains(logReturnFocus)) logReturnFocus.focus();
    logReturnFocus = null;
  }
  $$("[data-lclose]", logModal).forEach((el) => el.addEventListener("click", closeLog));
  $("#vBody").addEventListener("click", (e) => { const b = e.target.closest("[data-vidx]"); if (b) openLog(vState.items[+b.getAttribute("data-vidx")]); });

  /* ================= FAQ management ================= */
  let editingId = null;
  let faqItems = [];
  let faqCloseTimer = null;
  async function renderFaqAdmin() {
    try { faqItems = await window.AzeStore.getFaq(); } catch (err) { handleError(err); return; }
    const list = $("#faqAdminList");
    list.innerHTML = faqItems.map((it) => `
      <div class="faq-admin-item">
        <div><div class="q">${esc(it.q.az || "")}</div><div class="a">${esc(it.a.az || "")}</div></div>
        <div class="faq-admin-actions">
          <button class="btn btn--outline btn--sm" data-faqedit="${esc(it.id)}">Redaktə</button>
          <button class="btn btn--danger btn--sm" data-faqdel="${esc(it.id)}">Sil</button>
        </div>
      </div>`).join("") || '<p style="color:var(--muted)">Hələ sual yoxdur.</p>';
  }
  const faqEditor = $("#faqEditor");
  function fillFaqEditor(it) {
    $("#fq_az").value = it ? (it.q.az || "") : ""; $("#fa_az").value = it ? (it.a.az || "") : "";
    $("#fq_ru").value = it ? (it.q.ru || "") : ""; $("#fa_ru").value = it ? (it.a.ru || "") : "";
    $("#fq_en").value = it ? (it.q.en || "") : ""; $("#fa_en").value = it ? (it.a.en || "") : "";
    setEditorLang("#faqLangTabs", "elang", "az");
  }
  function openFaqEditor(it) { clearTimeout(faqCloseTimer); $("#faqSave").disabled = false; editingId = it ? it.id : null; $("#faqEditorTitle").textContent = it ? "Sualı redaktə et" : "Sual əlavə et"; fillFaqEditor(it); faqEditor.hidden = false; $("#faqSaved").hidden = true; faqEditor.scrollIntoView({ behavior: "smooth", block: "nearest" }); }
  $("#faqAdd").addEventListener("click", () => openFaqEditor(null));
  $("#faqCancel").addEventListener("click", () => { faqEditor.hidden = true; editingId = null; });
  $("#faqAdminList").addEventListener("click", async (e) => {
    const ed = e.target.closest("[data-faqedit]"), del = e.target.closest("[data-faqdel]");
    if (ed) { const it = faqItems.find((x) => x.id === ed.getAttribute("data-faqedit")); openFaqEditor(it); }
    if (del) {
      if (!confirm("Bu sualı silmək istədiyinizə əminsiniz?")) return;
      const id = del.getAttribute("data-faqdel");
      try { await api().deleteFaq(id); } catch (err) { handleError(err); return; }
      if (editingId === id) { faqEditor.hidden = true; editingId = null; }
      renderFaqAdmin();
    }
  });
  $("#faqSave").addEventListener("click", async () => {
    const item = {
      q: { az: $("#fq_az").value.trim(), ru: $("#fq_ru").value.trim(), en: $("#fq_en").value.trim() },
      a: { az: $("#fa_az").value.trim(), ru: $("#fa_ru").value.trim(), en: $("#fa_en").value.trim() }
    };
    if (!item.q.az && !item.q.ru && !item.q.en) { alert("Ən azı bir dildə sual daxil edin."); return; }
    const saveBtn = $("#faqSave");
    saveBtn.disabled = true; // until the editor closes, so a double click cannot add the question twice
    try {
      if (editingId) await api().updateFaq(editingId, item);
      else await api().addFaq(item);
    } catch (err) { saveBtn.disabled = false; handleError(err); return; }
    await renderFaqAdmin();
    $("#faqSaved").hidden = false;
    faqCloseTimer = setTimeout(() => { faqEditor.hidden = true; editingId = null; saveBtn.disabled = false; }, 700);
  });

  /* ================= Terms editor ================= */
  async function loadTermsEditor() {
    let t;
    try { t = await window.AzeStore.getTerms(); } catch (err) { handleError(err); return; }
    $("#tt_az").value = t.az || ""; $("#tt_ru").value = t.ru || ""; $("#tt_en").value = t.en || "";
    setEditorLang("#termsLangTabs", "tlang", "az");
  }
  $("#termsSave").addEventListener("click", async () => {
    try { await api().saveTerms({ az: $("#tt_az").value, ru: $("#tt_ru").value, en: $("#tt_en").value }); } catch (err) { handleError(err); return; }
    $("#termsSaved").hidden = false; setTimeout(() => ($("#termsSaved").hidden = true), 1800);
  });

  /* ================= Stats editor (home page trust strip) ================= */
  // Four fixed positions; the server checks the limits again. Values are set with .value,
  // never through innerHTML, so nothing typed here can turn into markup.
  const STAT_FIELDS = [["value", "Rəqəm", 20], ["az", "Mətn (AZ)", 80], ["ru", "Текст (RU)", 80], ["en", "Text (EN)", 80]];
  function buildStatsRows() {
    const box = $("#statsRows");
    if (box.childElementCount) return;
    box.innerHTML = [1, 2, 3, 4].map((n) => `
      <div class="stat-edit" role="group" aria-label="Göstərici ${n}">
        ${STAT_FIELDS.map(([f, label, max]) => `<label class="stat-edit__field"><span class="stat-edit__label">${label}</span><input type="text" id="st_${f}_${n}" maxlength="${max}" /></label>`).join("")}
      </div>`).join("");
  }
  async function loadStatsEditor() {
    buildStatsRows();
    let stats;
    try { stats = await window.AzeStore.getStats(); } catch (err) { handleError(err); return; }
    (Array.isArray(stats) ? stats : []).slice(0, 4).forEach((st, i) => {
      const n = i + 1, label = st.label || {};
      $(`#st_value_${n}`).value = st.value || "";
      $(`#st_az_${n}`).value = label.az || ""; $(`#st_ru_${n}`).value = label.ru || ""; $(`#st_en_${n}`).value = label.en || "";
    });
  }
  $("#statsSave").addEventListener("click", async () => {
    const stats = [1, 2, 3, 4].map((n) => ({
      value: $(`#st_value_${n}`).value.trim(),
      label: { az: $(`#st_az_${n}`).value.trim(), ru: $(`#st_ru_${n}`).value.trim(), en: $(`#st_en_${n}`).value.trim() }
    }));
    const missing = stats.findIndex((s) => s.value && !s.label.az);
    if (missing >= 0) { alert(`${missing + 1}-ci göstəricinin AZ mətnini yazın.`); $(`#st_az_${missing + 1}`).focus(); return; }
    const btn = $("#statsSave");
    btn.disabled = true;
    try { await api().saveStats(stats); } catch (err) { btn.disabled = false; handleError(err); return; }
    btn.disabled = false;
    $("#statsSaved").hidden = false; setTimeout(() => ($("#statsSaved").hidden = true), 1800);
  });

  /* ---- editor language sub-tabs (az/ru/en) ---- */
  function setEditorLang(tabsSel, attr, lang) {
    const tabs = $(tabsSel);
    $$(".lang-tab", tabs).forEach((b) => b.classList.toggle("is-active", b.dataset[attr] === lang));
    const scope = tabs.closest(".editor") || tabs.closest(".admin-pane");
    $$(`.lang-fields[data-${attr}]`, scope).forEach((f) => f.classList.toggle("is-active", f.dataset[attr] === lang));
  }
  document.addEventListener("click", (e) => {
    const t = e.target.closest(".lang-tab");
    if (!t) return;
    if (t.dataset.elang) setEditorLang("#faqLangTabs", "elang", t.dataset.elang);
    if (t.dataset.tlang) setEditorLang("#termsLangTabs", "tlang", t.dataset.tlang);
  });

  /* ================= Tabs ================= */
  function switchTab(name) {
    if ((name === "faq" || name === "terms" || name === "stats") && !canContent()) name = "bookings";
    if (name === "visits" && !canLogs()) name = "bookings";
    $$(".admin-tab").forEach((t) => t.classList.toggle("is-active", t.dataset.tab === name));
    $$(".admin-pane").forEach((p) => p.classList.toggle("is-active", p.dataset.pane === name));
    if (name === "visits") renderVisits(); // fresh data every time the tab is opened
  }
  $("#tabs").addEventListener("click", (e) => { const t = e.target.closest(".admin-tab"); if (t) switchTab(t.dataset.tab); });

  /* ================= Auth ================= */
  function showDash(r) {
    role = r;
    loginView.hidden = true; dashView.hidden = false;
    $("#roleBadge").textContent = LABELS[r] || r;
    $$(".admin-tab[data-op]").forEach((t) => (t.hidden = !canContent()));
    $$(".admin-tab[data-admin]").forEach((t) => (t.hidden = !canLogs()));
    // A new login starts the log view from scratch.
    Object.assign(vState, { page: 1, kind: "all", q: "", items: [], total: 0 });
    $("#vSearch").value = ""; $("#vKind").value = "all"; $("#vBody").innerHTML = "";
    switchTab("bookings");
    // The labels depend on whether online payment is connected; on failure keep the last value.
    window.AzeStore.getConfig()
      .then((c) => { if (c && typeof c.onlinePayment === "boolean") onlinePayment = c.onlinePayment; })
      .catch(() => {})
      .then(renderBookings);
    if (canContent()) { renderFaqAdmin(); loadTermsEditor(); loadStatsEditor(); }
  }
  function showLogin() {
    closeDetail();
    closeLog();
    dashView.hidden = true; loginView.hidden = false;
    $("#pass").value = "";
    role = null;
    vSeq++; // drop answers that are still on their way
  }

  $("#loginForm").addEventListener("submit", async (e) => {
    e.preventDefault();
    const box = $("#loginErr");
    try {
      const me = await api().login($("#user").value.trim().toLowerCase(), $("#pass").value);
      box.hidden = true;
      showDash(me.role);
    } catch (err) {
      box.textContent = err.message || "Xəta baş verdi.";
      box.hidden = false;
    }
  });
  $("#logout").addEventListener("click", async () => {
    try { await api().logout(); } catch (err) { console.error("Logout:", err); }
    showLogin();
  });
  $("#refresh").addEventListener("click", renderBookings);

  // Reopen the dashboard if the session cookie is still valid.
  api().me().then((me) => showDash(me.role)).catch(() => {});
})();
