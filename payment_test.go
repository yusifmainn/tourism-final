package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	stubBankUser = "TerminalSys/kapital"
	stubBankPass = "stub-pass"
)

// stubOrder is one order held by stubBank.
type stubOrder struct {
	TypeRid, Amount, Currency, Language, Description, RedirectURL string
	Status                                                        string
}

// stubBank imitates the Kapital Bank API (create order, order details) as researched.
type stubBank struct {
	srv        *httptest.Server
	mu         sync.Mutex
	nextID     int64
	orders     map[int64]*stubOrder
	failCreate bool
}

func newStubBank(t *testing.T) *stubBank {
	t.Helper()
	b := &stubBank{nextID: 4595, orders: map[int64]*stubOrder{}}
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			user, pass, ok := r.BasicAuth()
			if !ok || user != stubBankUser || pass != stubBankPass {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"errorCode":"InvalidRequest","errorDescription":"insufficient authentication"}`))
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("POST /api/order", auth(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Order struct {
				TypeRid        string `json:"typeRid"`
				Amount         string `json:"amount"`
				Currency       string `json:"currency"`
				Language       string `json:"language"`
				Description    string `json:"description"`
				HppRedirectURL string `json:"hppRedirectUrl"`
			} `json:"order"`
		}
		if r.Header.Get("Content-Type") != "application/json" || json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.failCreate {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"errorCode":"PmoUnreachable","errorDescription":"stub outage"}`))
			return
		}
		b.nextID++
		id := b.nextID
		o := in.Order
		b.orders[id] = &stubOrder{TypeRid: o.TypeRid, Amount: o.Amount, Currency: o.Currency, Language: o.Language,
			Description: o.Description, RedirectURL: o.HppRedirectURL, Status: "Preparing"}
		json.NewEncoder(w).Encode(map[string]any{"order": map[string]any{
			"id": id, "hppUrl": b.srv.URL + "/flex", "password": "pw" + strconv.FormatInt(id, 10),
			"status": "Preparing", "cvv2AuthStatus": "Required", "secret": "312866",
		}})
	}))
	mux.HandleFunc("GET /api/order/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		b.mu.Lock()
		defer b.mu.Unlock()
		o, ok := b.orders[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"errorCode":"InvalidRequest","errorDescription":"order not found"}`))
			return
		}
		amount, _ := strconv.ParseFloat(o.Amount, 64)
		order := map[string]any{"id": id, "typeRid": o.TypeRid, "status": o.Status, "amount": amount,
			"currency": o.Currency, "createTime": "2026-11-05 15:38:23"}
		if o.Status != "Preparing" {
			order["finishTime"] = "2026-11-05 15:39:17"
		}
		json.NewEncoder(w).Encode(map[string]any{"order": order})
	}))
	b.srv = httptest.NewServer(mux)
	t.Cleanup(b.srv.Close)
	return b
}

func (b *stubBank) setStatus(id int64, status string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.orders[id].Status = status
}

func (b *stubBank) order(id int64) stubOrder {
	b.mu.Lock()
	defer b.mu.Unlock()
	return *b.orders[id]
}

func (b *stubBank) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.orders)
}

func (b *stubBank) config() Config {
	return Config{
		PublicURL:       "https://aze.example",
		KapitalURL:      b.srv.URL + "/api",
		KapitalUsername: stubBankUser,
		KapitalPassword: stubBankPass,
	}
}

// noRedirect is a client that shows redirects instead of following them.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func redirectOf(t *testing.T, target string) string {
	t.Helper()
	res, err := noRedirect.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("GET %s: status %d, want 303", target, res.StatusCode)
	}
	return res.Header.Get("Location")
}

// orderIDFrom reads the bank order id from a payment page URL.
func orderIDFrom(t *testing.T, paymentURL string) int64 {
	t.Helper()
	u, err := url.Parse(paymentURL)
	if err != nil {
		t.Fatal(err)
	}
	id, err := strconv.ParseInt(u.Query().Get("id"), 10, 64)
	if err != nil {
		t.Fatalf("no order id in %q", paymentURL)
	}
	return id
}

func paymentStatusOf(t *testing.T, srv *httptest.Server, ref string) map[string]any {
	t.Helper()
	status, body := call(t, http.DefaultClient, "GET", srv.URL+"/api/bookings/"+ref+"/payment", nil)
	if status != http.StatusOK {
		t.Fatalf("payment status: %d %s", status, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

/* ---------- Tests that need no database ---------- */

func TestKapitalHelpers(t *testing.T) {
	if got := formatAmount(10); got != "10.00" {
		t.Errorf("formatAmount(10) = %q", got)
	}
	pages := map[string]string{
		"https://txpgtst.kapitalbank.az/flex":  "https://txpgtst.kapitalbank.az/flex?id=4595&password=8xjpd1ejxdma",
		"https://txpgtst.kapitalbank.az/flex/": "https://txpgtst.kapitalbank.az/flex?id=4595&password=8xjpd1ejxdma",
		"https://e-commerce.kapitalbank.az":    "https://e-commerce.kapitalbank.az/flex?id=4595&password=8xjpd1ejxdma",
	}
	for hpp, want := range pages {
		if got, err := hostedPageURL(hpp, 4595, "8xjpd1ejxdma"); err != nil || got != want {
			t.Errorf("hostedPageURL(%q) = %q, %v; want %q", hpp, got, err, want)
		}
	}
	if _, err := hostedPageURL("javascript:alert(1)", 1, "x"); err == nil {
		t.Error("hostedPageURL accepted a non-http URL")
	}

	returns := map[string]int64{"ID=4595&STATUS=FullyPaid": 4595, "id=12": 12, "STATUS=FullyPaid": 0, "ID=abc": 0, "ID=-5": 0}
	for query, want := range returns {
		q, _ := url.ParseQuery(query)
		got, ok := returnOrderID(q)
		if ok != (want != 0) || (ok && got != want) {
			t.Errorf("returnOrderID(%q) = %d, %v; want %d", query, got, ok, want)
		}
	}

	ok := bankOrderState{ID: 1, Amount: 10, Currency: "AZN", TypeRid: "Order_SMS"}
	outcomes := []struct {
		change func(*bankOrderState)
		want   orderOutcome
	}{
		{func(s *bankOrderState) { s.Status = "FullyPaid" }, outcomePaid},
		{func(s *bankOrderState) { s.Status = "FullyPaid"; s.TypeRid = "" }, outcomePaid},
		{func(s *bankOrderState) { s.Status = "Closed" }, outcomePaid},
		{func(s *bankOrderState) { s.Status = "FullyPaid"; s.Amount = 1 }, outcomeOpen},
		{func(s *bankOrderState) { s.Status = "FullyPaid"; s.Currency = "USD" }, outcomeOpen},
		{func(s *bankOrderState) { s.Status = "FullyPaid"; s.TypeRid = "Order_DMS" }, outcomeOpen},
		{func(s *bankOrderState) { s.Status = "Preparing" }, outcomeOpen},
		{func(s *bankOrderState) { s.Status = "PartPaid" }, outcomeOpen},
		{func(s *bankOrderState) { s.Status = "SomethingNew" }, outcomeOpen},
		{func(s *bankOrderState) { s.Status = "Declined" }, outcomeFailed},
		{func(s *bankOrderState) { s.Status = "Expired" }, outcomeFailed},
		{func(s *bankOrderState) { s.Status = "Cancelled" }, outcomeFailed},
		{func(s *bankOrderState) { s.Status = "Refunded" }, outcomeFailed},
	}
	for _, c := range outcomes {
		st := ok
		c.change(&st)
		if got := classifyOrder(st, depositAmount); got != c.want {
			t.Errorf("classifyOrder(%+v) = %d, want %d", st, got, c.want)
		}
	}
}

func TestKapitalClient(t *testing.T) {
	bank := newStubBank(t)
	ctx := context.Background()

	client := newKapitalClient(bank.srv.URL+"/api/", stubBankUser, stubBankPass)
	order, err := client.createOrder(ctx, depositAmount, "ru", "Депозит AZT-1", "https://aze.example/odenis/netice")
	if err != nil {
		t.Fatal(err)
	}
	wantURL := bank.srv.URL + "/flex?id=" + strconv.FormatInt(order.ID, 10) + "&password=pw" + strconv.FormatInt(order.ID, 10)
	if order.PaymentURL != wantURL || order.Status != "Preparing" {
		t.Errorf("order = %+v, want URL %s", order, wantURL)
	}
	sent := bank.order(order.ID)
	if sent.TypeRid != "Order_SMS" || sent.Amount != "10.00" || sent.Currency != "AZN" || sent.Language != "ru" ||
		sent.RedirectURL != "https://aze.example/odenis/netice" {
		t.Errorf("bank received %+v", sent)
	}

	bank.setStatus(order.ID, "FullyPaid")
	st, err := client.orderState(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "FullyPaid" || st.Amount != 10 || st.Currency != "AZN" || st.TypeRid != "Order_SMS" ||
		!st.FinishTime.Equal(time.Date(2026, 11, 5, 15, 39, 17, 0, baku)) {
		t.Errorf("state = %+v", st)
	}

	wrong := newKapitalClient(bank.srv.URL+"/api", stubBankUser, "wrong")
	_, err = wrong.createOrder(ctx, depositAmount, "az", "x", "https://aze.example/odenis/netice")
	var bankErr *bankError
	if !errors.As(err, &bankErr) || bankErr.HTTPStatus != http.StatusBadRequest || bankErr.Code != "InvalidRequest" {
		t.Errorf("wrong password: %v", err)
	}
	if strings.Contains(err.Error(), "wrong") || strings.Contains(err.Error(), stubBankPass) {
		t.Errorf("error message leaks the password: %v", err)
	}
}

/* ---------- Tests that need PostgreSQL ---------- */

func TestPaymentFlow(t *testing.T) {
	bank := newStubBank(t)
	srv, store, _ := newTestApp(t, bank.config())

	status, body := call(t, http.DefaultClient, "GET", srv.URL+"/api/config", nil)
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"currency":"AZN","deposit":10,"onlinePayment":true}` {
		t.Errorf("config: %d %s", status, body)
	}

	status, body = call(t, http.DefaultClient, "POST", srv.URL+"/api/bookings", validBooking())
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	var created bookingCreated
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	if created.PaymentStatus != "pending" || created.PaymentError || !strings.HasPrefix(created.PaymentURL, bank.srv.URL+"/flex?id=") {
		t.Fatalf("create answer: %s", body)
	}
	orderID := orderIDFrom(t, created.PaymentURL)
	sent := bank.order(orderID)
	if sent.Amount != "10.00" || sent.Language != "en" || !strings.Contains(sent.Description, created.Ref) ||
		sent.RedirectURL != "https://aze.example/odenis/netice" {
		t.Errorf("bank received %+v", sent)
	}
	if got := paymentStatusOf(t, srv, created.Ref); got["paymentStatus"] != "pending" || got["deposit"] != float64(10) || got["onlinePayment"] != true {
		t.Errorf("payment status: %v", got)
	}

	// The STATUS parameter is never trusted: the bank still says Preparing.
	returnURL := srv.URL + "/odenis/netice?ID=" + strconv.FormatInt(orderID, 10) + "&STATUS=FullyPaid"
	if loc := redirectOf(t, returnURL); loc != "/rezervasiya/?ref="+created.Ref {
		t.Errorf("return redirect: %q", loc)
	}
	if got := paymentStatusOf(t, srv, created.Ref); got["paymentStatus"] != "pending" {
		t.Fatalf("paid on the word of STATUS: %v", got)
	}

	bank.setStatus(orderID, "FullyPaid")
	if loc := redirectOf(t, returnURL); loc != "/rezervasiya/?ref="+created.Ref {
		t.Errorf("return redirect: %q", loc)
	}
	if got := paymentStatusOf(t, srv, created.Ref); got["paymentStatus"] != "paid" {
		t.Fatalf("after the bank confirmed: %v", got)
	}
	b, _, err := store.Booking(context.Background(), created.Ref)
	if err != nil {
		t.Fatal(err)
	}
	p := b.Payment
	if p.Status != "paid" || p.Method != methodBank || p.BankOrderID != strconv.FormatInt(orderID, 10) || p.PaidAt == nil || p.Amount != 10 {
		t.Errorf("saved payment: %+v", p)
	}

	status, body = call(t, http.DefaultClient, "POST", srv.URL+"/api/bookings/"+created.Ref+"/pay", map[string]string{"lang": "az"})
	if status != http.StatusConflict || !strings.Contains(string(body), `"code":"paid"`) {
		t.Errorf("pay again: %d %s", status, body)
	}
	finance := loginAs(t, srv, "maliyye")
	status, body = call(t, finance, "PATCH", srv.URL+"/api/admin/bookings/"+created.Ref, map[string]string{"paymentStatus": "pending"})
	if status != http.StatusConflict {
		t.Errorf("undo a bank payment: %d %s", status, body)
	}

	for _, q := range []string{"ID=999999&STATUS=FullyPaid", "STATUS=FullyPaid", "ID=abc"} {
		if loc := redirectOf(t, srv.URL+"/odenis/netice?"+q); loc != "/rezervasiya/?odenis=xeta" {
			t.Errorf("%s: redirect %q", q, loc)
		}
	}
}

func TestPaymentFailureRetryAndReconciler(t *testing.T) {
	bank := newStubBank(t)
	srv, store, app := newTestApp(t, bank.config())
	ctx := context.Background()

	_, body := call(t, http.DefaultClient, "POST", srv.URL+"/api/bookings", validBooking())
	var created bookingCreated
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	first := orderIDFrom(t, created.PaymentURL)
	bank.setStatus(first, "Declined")
	redirectOf(t, srv.URL+"/odenis/netice?ID="+strconv.FormatInt(first, 10)+"&STATUS=Declined")
	if got := paymentStatusOf(t, srv, created.Ref); got["paymentStatus"] != "failed" {
		t.Fatalf("after decline: %v", got)
	}

	status, body := call(t, http.DefaultClient, "POST", srv.URL+"/api/bookings/"+created.Ref+"/pay", map[string]string{"lang": "ru"})
	if status != http.StatusOK {
		t.Fatalf("retry: %d %s", status, body)
	}
	var retry struct {
		PaymentURL string `json:"paymentUrl"`
	}
	if err := json.Unmarshal(body, &retry); err != nil {
		t.Fatal(err)
	}
	second := orderIDFrom(t, retry.PaymentURL)
	if second == first || bank.count() != 2 || bank.order(second).Language != "ru" {
		t.Errorf("retry did not create a second order: %d %d (%d orders)", first, second, bank.count())
	}
	if state, _, _ := store.PaymentState(ctx, created.Ref); state != "pending" {
		t.Errorf("a new attempt should make the booking pending again, got %q", state)
	}

	// The customer pays but closes the tab: only the reconciler can find out.
	bank.setStatus(second, "FullyPaid")
	app.reconcilePayments(ctx)
	if state, _, _ := store.PaymentState(ctx, created.Ref); state != "paid" {
		t.Errorf("after reconcile: %q", state)
	}
	open, err := store.OpenPaymentOrders(ctx, time.Now().Add(-time.Hour), 10)
	if err != nil || len(open) != 0 {
		t.Errorf("open orders after reconcile: %v %v", open, err)
	}
	if b, _, _ := store.Booking(ctx, created.Ref); b.Payment.BankOrderID != strconv.FormatInt(second, 10) {
		t.Errorf("bank order shown to staff: %q, want the paid one %d", b.Payment.BankOrderID, second)
	}
}

func TestPaymentStatusPageAsksTheBank(t *testing.T) {
	bank := newStubBank(t)
	srv, _, _ := newTestApp(t, bank.config())
	_, body := call(t, http.DefaultClient, "POST", srv.URL+"/api/bookings", validBooking())
	var created bookingCreated
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	bank.setStatus(orderIDFrom(t, created.PaymentURL), "FullyPaid")
	if got := paymentStatusOf(t, srv, created.Ref); got["paymentStatus"] != "paid" {
		t.Errorf("status page did not re-check a pending order: %v", got)
	}
}

func TestPaymentBankUnavailable(t *testing.T) {
	bank := newStubBank(t)
	bank.failCreate = true
	srv, store, _ := newTestApp(t, bank.config())

	status, body := call(t, http.DefaultClient, "POST", srv.URL+"/api/bookings", validBooking())
	var created bookingCreated
	if status != http.StatusCreated || json.Unmarshal(body, &created) != nil || !created.PaymentError || created.PaymentURL != "" {
		t.Fatalf("create with the bank down: %d %s", status, body)
	}
	if state, found, _ := store.PaymentState(context.Background(), created.Ref); !found || state != "pending" {
		t.Errorf("booking not kept: %v %q", found, state)
	}
	payURL := srv.URL + "/api/bookings/" + created.Ref + "/pay"
	if status, _ := call(t, http.DefaultClient, "POST", payURL, map[string]string{"lang": "az"}); status != http.StatusBadGateway {
		t.Errorf("retry with the bank down: %d", status)
	}
	bank.mu.Lock()
	bank.failCreate = false
	bank.mu.Unlock()
	if status, body := call(t, http.DefaultClient, "POST", payURL, map[string]string{"lang": "az"}); status != http.StatusOK {
		t.Errorf("retry with the bank back: %d %s", status, body)
	}
}

func TestPaymentDisabled(t *testing.T) {
	srv, _ := newTestServer(t)
	status, body := call(t, http.DefaultClient, "GET", srv.URL+"/api/config", nil)
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"currency":"AZN","deposit":10,"onlinePayment":false}` {
		t.Errorf("config: %d %s", status, body)
	}
	_, body = call(t, http.DefaultClient, "POST", srv.URL+"/api/bookings", validBooking())
	var created bookingCreated
	if err := json.Unmarshal(body, &created); err != nil || created.PaymentURL != "" || created.PaymentError {
		t.Fatalf("create: %s", body)
	}
	if got := paymentStatusOf(t, srv, created.Ref); got["onlinePayment"] != false || got["paymentStatus"] != "pending" {
		t.Errorf("payment status: %v", got)
	}
	if status, _ := call(t, http.DefaultClient, "POST", srv.URL+"/api/bookings/"+created.Ref+"/pay", map[string]string{}); status != http.StatusServiceUnavailable {
		t.Errorf("pay while disabled: %d", status)
	}
	if status, _ := call(t, http.DefaultClient, "POST", srv.URL+"/api/bookings/AZT-000000-2026/pay", map[string]string{}); status != http.StatusNotFound {
		t.Errorf("pay unknown booking: %d", status)
	}
	if status, _ := call(t, http.DefaultClient, "GET", srv.URL+"/api/bookings/AZT-000000-2026/payment", nil); status != http.StatusNotFound {
		t.Errorf("status of unknown booking: %d", status)
	}
}

func TestManualPayment(t *testing.T) {
	srv, _ := newTestServer(t)
	_, body := call(t, http.DefaultClient, "POST", srv.URL+"/api/bookings", validBooking())
	var created bookingCreated
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	bookingURL := srv.URL + "/api/admin/bookings/" + created.Ref
	admin, finance, operator := loginAs(t, srv, "admin"), loginAs(t, srv, "maliyye"), loginAs(t, srv, "isci")

	payment := func() map[string]any {
		t.Helper()
		_, body := call(t, admin, "GET", bookingURL, nil)
		var d struct {
			Payment map[string]any `json:"payment"`
		}
		if err := json.Unmarshal(body, &d); err != nil {
			t.Fatal(err)
		}
		return d.Payment
	}

	checks := []struct {
		name   string
		client *http.Client
		url    string
		body   map[string]string
		want   int
	}{
		{"operator may not touch payment", operator, bookingURL, map[string]string{"paymentStatus": "paid"}, http.StatusForbidden},
		{"operator may change status", operator, bookingURL, map[string]string{"status": "İmtina"}, http.StatusNoContent},
		{"empty change", admin, bookingURL, map[string]string{}, http.StatusBadRequest},
		{"unknown payment status", admin, bookingURL, map[string]string{"paymentStatus": "failed"}, http.StatusBadRequest},
		{"unknown booking", admin, srv.URL + "/api/admin/bookings/AZT-000000-2026", map[string]string{"paymentStatus": "paid"}, http.StatusNotFound},
	}
	for _, c := range checks {
		if status, body := call(t, c.client, "PATCH", c.url, c.body); status != c.want {
			t.Errorf("%s: %d %s", c.name, status, body)
		}
	}
	if p := payment(); p["status"] != "pending" {
		t.Errorf("payment changed by a refused request: %v", p)
	}

	if status, _ := call(t, finance, "PATCH", bookingURL, map[string]string{"paymentStatus": "paid"}); status != http.StatusNoContent {
		t.Fatalf("finance marks paid: %d", status)
	}
	if p := payment(); p["status"] != "paid" || p["method"] != methodManual || p["paidAt"] == nil {
		t.Errorf("after manual mark: %v", p)
	}
	_, body = call(t, finance, "GET", srv.URL+"/api/admin/bookings", nil)
	if !strings.Contains(string(body), `"paymentStatus":"paid"`) {
		t.Errorf("list after manual mark: %s", body)
	}
	if status, _ := call(t, admin, "PATCH", bookingURL, map[string]string{"paymentStatus": "pending"}); status != http.StatusNoContent {
		t.Fatalf("admin undoes the mark: %d", status)
	}
	if p := payment(); p["status"] != "pending" || p["method"] != "" || p["paidAt"] != nil {
		t.Errorf("after undo: %v", p)
	}
}
