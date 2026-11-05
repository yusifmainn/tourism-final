package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

var testPasswords = map[string]string{
	"admin":   "admin-test-pass",
	"maliyye": "finance-test-pass",
	"isci":    "operator-test-pass",
}

// newTestStore needs a PostgreSQL server (TEST_DATABASE_URL). Each test works in
// its own temporary schema, which is dropped afterwards, so existing tables are untouched.
// The schema is left empty when migrate is false.
func newTestStore(t *testing.T, migrate bool) *Store {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL is not set; see DEPLOY.md")
	}
	ctx := context.Background()

	conn, err := openStore(ctx, base) // waits until the server is ready
	if err != nil {
		t.Fatal(err)
	}
	schema := "test_" + randomHex(8)
	if _, err := conn.db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := conn.db.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
		conn.Close()
	})

	separator := "?"
	if strings.Contains(base, "?") {
		separator = "&"
	}
	store, err := openStore(ctx, base+separator+"search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if migrate {
		if err := store.migrate(ctx, schemaSQL, seedJSON); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// stubLookup stands in for the DNS provider lookup in every test.
func stubLookup(_ context.Context, ip string) netInfo {
	return netInfo{Provider: "Stub Telecom", ASN: "AS64500", Country: "AZ", Hostname: "host-" + ip + ".stub.example"}
}

// newTestApp starts the API with the given configuration (passwords are always the test ones)
// and with the visit log workers running against stubLookup.
func newTestApp(t *testing.T, cfg Config) (*httptest.Server, *Store, *App) {
	t.Helper()
	store := newTestStore(t, true)
	cfg.Passwords = testPasswords
	app := newApp(cfg, store)
	app.visits.lookup = stubLookup
	if err := app.ensureUsers(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(app.routes())
	t.Cleanup(srv.Close)
	app.visits.start()
	t.Cleanup(func() { app.visits.stop(context.Background()) }) // runs before the store closes
	return srv, store, app
}

func newTestServer(t *testing.T) (*httptest.Server, *Store) {
	t.Helper()
	srv, store, _ := newTestApp(t, Config{})
	return srv, store
}

func call(t *testing.T, c *http.Client, method, url string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, out
}

func loginAs(t *testing.T, srv *httptest.Server, username string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Jar: jar}
	status, _ := call(t, c, "POST", srv.URL+"/api/admin/login", map[string]string{
		"username": username,
		"password": testPasswords[username],
	})
	if status != http.StatusOK {
		t.Fatalf("login %s: status %d", username, status)
	}
	return c
}

func testImageDataURL() string {
	jpeg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 64)...)
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(jpeg)
}

func validBooking() map[string]any {
	return map[string]any{
		"ad": "Test", "soyad": "İstifadəçi", "fin": "ABC1234", "email": "test@example.com",
		"tel": "501234567", "country": "AZ",
		"tourDate":      time.Now().In(baku).AddDate(0, 0, 7).Format(dateLayout),
		"unvan":         "Nizami 1",
		"seher":         "Bakı",
		"termsAccepted": true,
		"idImage":       testImageDataURL(),
		"idName":        "id.jpg",
		"lang":          "en",
		"cardLast4":     "4242",
		"cardExp":       "12/40",
	}
}

/* ---------- Tests that need no database ---------- */

func TestStaticFiles(t *testing.T) {
	srv := httptest.NewServer(staticHandler(webFiles))
	defer srv.Close()
	for path, want := range map[string]int{
		"/":                      http.StatusOK,
		"/rezervasiya/":          http.StatusOK,
		"/sertler/":              http.StatusOK,
		"/admin/":                http.StatusOK,
		"/assets/css/styles.css": http.StatusOK,
		"/favicon.ico":           http.StatusOK,
		"/assets/img/logo.png":   http.StatusOK,
		"/assets/":               http.StatusNotFound,
		"/main.go":               http.StatusNotFound,
		"/seed.json":             http.StatusNotFound,
		"/schema.sql":            http.StatusNotFound,
		"/.env":                  http.StatusNotFound,
		"/no/such/page":          http.StatusNotFound,
	} {
		if status, _ := call(t, http.DefaultClient, "GET", srv.URL+path, nil); status != want {
			t.Errorf("GET %s: status %d, want %d", path, status, want)
		}
	}
}

func TestValidateCard(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, baku)
	cases := []struct {
		last4, exp, field string
	}{
		{"4242", "11/26", ""}, // valid until the end of the expiry month
		{"4242", "12/40", ""},
		{"424", "12/40", "cardNo"},
		{"42a2", "12/40", "cardNo"},
		{"4242424242424242", "12/40", "cardNo"},
		{"4242", "13/40", "cardExp"},
		{"4242", "1240", "cardExp"},
		{"4242", "10/26", "cardExp"}, // expired
		{"4242", "", "cardExp"},
	}
	for _, tc := range cases {
		req := bookingRequest{CardLast4: tc.last4, CardExp: tc.exp}
		if field, _ := req.validateCard(now); field != tc.field {
			t.Errorf("last4=%q exp=%q: field %q, want %q", tc.last4, tc.exp, field, tc.field)
		}
	}
}

func TestValidateBooking(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, baku)
	valid := func() bookingRequest {
		return bookingRequest{
			Ad: "Test", Soyad: "User", FIN: "abc1234", Email: "t@example.com",
			Tel: "501234567", Country: "AZ", TourDate: "2026-11-05", TermsAccepted: true,
			Unvan: "Nizami 1", Seher: "Bakı", Lang: "xx",
		}
	}
	good := valid()
	if field, msg := good.validate(now); field != "" {
		t.Fatalf("valid request rejected: %s: %s", field, msg)
	}
	if good.FIN != "ABC1234" {
		t.Errorf("FIN was not upper-cased: %q", good.FIN)
	}
	if good.Lang != "az" {
		t.Errorf("unknown language kept: %q", good.Lang)
	}

	cases := []struct {
		name   string
		change func(*bookingRequest)
		field  string
	}{
		{"no name", func(b *bookingRequest) { b.Ad = "  " }, "ad"},
		{"no surname", func(b *bookingRequest) { b.Soyad = "" }, "soyad"},
		{"short FIN", func(b *bookingRequest) { b.FIN = "123" }, "fin"},
		{"bad email", func(b *bookingRequest) { b.Email = "nope" }, "email"},
		{"phone too short", func(b *bookingRequest) { b.Tel = "50123" }, "tel"},
		{"phone with letters", func(b *bookingRequest) { b.Tel = "50123456a" }, "tel"},
		{"unknown country", func(b *bookingRequest) { b.Country = "XX" }, "tel"},
		{"bad date", func(b *bookingRequest) { b.TourDate = "05.11.2026" }, "tarix"},
		{"past date", func(b *bookingRequest) { b.TourDate = "2026-11-04" }, "tarix"},
		{"terms refused", func(b *bookingRequest) { b.TermsAccepted = false }, "terms"},
		{"no address", func(b *bookingRequest) { b.Unvan = "" }, "unvan"},
		{"no city", func(b *bookingRequest) { b.Seher = "" }, "seher"},
		{"text too long", func(b *bookingRequest) { b.Unvan = strings.Repeat("x", maxTextLen+1) }, "unvan"},
	}
	for _, tc := range cases {
		req := valid()
		tc.change(&req)
		if field, _ := req.validate(now); field != tc.field {
			t.Errorf("%s: field %q, want %q", tc.name, field, tc.field)
		}
	}
}

func TestDecodeDocument(t *testing.T) {
	doc, err := decodeDocument(testImageDataURL())
	if err != nil {
		t.Fatalf("valid image rejected: %v", err)
	}
	if doc.Mime != "image/jpeg" {
		t.Errorf("mime = %q", doc.Mime)
	}
	bad := map[string]string{
		"empty":     "",
		"no base64": "data:image/jpeg,abc",
		"not image": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("<script>alert(1)</script>")),
		"too large": "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(make([]byte, maxDocBytes+1)),
	}
	for name, dataURL := range bad {
		if _, err := decodeDocument(dataURL); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTourRange(t *testing.T) {
	cases := map[string]string{
		"2026-11-05": "5–6 Noyabr 2026",
		"2026-11-30": "30 Noyabr–1 Dekabr 2026",
		"2026-12-31": "31 Dekabr–1 Yanvar 2027",
		"garbage":    "garbage",
	}
	for input, want := range cases {
		if got := tourRange(input); got != want {
			t.Errorf("tourRange(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPasswordHash(t *testing.T) {
	hash, err := hashPassword("secret-password")
	if err != nil {
		t.Fatal(err)
	}
	if !checkPassword(hash, "secret-password") {
		t.Error("correct password rejected")
	}
	if checkPassword(hash, "wrong") {
		t.Error("wrong password accepted")
	}
}

/* ---------- Tests that need PostgreSQL ---------- */

func TestDefaultAdminLogin(t *testing.T) {
	srv, store := newTestServer(t)

	// A fresh database with no ADMIN_PASSWORD must give admin / admin.
	if _, err := store.db.ExecContext(context.Background(), "DELETE FROM users WHERE username = 'admin'"); err != nil {
		t.Fatal(err)
	}
	app := newApp(Config{Passwords: map[string]string{}}, store)
	if err := app.ensureUsers(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, _ := call(t, http.DefaultClient, "POST", srv.URL+"/api/admin/login", map[string]string{
		"username": "admin",
		"password": "admin",
	})
	if status != http.StatusOK {
		t.Errorf("admin / admin: status %d", status)
	}
}

func TestBookingFlowAndRoles(t *testing.T) {
	srv, store := newTestServer(t)
	public := http.DefaultClient

	bad := validBooking()
	bad["fin"] = "123"
	status, body := call(t, public, "POST", srv.URL+"/api/bookings", bad)
	if status != http.StatusBadRequest || !strings.Contains(string(body), `"field":"fin"`) {
		t.Fatalf("bad FIN: status %d, body %s", status, body)
	}

	good := validBooking()
	good["cardNo"] = "4242424242424242" // a full number must be ignored, never stored
	good["cardCvv"] = "123"             // must be ignored, never stored
	good["mebleg"] = 1                  // the browser cannot set the amount
	status, body = call(t, public, "POST", srv.URL+"/api/bookings", good)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", status, body)
	}
	var created map[string]any
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	// Online payment is off in this test: no payment URL and no payment error.
	if created["paymentStatus"] != "pending" || created["paymentUrl"] != nil || created["paymentError"] != nil {
		t.Errorf("create answer: %s", body)
	}
	ref, _ := created["ref"].(string)
	bookingURL := srv.URL + "/api/admin/bookings/" + ref

	if status, _ := call(t, public, "GET", bookingURL, nil); status != http.StatusUnauthorized {
		t.Errorf("anonymous detail: status %d", status)
	}
	if status, _ := call(t, public, "GET", bookingURL+"/document", nil); status != http.StatusUnauthorized {
		t.Errorf("anonymous document: status %d", status)
	}

	roles := []struct {
		username     string
		wantPayment  bool
		wantDocument bool
		wantCard     bool
	}{
		{"admin", true, true, true},
		{"maliyye", true, false, false},
		{"isci", false, true, false},
	}
	for _, tc := range roles {
		c := loginAs(t, srv, tc.username)
		status, body := call(t, c, "GET", bookingURL, nil)
		if status != http.StatusOK {
			t.Fatalf("%s detail: status %d, body %s", tc.username, status, body)
		}
		var detail map[string]any
		if err := json.Unmarshal(body, &detail); err != nil {
			t.Fatal(err)
		}
		payment, has := detail["payment"].(map[string]any)
		if has != tc.wantPayment {
			t.Errorf("%s: payment visible = %v", tc.username, has)
		}
		if has {
			want := map[string]any{"status": "pending", "method": "", "bankOrderId": "", "paidAt": nil, "amount": float64(depositAmount)}
			for k, v := range want {
				if got, ok := payment[k]; !ok || got != v {
					t.Errorf("%s: payment.%s = %v, want %v", tc.username, k, got, v)
				}
			}
			for _, k := range []string{"cardName", "cardLast4", "cardExp", "txn"} {
				if _, ok := payment[k]; ok {
					t.Errorf("%s: payment still has %s", tc.username, k)
				}
			}
		}
		if _, has := detail["idImage"]; has != tc.wantDocument {
			t.Errorf("%s: document visible = %v", tc.username, has)
		}
		card, hasCard := detail["card"].(map[string]any)
		if hasCard != tc.wantCard {
			t.Errorf("%s: card visible = %v", tc.username, hasCard)
		}
		if hasCard && (card["last4"] != "4242" || card["exp"] != "12/40" || len(card) != 2) {
			t.Errorf("%s: card = %v", tc.username, card)
		}
		if strings.Contains(string(body), "4242424242424242") || strings.Contains(string(body), "cardCvv") {
			t.Errorf("%s: full card number or CVV in the answer: %s", tc.username, body)
		}
		if detail["mebleg"] != float64(depositAmount) {
			t.Errorf("%s: mebleg = %v", tc.username, detail["mebleg"])
		}
		if detail["soyad"] != "İstifadəçi" {
			t.Errorf("%s: soyad = %v", tc.username, detail["soyad"])
		}
		if tourDate, _ := detail["tourDate"].(string); tourDate != good["tourDate"] {
			t.Errorf("%s: tourDate = %v, want %v", tc.username, tourDate, good["tourDate"])
		}
		wantDoc := http.StatusForbidden
		if tc.wantDocument {
			wantDoc = http.StatusOK
		}
		status, image := call(t, c, "GET", bookingURL+"/document", nil)
		if status != wantDoc {
			t.Errorf("%s document: status %d, want %d", tc.username, status, wantDoc)
		}
		if tc.wantDocument && !bytes.HasPrefix(image, []byte{0xFF, 0xD8, 0xFF}) {
			t.Errorf("%s document: wrong bytes returned", tc.username)
		}

		status, body = call(t, c, "GET", srv.URL+"/api/admin/bookings", nil)
		var list []map[string]any
		if status != http.StatusOK || json.Unmarshal(body, &list) != nil || len(list) != 1 || list[0]["ref"] != ref {
			t.Fatalf("%s list: status %d, body %s", tc.username, status, body)
		}
		// The operator does not see payment state, not even in the list.
		if got, has := list[0]["paymentStatus"]; has != tc.wantPayment || (has && got != "pending") {
			t.Errorf("%s list: paymentStatus = %v (present %v)", tc.username, got, has)
		}
	}

	operator := loginAs(t, srv, "isci")
	if status, _ := call(t, operator, "PATCH", bookingURL, map[string]string{"status": "nonsense"}); status != http.StatusBadRequest {
		t.Errorf("bad status: status %d", status)
	}
	if status, _ := call(t, operator, "PATCH", bookingURL, map[string]string{"status": "Təsdiqləndi"}); status != http.StatusNoContent {
		t.Errorf("set status: status %d", status)
	}

	saved, ok, err := store.Booking(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || saved.Status != "Təsdiqləndi" {
		t.Errorf("saved booking: found=%v status=%q", ok, saved.Status)
	}
	if !saved.HasDocument || saved.IDName != "id.jpg" {
		t.Errorf("saved document: has=%v name=%q", saved.HasDocument, saved.IDName)
	}
	if saved.Mebleg != depositAmount || saved.PaymentStatus != payPending {
		t.Errorf("saved deposit: mebleg=%d payment=%q", saved.Mebleg, saved.PaymentStatus)
	}
}

func TestContentEditingRoles(t *testing.T) {
	srv, _ := newTestServer(t)
	item := map[string]any{
		"q": map[string]string{"az": "Sual?"},
		"a": map[string]string{"az": "Cavab."},
	}

	finance := loginAs(t, srv, "maliyye")
	if status, _ := call(t, finance, "POST", srv.URL+"/api/admin/faq", item); status != http.StatusForbidden {
		t.Errorf("finance add FAQ: status %d", status)
	}
	if status, _ := call(t, finance, "PUT", srv.URL+"/api/admin/terms", map[string]string{"az": "x"}); status != http.StatusForbidden {
		t.Errorf("finance edit terms: status %d", status)
	}

	operator := loginAs(t, srv, "isci")
	status, body := call(t, operator, "POST", srv.URL+"/api/admin/faq", item)
	if status != http.StatusCreated {
		t.Fatalf("add FAQ: status %d, body %s", status, body)
	}
	var created FAQ
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	faqURL := srv.URL + "/api/admin/faq/" + created.ID
	if status, _ := call(t, operator, "PUT", faqURL, item); status != http.StatusOK {
		t.Errorf("update FAQ: status %d", status)
	}
	if status, _ := call(t, operator, "DELETE", faqURL, nil); status != http.StatusNoContent {
		t.Errorf("delete FAQ: status %d", status)
	}

	status, body = call(t, http.DefaultClient, "GET", srv.URL+"/api/faq", nil)
	if status != http.StatusOK {
		t.Fatalf("list FAQ: status %d", status)
	}
	var faq []FAQ
	if err := json.Unmarshal(body, &faq); err != nil {
		t.Fatal(err)
	}
	var seeded struct {
		FAQ []FAQ `json:"faq"`
	}
	if err := json.Unmarshal(seedJSON, &seeded); err != nil {
		t.Fatal(err)
	}
	if len(seeded.FAQ) == 0 || len(faq) != len(seeded.FAQ) {
		t.Errorf("FAQ count = %d, want the %d seeded items", len(faq), len(seeded.FAQ))
	}

	if status, _ := call(t, operator, "PUT", srv.URL+"/api/admin/terms", map[string]string{"az": "## Yeni"}); status != http.StatusOK {
		t.Errorf("edit terms: status %d", status)
	}
	_, body = call(t, http.DefaultClient, "GET", srv.URL+"/api/terms", nil)
	if !strings.Contains(string(body), "## Yeni") {
		t.Errorf("terms not updated: %s", body)
	}
}

func TestLoginAndLogout(t *testing.T) {
	srv, _ := newTestServer(t)
	status, _ := call(t, http.DefaultClient, "POST", srv.URL+"/api/admin/login", map[string]string{
		"username": "admin",
		"password": "wrong-password",
	})
	if status != http.StatusUnauthorized {
		t.Errorf("wrong password: status %d", status)
	}

	c := loginAs(t, srv, "admin")
	if status, _ := call(t, c, "GET", srv.URL+"/api/admin/me", nil); status != http.StatusOK {
		t.Errorf("me after login: status %d", status)
	}
	if status, _ := call(t, c, "POST", srv.URL+"/api/admin/logout", nil); status != http.StatusNoContent {
		t.Errorf("logout: status %d", status)
	}
	if status, _ := call(t, c, "GET", srv.URL+"/api/admin/me", nil); status != http.StatusUnauthorized {
		t.Errorf("me after logout: status %d", status)
	}
}

func TestHealthCheck(t *testing.T) {
	srv, _ := newTestServer(t)
	status, body := call(t, http.DefaultClient, "GET", srv.URL+"/healthz", nil)
	if status != http.StatusOK || string(body) != "ok" {
		t.Errorf("healthz: status %d, body %s", status, body)
	}
}

func TestStatsEditing(t *testing.T) {
	srv, store := newTestServer(t)
	readStats := func() []Stat {
		t.Helper()
		status, body := call(t, http.DefaultClient, "GET", srv.URL+"/api/stats", nil)
		if status != http.StatusOK {
			t.Fatalf("GET stats: status %d", status)
		}
		var stats []Stat
		if err := json.Unmarshal(body, &stats); err != nil {
			t.Fatal(err)
		}
		return stats
	}

	// The seeded figures come from seed.json.
	stats := readStats()
	if len(stats) != statCount || stats[0].Value != "12+" || stats[1].Label.RU == "" {
		t.Fatalf("seeded stats: %+v", stats)
	}

	edit := []Stat{
		{Value: " 15+ ", Label: I18n{AZ: "illik təcrübə", RU: "лет опыта", EN: "years of experience"}},
		{Value: "4 000+", Label: I18n{AZ: "iştirakçı"}},
		{Value: "", Label: I18n{}}, // hidden on the site
		{Value: "4.8/5", Label: I18n{AZ: "məmnuniyyət"}},
	}
	if status, _ := call(t, http.DefaultClient, "PUT", srv.URL+"/api/admin/stats", edit); status != http.StatusUnauthorized {
		t.Errorf("anonymous edit: status %d", status)
	}
	finance := loginAs(t, srv, "maliyye")
	if status, _ := call(t, finance, "PUT", srv.URL+"/api/admin/stats", edit); status != http.StatusForbidden {
		t.Errorf("finance edit: status %d", status)
	}

	operator := loginAs(t, srv, "isci")
	bad := map[string][]Stat{
		"three items":    edit[:3],
		"value too long": {{Value: strings.Repeat("9", maxStatValue+1), Label: I18n{AZ: "x"}}, edit[1], edit[2], edit[3]},
		"label too long": {{Value: "1", Label: I18n{AZ: strings.Repeat("ə", maxStatLabel+1)}}, edit[1], edit[2], edit[3]},
		"no AZ label":    {{Value: "1", Label: I18n{EN: "only english"}}, edit[1], edit[2], edit[3]},
	}
	for name, body := range bad {
		if status, _ := call(t, operator, "PUT", srv.URL+"/api/admin/stats", body); status != http.StatusBadRequest {
			t.Errorf("%s: status %d", name, status)
		}
	}
	if status, body := call(t, operator, "PUT", srv.URL+"/api/admin/stats", edit); status != http.StatusOK {
		t.Fatalf("operator edit: status %d, body %s", status, body)
	}
	stats = readStats()
	if stats[0].Value != "15+" || stats[1].Value != "4 000+" || stats[2].Value != "" || stats[3].Label.AZ != "məmnuniyyət" {
		t.Errorf("after edit: %+v", stats)
	}

	admin := loginAs(t, srv, "admin")
	edit[2] = Stat{Value: "70+", Label: I18n{AZ: "müştəri"}}
	if status, _ := call(t, admin, "PUT", srv.URL+"/api/admin/stats", edit); status != http.StatusOK {
		t.Errorf("admin edit: status %d", status)
	}
	if stats = readStats(); stats[2].Value != "70+" {
		t.Errorf("admin edit not saved: %+v", stats)
	}

	// A restart runs migrate again: edited figures must not be reset to the seed values.
	if err := store.migrate(context.Background(), schemaSQL, seedJSON); err != nil {
		t.Fatal(err)
	}
	if stats = readStats(); stats[0].Value != "15+" || stats[2].Value != "70+" {
		t.Errorf("after restart: %+v", stats)
	}
}
