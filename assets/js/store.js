/* Aze-Tourism — API client shared by all pages (window.AzeStore).
   All data lives on the server; see the Go backend in the project root. */
(function () {
  "use strict";

  async function request(method, url, body) {
    const opts = { method, headers: {} };
    if (body !== undefined) { opts.headers["Content-Type"] = "application/json"; opts.body = JSON.stringify(body); }
    let res;
    try { res = await fetch(url, opts); }
    catch (e) { throw new Error("Serverə qoşulmaq olmadı. İnternet bağlantısını yoxlayın."); }
    const data = res.status === 204 ? null : await res.json().catch(() => null);
    if (!res.ok) {
      const err = new Error((data && data.error) || "Server xətası");
      err.status = res.status;
      err.field = data && data.field;
      err.code = data && data.code; // e.g. "paid" on 409 from /pay
      throw err;
    }
    return data;
  }

  const seg = encodeURIComponent;

  window.AzeStore = {
    getFaq: () => request("GET", "/api/faq"),
    getTerms: () => request("GET", "/api/terms"),
    // Home page trust strip: always 4 items [{value, label: {az, ru, en}}]; empty value = hidden
    getStats: () => request("GET", "/api/stats"),
    // {deposit, currency, onlinePayment}
    getConfig: () => request("GET", "/api/config"),
    // -> {ref, paymentStatus, paymentUrl?, paymentError?}
    addBooking: (b) => request("POST", "/api/bookings", b),
    // New Kapital Bank order for an unpaid booking -> {paymentUrl}
    payBooking: (ref, lang) => request("POST", "/api/bookings/" + seg(ref) + "/pay", { lang }),
    // -> {ref, paymentStatus, deposit, onlinePayment} (nothing personal)
    paymentStatus: (ref) => request("GET", "/api/bookings/" + seg(ref) + "/payment"),
    admin: {
      me: () => request("GET", "/api/admin/me"),
      login: (username, password) => request("POST", "/api/admin/login", { username, password }),
      logout: () => request("POST", "/api/admin/logout"),
      bookings: () => request("GET", "/api/admin/bookings"),
      booking: (ref) => request("GET", "/api/admin/bookings/" + seg(ref)),
      setStatus: (ref, status) => request("PATCH", "/api/admin/bookings/" + seg(ref), { status }),
      // "paid" = deposit received outside the website, "pending" = undo that manual mark
      setPaymentStatus: (ref, paymentStatus) => request("PATCH", "/api/admin/bookings/" + seg(ref), { paymentStatus }),
      // {page, size, kind, q} -> {items, total, page, size, stats}
      visits: (params) => request("GET", "/api/admin/visits?" + new URLSearchParams(params).toString()),
      addFaq: (item) => request("POST", "/api/admin/faq", item),
      updateFaq: (id, item) => request("PUT", "/api/admin/faq/" + seg(id), item),
      deleteFaq: (id) => request("DELETE", "/api/admin/faq/" + seg(id)),
      saveTerms: (terms) => request("PUT", "/api/admin/terms", terms),
      saveStats: (stats) => request("PUT", "/api/admin/stats", stats)
    }
  };
})();
