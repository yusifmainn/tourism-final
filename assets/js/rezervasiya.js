/* Aze-Tourism — rezervasiya (booking) flow. Sends the booking to the Go backend.
   With Kapital Bank connected (GET /api/config onlinePayment:true) the customer is
   sent to the bank's hosted payment page for the deposit and comes back to
   /rezervasiya/?ref=…, where this page shows GET /api/bookings/{ref}/payment.
   Until the bank is connected, step 3 shows card fields that are checked in the browser;
   nothing is charged, the booking is saved and the success screen is shown. Only the
   card's last 4 digits and expiry go to the server (visible to the admin role only);
   the full card number and the CVV never leave this page. */
(function () {
  "use strict";

  const $ = (s, c = document) => c.querySelector(s);
  const $$ = (s, c = document) => Array.from(c.querySelectorAll(s));
  const I = window.AzeI18n;
  const t = (k) => (I ? I.t(k) : k);
  const lang = () => (I ? I.lang() : "az");
  let idImageData = null; // compressed data URL of the uploaded ID card (shown in admin)
  let idImageName = "";

  /* ---------- Phone: country codes + per-country rules ---------- */
  const COUNTRIES = [
    { iso: "AZ", dial: "+994", len: 9, ex: "50 123 45 67", name: { az: "Azərbaycan", ru: "Азербайджан", en: "Azerbaijan" } },
    { iso: "TR", dial: "+90", len: 10, ex: "5XX XXX XX XX", name: { az: "Türkiyə", ru: "Турция", en: "Turkey" } },
    { iso: "GE", dial: "+995", len: 9, ex: "5XX XX XX XX", name: { az: "Gürcüstan", ru: "Грузия", en: "Georgia" } },
    { iso: "RU", dial: "+7", len: 10, ex: "9XX XXX XX XX", name: { az: "Rusiya", ru: "Россия", en: "Russia" } },
    { iso: "IR", dial: "+98", len: 10, ex: "9XX XXX XXXX", name: { az: "İran", ru: "Иран", en: "Iran" } }
  ];
  const country = () => COUNTRIES.find((c) => c.iso === telCode.value) || COUNTRIES[0];

  const ERR = {
    az: { req: "Bu xana tələb olunur", fin: "FİN 7 simvol olmalıdır", email: "Düzgün e-poçt daxil edin", phone: "Nömrə {n} rəqəmdən ibarət olmalıdır", date: "Tur tarixini seçin", file: "Şəkli yükləyin", terms: "Şərtləri qəbul etməlisiniz", ftype: "Yalnız şəkil faylı yükləyin", fsize: "Fayl 8 MB-dan böyükdür", card: "Kart nömrəsi 16 rəqəm olmalıdır", exp: "Etibarlı bitmə tarixi yazın (AA/İL)", cvv: "CVV 3–4 rəqəm olmalıdır", invalid: "Məlumatı yoxlayıb yenidən cəhd edin", server: "Rezervasiya göndərilə bilmədi. Bir az sonra yenidən yoxlayın.", busy: "Çox sayda sorğu göndərilib. Bir az sonra yenidən yoxlayın.", payfail: "Ödəniş səhifəsini açmaq alınmadı. Bir az sonra yenidən yoxlayın.", checkfail: "Ödəniş statusunu yoxlamaq alınmadı. Yenidən yoxlayın." },
    ru: { req: "Поле обязательно", fin: "FİN должен содержать 7 символов", email: "Введите корректный e-mail", phone: "Номер должен содержать {n} цифр", date: "Выберите дату тура", file: "Загрузите фото", terms: "Примите условия", ftype: "Только файл изображения", fsize: "Файл больше 8 МБ", card: "Номер карты должен содержать 16 цифр", exp: "Укажите действующий срок (ММ/ГГ)", cvv: "CVV должен содержать 3–4 цифры", invalid: "Проверьте данные и повторите попытку", server: "Не удалось отправить бронирование. Повторите попытку позже.", busy: "Слишком много запросов. Повторите попытку позже.", payfail: "Не удалось открыть страницу оплаты. Повторите попытку позже.", checkfail: "Не удалось проверить статус оплаты. Попробуйте ещё раз." },
    en: { req: "This field is required", fin: "FIN must be 7 characters", email: "Enter a valid e-mail", phone: "Number must be {n} digits", date: "Select the tour date", file: "Upload the photo", terms: "Accept the terms", ftype: "Image files only", fsize: "File larger than 8 MB", card: "Card number must be 16 digits", exp: "Enter a valid expiry date (MM/YY)", cvv: "CVV must be 3–4 digits", invalid: "Check this and try again", server: "The booking could not be sent. Please try again shortly.", busy: "Too many requests. Please try again shortly.", payfail: "The payment page could not be opened. Please try again shortly.", checkfail: "Could not check the payment status. Please try again." }
  };
  const e = (k) => (ERR[lang()] || ERR.az)[k];

  const telCode = $("#telCode"), tel = $("#tel");
  function renderCountries() {
    const cur = telCode.value || "AZ";
    telCode.innerHTML = COUNTRIES.map((c) => `<option value="${c.iso}">${c.name[lang()] || c.name.az} (${c.dial})</option>`).join("");
    telCode.value = cur;
    tel.placeholder = country().ex;
  }
  telCode.addEventListener("change", () => { tel.placeholder = country().ex; tel.value = tel.value.slice(0, country().len); clearError("tel"); });
  tel.addEventListener("input", () => { tel.value = tel.value.replace(/\D/g, "").slice(0, country().len); });

  /* ---------- Calendar ---------- */
  const grid = $("#calGrid"), title = $("#calTitle"), rangeEl = $("#dateRange"), dowEl = $("#calDow");
  let selected = null;
  const today = new Date(); today.setHours(0, 0, 0, 0);
  let view = new Date(today.getFullYear(), today.getMonth(), 1);
  const sameDay = (a, b) => a && b && a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
  const months = () => I.months(lang());

  function renderDow() { dowEl.innerHTML = I.dow(lang()).map((d) => `<span>${d}</span>`).join(""); }
  function renderCalendar() {
    if (!grid) return;
    title.textContent = `${months()[view.getMonth()]} ${view.getFullYear()}`;
    grid.innerHTML = "";
    const lead = (new Date(view.getFullYear(), view.getMonth(), 1).getDay() + 6) % 7;
    const days = new Date(view.getFullYear(), view.getMonth() + 1, 0).getDate();
    const night = selected ? new Date(selected.getTime() + 86400000) : null;
    for (let i = 0; i < lead; i++) { const s = document.createElement("span"); s.className = "cal-day is-empty"; grid.appendChild(s); }
    for (let d = 1; d <= days; d++) {
      const date = new Date(view.getFullYear(), view.getMonth(), d);
      const b = document.createElement("button");
      b.type = "button"; b.className = "cal-day"; b.textContent = d;
      if (date < today) b.disabled = true;
      if (sameDay(date, selected)) b.classList.add("is-start");
      if (sameDay(date, night)) b.classList.add("is-night");
      b.addEventListener("click", () => { selected = date; renderCalendar(); updateRange(); clearError("tarix"); });
      grid.appendChild(b);
    }
  }
  function rangeText() {
    if (!selected) return "";
    const end = new Date(selected.getTime() + 86400000);
    const m = months();
    const same = selected.getMonth() === end.getMonth();
    const start = same ? selected.getDate() : `${selected.getDate()} ${m[selected.getMonth()]}`;
    return `${start}–${end.getDate()} ${m[end.getMonth()]} ${end.getFullYear()}`;
  }
  function updateRange() {
    rangeEl.textContent = selected ? `${rangeText()} · ${t("dur")}` : t("date.none");
    $("#sumDate").textContent = selected ? rangeText() : "—";
  }
  $("#calPrev")?.addEventListener("click", () => { const p = new Date(view.getFullYear(), view.getMonth() - 1, 1); if (p < new Date(today.getFullYear(), today.getMonth(), 1)) return; view = p; renderCalendar(); });
  $("#calNext")?.addEventListener("click", () => { view = new Date(view.getFullYear(), view.getMonth() + 1, 1); renderCalendar(); });

  /* ---------- Deposit (amount + online payment flag from GET /api/config) ---------- */
  // Default is "not connected", which is the current state of the server.
  const cfg = { deposit: 10, onlinePayment: false };
  const money = (n) => (Number.isInteger(n) ? String(n) : n.toFixed(2)) + " ₼";
  const cardMode = () => !cfg.onlinePayment; // card fields on our page (bank not connected yet)
  function updatePayLabel() {
    const el = $("#payBtn .btn__label");
    if (el) el.textContent = t("pay.deposit").replace("{amount}", money(cfg.deposit));
  }
  function applyConfig() {
    $$("[data-deposit]").forEach((el) => { el.textContent = money(cfg.deposit); });
    $("#depOnline").hidden = !cfg.onlinePayment;
    $("#depOffline").hidden = cfg.onlinePayment;
    $("#cardFields").hidden = !cardMode();
    updatePayLabel();
    renderResult(); // the retry button depends on onlinePayment
  }
  function readConfig(c) {
    if (!c) return;
    if (+c.deposit > 0) cfg.deposit = +c.deposit;
    if (typeof c.onlinePayment === "boolean") cfg.onlinePayment = c.onlinePayment;
  }

  /* ---------- Validation ---------- */
  function setError(name, msg) { const el = $(`.error[data-for="${name}"]`); if (el) el.textContent = msg || ""; const inp = $(`#${name}`); if (inp) inp.classList.toggle("invalid", !!msg); }
  const clearError = (n) => setError(n, "");

  function validatePanel(step) {
    let ok = true;
    const need = (id) => { const el = $(`#${id}`); if (!el || !el.value.trim()) { setError(id, e("req")); ok = false; } else clearError(id); };
    if (step === 1) {
      need("ad"); need("soyad");
      if ($("#fin").value.trim().length !== 7) { setError("fin", e("fin")); ok = false; } else clearError("fin");
      if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test($("#email").value.trim())) { setError("email", e("email")); ok = false; } else clearError("email");
      const digits = tel.value.replace(/\D/g, "");
      if (digits.length !== country().len) { setError("tel", e("phone").replace("{n}", country().len)); ok = false; } else clearError("tel");
      if (!selected) { setError("tarix", e("date")); ok = false; } else clearError("tarix");
    }
    if (step === 2) {
      if (!idImageData) { setError("idfile", e("file")); ok = false; } else clearError("idfile");
      if (!$("#terms").checked) { setError("terms", e("terms")); ok = false; } else clearError("terms");
    }
    if (step === 3) {
      need("unvan"); need("seher");
      if (cardMode()) {
        need("cardName");
        if ($("#cardNo").value.replace(/\D/g, "").length !== 16) { setError("cardNo", e("card")); ok = false; } else clearError("cardNo");
        if (!expiryValid($("#cardExp").value)) { setError("cardExp", e("exp")); ok = false; } else clearError("cardExp");
        if (!/^\d{3,4}$/.test($("#cardCvv").value)) { setError("cardCvv", e("cvv")); ok = false; } else clearError("cardCvv");
      }
    }
    return ok;
  }
  function expiryValid(value) {
    const m = /^(0[1-9]|1[0-2])\/(\d{2})$/.exec(value);
    if (!m) return false;
    const now = new Date(), year = 2000 + +m[2];
    return year > now.getFullYear() || (year === now.getFullYear() && +m[1] >= now.getMonth() + 1);
  }
  // The card inputs are cleared once the booking is saved.
  function clearCard() { ["cardName", "cardNo", "cardExp", "cardCvv"].forEach((id) => { const el = $("#" + id); if (el) el.value = ""; }); }

  /* ---------- Wizard ---------- */
  const panels = $$(".panel"), stepsUi = $$(".step");
  let current = 1;
  function goTo(step) {
    current = step;
    panels.forEach((p) => p.classList.toggle("is-active", +p.dataset.panel === step));
    stepsUi.forEach((s) => { const n = +s.dataset.step; s.classList.toggle("is-active", n === step); s.classList.toggle("is-done", n < step); });
    $(".panelcard")?.scrollIntoView({ behavior: "smooth", block: "start" });
  }
  // Show the first problem: on a long panel it may be off screen.
  function focusFirstError() {
    const panel = $('.panel[data-panel="' + current + '"]');
    const err = $$(".error", panel).find((el) => el.textContent);
    const field = err && err.closest(".field");
    if (!field) return;
    field.scrollIntoView({ behavior: "smooth", block: "center" });
    const input = $(".invalid", field) || $("input:not([type=file]), select", field);
    if (input) input.focus({ preventScroll: true });
  }
  $$("[data-next]").forEach((b) => b.addEventListener("click", () => { if (validatePanel(current)) goTo(+b.dataset.next); else focusFirstError(); }));
  $$("[data-prev]").forEach((b) => b.addEventListener("click", () => goTo(+b.dataset.prev)));

  /* ---------- Upload ---------- */
  const uploadBox = $("#uploadBox"), fileInput = $("#idfile"), emptyEl = $("#uploadEmpty"), previewEl = $("#uploadPreview");
  // Shrink the uploaded image so the upload stays small and it renders fast in admin.
  function compressImage(src, cb) {
    const img = new Image();
    img.onload = () => {
      const max = 1000; let w = img.width, h = img.height;
      if (w > max || h > max) { const s = Math.min(max / w, max / h); w = Math.round(w * s); h = Math.round(h * s); }
      const c = document.createElement("canvas"); c.width = w; c.height = h;
      c.getContext("2d").drawImage(img, 0, 0, w, h);
      try { cb(c.toDataURL("image/jpeg", 0.7)); } catch (err) { cb(null); }
    };
    img.onerror = () => cb(null); // a format this browser cannot read, e.g. HEIC
    img.src = src;
  }
  function clearUpload() { fileInput.value = ""; emptyEl.hidden = false; previewEl.hidden = true; idImageData = null; idImageName = ""; }
  function showFile(file) {
    if (!file) return;
    if (!file.type.startsWith("image/")) { clearUpload(); setError("idfile", e("ftype")); return; }
    if (file.size > 8 * 1024 * 1024) { clearUpload(); setError("idfile", e("fsize")); return; }
    clearError("idfile");
    const r = new FileReader();
    r.onload = (ev) => {
      compressImage(ev.target.result, (out) => {
        if (!out) { clearUpload(); setError("idfile", e("ftype")); return; }
        idImageData = out; idImageName = file.name;
        $("#previewImg").src = out; $("#fileName").textContent = file.name;
        emptyEl.hidden = true; previewEl.hidden = false;
      });
    };
    r.onerror = () => { clearUpload(); setError("idfile", e("ftype")); };
    r.readAsDataURL(file);
  }
  if (uploadBox) {
    uploadBox.addEventListener("click", (ev) => { if (ev.target.id !== "removeFile") fileInput.click(); });
    uploadBox.addEventListener("keydown", (ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); fileInput.click(); } });
    fileInput.addEventListener("change", () => showFile(fileInput.files[0]));
    ["dragover", "dragenter"].forEach((x) => uploadBox.addEventListener(x, (ev) => { ev.preventDefault(); uploadBox.classList.add("is-drag"); }));
    ["dragleave", "drop"].forEach((x) => uploadBox.addEventListener(x, (ev) => { ev.preventDefault(); uploadBox.classList.remove("is-drag"); }));
    uploadBox.addEventListener("drop", (ev) => { const f = ev.dataTransfer.files[0]; if (f) { fileInput.files = ev.dataTransfer.files; showFile(f); } });
    $("#removeFile").addEventListener("click", (ev) => { ev.stopPropagation(); clearUpload(); });
  }

  $("#fin")?.addEventListener("input", (ev) => { ev.target.value = ev.target.value.toUpperCase().replace(/[^A-Z0-9]/g, "").slice(0, 7); });

  /* ---------- Card formatting (card mode only; values never leave the browser) ---------- */
  $("#cardNo")?.addEventListener("input", (ev) => { ev.target.value = ev.target.value.replace(/\D/g, "").slice(0, 16).replace(/(.{4})/g, "$1 ").trim(); });
  $("#cardExp")?.addEventListener("input", (ev) => { let v = ev.target.value.replace(/\D/g, "").slice(0, 4); if (v.length >= 3) v = v.slice(0, 2) + "/" + v.slice(2); ev.target.value = v; });
  $("#cardCvv")?.addEventListener("input", (ev) => { ev.target.value = ev.target.value.replace(/\D/g, "").slice(0, 4); });

  /* ---------- Result view: booking saved / payment status ---------- */
  const ICONS = {
    ok: '<svg viewBox="0 0 24 24"><path d="M20 6 9 17l-5-5"/></svg>',
    wait: '<span class="success__spin"></span>',
    pending: '<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></svg>',
    fail: '<svg viewBox="0 0 24 24"><path d="M18 6 6 18M6 6l12 12"/></svg>',
    alert: '<svg viewBox="0 0 24 24"><path d="M12 7v6m0 4h.01"/></svg>'
  };
  // Each state: icon, colour tone, texts and which buttons to show.
  const STATES = {
    booked: () => ({ icon: "ok", title: t("success.title"), sub: t("success.sub"), ref: true }),
    payerror: () => ({ icon: "pending", tone: "warn", title: t("success.title"), sub: t("payerr.sub"), ref: true, pay: true }),
    checking: () => ({ icon: "wait", tone: "wait", title: t("check.title"), sub: t("check.sub") }),
    paid: () => ({ icon: "ok", title: t("paid.title"), sub: t("paid.sub"), ref: true }),
    pending: () => ({ icon: "pending", tone: "warn", title: t("pending.title"), sub: t("pending.sub"), ref: true, check: true, pay: true }),
    failed: () => ({ icon: "fail", tone: "fail", title: t("failed.title"), sub: t("failed.sub"), ref: true, pay: true }),
    error: () => ({ icon: "alert", tone: "fail", title: t("err.title"), sub: t("err.sub"), restart: true })
  };
  const form = $("#bookingForm"), payBtn = $("#payBtn"), resPay = $("#resPay");
  let bookingRef = null;
  let resultState = null;
  let leftForBank = false;

  function setBusy(btn, on) {
    if (!btn) return;
    const label = $(".btn__label", btn), spin = $(".btn__spinner", btn);
    if (label) label.hidden = on;
    if (spin) spin.hidden = !on;
    btn.disabled = on;
  }
  function renderResult() {
    if (!resultState) return;
    const s = STATES[resultState]();
    const icon = $("#resIcon");
    icon.className = "success__check" + (s.tone ? " success__check--" + s.tone : "");
    icon.innerHTML = ICONS[s.icon];
    $("#resTitle").textContent = s.title;
    $("#resSub").textContent = s.sub;
    const refEl = $("#refNo");
    refEl.hidden = !(s.ref && bookingRef);
    refEl.textContent = bookingRef ? t("success.refPrefix") + " " + bookingRef : "";
    resPay.hidden = !(s.pay && cfg.onlinePayment);
    $("#resCheck").hidden = !s.check;
    $("#resRestart").hidden = !s.restart;
  }
  function showResult(name) {
    resultState = name;
    form.hidden = true;
    $("#result").hidden = false;
    $("#resError").textContent = "";
    renderResult();
    $(".panelcard")?.scrollIntoView({ behavior: "smooth", block: "start" });
    if (name !== "checking") $("#resTitle").focus({ preventScroll: true });
  }
  // Customer came back from the bank (or from a link): show only the result.
  function enterReturnMode() {
    form.hidden = true;
    const steps = $(".steps"); if (steps) steps.hidden = true;
    const order = $(".ordercard"); if (order) order.hidden = true;
    $(".booking")?.classList.add("is-result");
  }

  /* ---------- Payment: Kapital Bank hosted page ---------- */
  const safeUrl = (u) => typeof u === "string" && /^https?:\/\//i.test(u);
  function goToBank(url) {
    // Back from the bank should show the payment status, not the filled form again.
    if (bookingRef) { try { history.replaceState(null, "", "?ref=" + encodeURIComponent(bookingRef)); } catch (err) { /* ignore */ } }
    leftForBank = true;
    location.assign(url);
  }
  async function checkPayment() {
    showResult("checking");
    let st;
    try { st = await window.AzeStore.paymentStatus(bookingRef); }
    catch (err) {
      if (err.status === 404) { showResult("error"); return; }
      showResult("pending");
      $("#resError").textContent = e(err.status === 429 ? "busy" : "checkfail");
      return;
    }
    readConfig({ deposit: st && st.deposit, onlinePayment: st && st.onlinePayment });
    const status = st && st.paymentStatus;
    if (status === "paid") showResult("paid");
    else if (!cfg.onlinePayment) showResult("booked"); // staff arrange the deposit
    else showResult(status === "failed" ? "failed" : "pending");
  }
  async function retryPay() {
    if (!bookingRef) return;
    setBusy(resPay, true);
    $("#resError").textContent = "";
    try {
      const r = await window.AzeStore.payBooking(bookingRef, lang());
      if (r && safeUrl(r.paymentUrl)) { goToBank(r.paymentUrl); return; } // stays busy while the page changes
      $("#resError").textContent = e("payfail");
    } catch (err) {
      if (err.status === 409 && err.code === "paid") showResult("paid");
      else if (err.status === 404) showResult("error");
      else if (err.status === 503) { cfg.onlinePayment = false; applyConfig(); showResult("booked"); }
      else $("#resError").textContent = e(err.status === 429 ? "busy" : "payfail");
    }
    setBusy(resPay, false);
  }
  resPay.addEventListener("click", retryPay);
  $("#resCheck").addEventListener("click", checkPayment);
  // Back button from the bank page may restore this page from the cache.
  window.addEventListener("pageshow", (ev) => {
    if (!ev.persisted || !leftForBank) return;
    leftForBank = false;
    setBusy(payBtn, false); setBusy(resPay, false);
    enterReturnMode();
    checkPayment();
  });

  /* ---------- Submit ---------- */
  const isoDate = (d) => d.getFullYear() + "-" + String(d.getMonth() + 1).padStart(2, "0") + "-" + String(d.getDate()).padStart(2, "0");
  const PANEL_OF = { ad: 1, soyad: 1, ata: 1, fin: 1, email: 1, tel: 1, tarix: 1, idfile: 2, terms: 2, unvan: 3, seher: 3, postal: 3, cardNo: 3, cardExp: 3 };
  function showSubmitError(err) {
    const field = err && err.field;
    if (PANEL_OF[field]) {
      if (field === "idfile") clearUpload();
      setError(field, e("invalid"));
      goTo(PANEL_OF[field]);
      focusFirstError();
      return;
    }
    $("#payError").textContent = e(err && err.status === 429 ? "busy" : "server");
  }

  form?.addEventListener("input", (ev) => { if (ev.target.id) clearError(ev.target.id); $("#payError").textContent = ""; });
  form?.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    if (current !== 3) { if (validatePanel(current)) goTo(current + 1); else focusFirstError(); return; } // Enter key on an earlier step
    if (!validatePanel(3)) { focusFirstError(); return; }
    setBusy(payBtn, true);
    $("#payError").textContent = "";
    let res;
    try {
      res = await window.AzeStore.addBooking({
        ad: $("#ad").value.trim(), soyad: $("#soyad").value.trim(), ata: $("#ata").value.trim(),
        fin: $("#fin").value.trim(), email: $("#email").value.trim(),
        tel: tel.value.replace(/\D/g, ""), country: country().iso,
        tourDate: isoDate(selected),
        unvan: $("#unvan").value.trim(), seher: $("#seher").value.trim(), postal: $("#postal").value.trim(),
        termsAccepted: $("#terms").checked,
        idImage: idImageData, idName: idImageName,
        lang: lang(),
        // Card mode only: just the last 4 digits and the expiry. The full number and the CVV stay here.
        cardLast4: cardMode() ? $("#cardNo").value.replace(/\D/g, "").slice(-4) : "",
        cardExp: cardMode() ? $("#cardExp").value : ""
      });
    } catch (err) {
      setBusy(payBtn, false);
      showSubmitError(err);
      return;
    }
    bookingRef = res && res.ref;
    clearCard();
    if (res && safeUrl(res.paymentUrl)) { goToBank(res.paymentUrl); return; } // stays busy while the page changes
    setBusy(payBtn, false);
    $$(".step").forEach((s) => { s.classList.add("is-done"); s.classList.remove("is-active"); });
    showResult(res && (res.paymentError || res.paymentUrl) ? "payerror" : "booked");
  });

  /* ---------- init + language changes ---------- */
  function refreshDynamic() {
    renderCountries(); renderDow(); renderCalendar(); updateRange(); updatePayLabel();
    // Messages on screen are in the old language; they return on the next check.
    $$(".error[data-for]").forEach((el) => clearError(el.dataset.for));
    $("#payError").textContent = "";
    $("#resError").textContent = "";
    renderResult();
  }
  refreshDynamic();
  window.addEventListener("azelangchange", refreshDynamic);
  window.AzeStore.getConfig().then(readConfig).catch(() => { /* keep the defaults */ }).then(applyConfig);

  // Returning from Kapital Bank: /rezervasiya/?ref=AZT-…  (or ?odenis=xeta)
  const params = new URLSearchParams(location.search);
  const retRef = (params.get("ref") || "").trim();
  if (params.get("odenis") === "xeta") { enterReturnMode(); showResult("error"); }
  else if (retRef) {
    enterReturnMode();
    if (/^[A-Za-z0-9-]{3,40}$/.test(retRef)) { bookingRef = retRef; checkPayment(); }
    else showResult("error");
  }
})();
