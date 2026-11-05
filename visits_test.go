package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	androidUA = "Mozilla/5.0 (Linux; Android 10; K) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36"
	iphoneUA  = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1"
)

// beacon posts a raw page-view body the way assets/js/visit.js does.
func beacon(t *testing.T, srv *httptest.Server, ua, contentType, body string, headers map[string]string) int {
	t.Helper()
	req, err := http.NewRequest("POST", srv.URL+"/api/visit", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", ua)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

type visitsPage struct {
	Items []VisitLog `json:"items"`
	Total int        `json:"total"`
	Page  int        `json:"page"`
	Size  int        `json:"size"`
	Stats VisitStats `json:"stats"`
	Raw   []byte     `json:"-"`
}

func getVisits(t *testing.T, c *http.Client, srv *httptest.Server, query string) visitsPage {
	t.Helper()
	status, body := call(t, c, "GET", srv.URL+"/api/admin/visits?"+query, nil)
	if status != http.StatusOK {
		t.Fatalf("visits?%s: %d %s", query, status, body)
	}
	var page visitsPage
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	page.Raw = body
	return page
}

// waitForTotal polls until the log holds `want` rows: rows are written by background workers.
func waitForTotal(t *testing.T, c *http.Client, srv *httptest.Server, want int) visitsPage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		page := getVisits(t, c, srv, "size=200")
		if page.Total == want {
			return page
		}
		if time.Now().After(deadline) {
			t.Fatalf("log has %d rows, want %d: %s", page.Total, want, page.Raw)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestLogText(t *testing.T) {
	cases := map[string]string{
		"  plain  ":                 "plain",
		"line\nbreak\ttab\x00nul":   "linebreaktabnul",
		"bad \xff utf8":             "bad  utf8",
		" sep ":                     "sep",
		strings.Repeat("ə", 400):    strings.Repeat("ə", 300),
		"<script>alert(1)</script>": "<script>alert(1)</script>", // stored as text; the panel escapes it
	}
	for in, want := range cases {
		if got := logText(in, maxLogTextLen); got != want {
			t.Errorf("logText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHeaderHints(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/admin/login", nil)
	r.Header.Set("Sec-CH-UA", `"Chromium";v="130", "Google Chrome";v="130", "Not?A_Brand";v="99"`)
	r.Header.Set("Sec-CH-UA-Platform", `"Windows"`)
	r.Header.Set("Sec-CH-UA-Mobile", "?0")
	h := headerHints(r)
	if h.Platform != "Windows" || h.Mobile || len(h.Brands) != 3 || h.Brands[1] != (hintBrand{"Google Chrome", "130"}) {
		t.Errorf("headerHints = %+v", h)
	}
	r.Header.Set("Accept-Language", "az-AZ,az;q=0.9,en;q=0.8")
	if got := acceptLanguage(r); got != "az-AZ" {
		t.Errorf("acceptLanguage = %q", got)
	}
}

func TestVisitAndLoginLog(t *testing.T) {
	srv, _ := newTestServer(t)

	// One failed and three successful admin logins.
	status, _ := call(t, http.DefaultClient, "POST", srv.URL+"/api/admin/login",
		map[string]string{"username": "Admin", "password": "wrong-password-xyz"})
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", status)
	}
	admin, finance, operator := loginAs(t, srv, "admin"), loginAs(t, srv, "maliyye"), loginAs(t, srv, "isci")

	body := `{"path":"/rezervasiya/","referrer":"https://www.google.com/","lang":"az-AZ","tz":"Asia/Baku",
	  "screen":"390x844","dpr":3,"touch":5,"cores":8,"memory":8,"conn":"4g",
	  "hints":{"platform":"Android","platformVersion":"14.0.0","model":"SM-S918B","mobile":true,
	           "brands":[{"brand":"Google Chrome","version":"130.0.6723.58"}],"architecture":"arm","bitness":"64"}}`
	if status := beacon(t, srv, androidUA, "application/json", body, nil); status != http.StatusNoContent {
		t.Fatalf("beacon: %d", status)
	}
	for _, path := range []string{"/", "/sertler/", "/", "/admin/"} {
		if status := beacon(t, srv, iphoneUA, "application/json", `{"path":"`+path+`","dpr":"2"}`, nil); status != http.StatusNoContent {
			t.Errorf("beacon %s: %d", path, status)
		}
	}
	if status := beacon(t, srv, iphoneUA, "application/json", `{"path":"javascript:alert(1)"}`, nil); status != http.StatusNoContent {
		t.Errorf("beacon with bad path: %d", status)
	}
	// Dropped silently: another site posting into our log.
	if status := beacon(t, srv, iphoneUA, "application/json", `{"path":"/x"}`, map[string]string{"Sec-Fetch-Site": "cross-site"}); status != http.StatusNoContent {
		t.Errorf("cross-site beacon: %d", status)
	}
	// Refused: not JSON, broken JSON, too large.
	bad := []struct{ contentType, body string }{
		{"text/plain", `{"path":"/"}`},
		{"application/json", `{"path":`},
		{"application/json", `{"path":"/","referrer":"` + strings.Repeat("x", maxVisitBody) + `"}`},
	}
	for _, b := range bad {
		if status := beacon(t, srv, iphoneUA, b.contentType, b.body, nil); status != http.StatusBadRequest {
			t.Errorf("bad beacon (%s, %d bytes): %d", b.contentType, len(b.body), status)
		}
	}

	page := waitForTotal(t, admin, srv, 10) // 6 visits + 4 logins
	time.Sleep(200 * time.Millisecond)
	if again := getVisits(t, admin, srv, "size=200"); again.Total != 10 {
		t.Errorf("a dropped beacon was saved: %d rows", again.Total)
	}
	if bytes.Contains(page.Raw, []byte("wrong-password-xyz")) {
		t.Error("the log contains a password")
	}

	var visit *VisitLog
	for i := range page.Items {
		if page.Items[i].Path == "/rezervasiya/" {
			visit = &page.Items[i]
		}
	}
	if visit == nil {
		t.Fatalf("beacon row missing: %s", page.Raw)
	}
	want := VisitLog{
		ID: visit.ID, TS: visit.TS, Kind: "visit", IP: "127.0.0.1",
		Provider: "Stub Telecom", ASN: "AS64500", Country: "AZ", Hostname: "host-127.0.0.1.stub.example",
		Path: "/rezervasiya/", Referrer: "https://www.google.com/",
		DeviceType: "mobile", Device: "Samsung SM-S918B", OS: "Android 14", Browser: "Chrome 130",
		Screen: "390x844", Language: "az-AZ", Timezone: "Asia/Baku",
		Hardware: "8 nüvə · 8 GB RAM · DPR 3 · toxunma: 5 · arm 64-bit", Connection: "4g", UserAgent: androidUA,
	}
	if *visit != want {
		t.Errorf("visit row:\n got %+v\nwant %+v", *visit, want)
	}
	if time.Since(visit.TS) > time.Minute {
		t.Errorf("visit time: %v", visit.TS)
	}
	for i := 1; i < len(page.Items); i++ {
		if page.Items[i].TS.After(page.Items[i-1].TS) {
			t.Error("rows are not newest first")
		}
	}

	fails := getVisits(t, admin, srv, "kind=login_fail")
	if fails.Total != 1 || fails.Items[0].Username != "admin" || fails.Items[0].Kind != "login_fail" || fails.Items[0].Provider != "Stub Telecom" {
		t.Errorf("failed logins: %s", fails.Raw)
	}
	if ok := getVisits(t, admin, srv, "kind=login_ok"); ok.Total != 3 {
		t.Errorf("successful logins: %d", ok.Total)
	}
	if visits := getVisits(t, admin, srv, "kind=visit&size=2&page=2"); visits.Total != 6 || len(visits.Items) != 2 || visits.Page != 2 || visits.Size != 2 {
		t.Errorf("paging: total %d, items %d, page %d, size %d", visits.Total, len(visits.Items), visits.Page, visits.Size)
	}
	if last := getVisits(t, admin, srv, "kind=visit&size=4&page=2"); len(last.Items) != 2 {
		t.Errorf("last page: %d items", len(last.Items))
	}
	if all := getVisits(t, admin, srv, "kind=nonsense&size=1"); all.Total != 10 || len(all.Items) != 1 {
		t.Errorf("unknown kind should list everything: %d", all.Total)
	}

	searches := map[string]int{
		"samsung":   1, // device, any case
		"IOS":       5, // os
		"maliyye":   1, // username
		"/sertler/": 1, // path
		"127.0.0":   10,
		"stub tel":  10, // provider
		"%":         0,  // matched literally
		"nothing":   0,
	}
	for q, want := range searches {
		if got := getVisits(t, admin, srv, "q="+url.QueryEscape(q)); got.Total != want {
			t.Errorf("search %q: %d rows, want %d", q, got.Total, want)
		}
	}

	stats := page.Stats
	if stats.Today != 6 || stats.TodayUniqueIPs != 1 || stats.Week != 6 || stats.FailedLogins24h != 1 {
		t.Errorf("stats: %+v", stats)
	}

	// Admin only.
	for name, c := range map[string]*http.Client{"finance": finance, "operator": operator} {
		if status, _ := call(t, c, "GET", srv.URL+"/api/admin/visits", nil); status != http.StatusForbidden {
			t.Errorf("%s: status %d", name, status)
		}
	}
	if status, _ := call(t, http.DefaultClient, "GET", srv.URL+"/api/admin/visits", nil); status != http.StatusUnauthorized {
		t.Errorf("anonymous: status %d", status)
	}
}

func TestVisitRateLimit(t *testing.T) {
	srv, _, app := newTestApp(t, Config{})
	app.visitLimit = newRateLimiter(2, time.Hour)
	for range 3 {
		if status := beacon(t, srv, iphoneUA, "application/json", `{"path":"/"}`, nil); status != http.StatusNoContent {
			t.Fatalf("beacon: %d", status)
		}
	}
	admin := loginAs(t, srv, "admin")
	page := waitForTotal(t, admin, srv, 3) // 2 visits + 1 login
	time.Sleep(200 * time.Millisecond)
	if got := getVisits(t, admin, srv, "kind=visit"); got.Total != 2 {
		t.Errorf("rate limit: %d visits saved, want 2 (%s)", got.Total, page.Raw)
	}
}

func TestVisitRetention(t *testing.T) {
	store := newTestStore(t, true)
	app := newApp(Config{}, store)
	ctx := context.Background()
	for _, age := range []time.Duration{181 * 24 * time.Hour, 179 * 24 * time.Hour, time.Minute} {
		if err := store.AddVisit(ctx, VisitLog{TS: time.Now().Add(-age), Kind: kindVisit, DeviceType: deviceUnknown}); err != nil {
			t.Fatal(err)
		}
	}
	app.pruneVisits(ctx)
	if _, total, err := store.Visits(ctx, "", "", 10, 0); err != nil || total != 2 {
		t.Errorf("after cleanup: %d rows (%v), want 2", total, err)
	}
}

func TestVisitLoggerDrainsOnStop(t *testing.T) {
	store := newTestStore(t, true)
	v := newVisitLogger(store, stubLookup)
	v.start()
	for range 20 {
		v.add(VisitLog{TS: time.Now(), Kind: kindVisit, IP: "198.51.100.7", DeviceType: deviceUnknown})
	}
	v.stop(context.Background())
	if v.add(VisitLog{TS: time.Now(), Kind: kindVisit}) {
		t.Error("a stopped logger accepted a row")
	}
	items, total, err := store.Visits(context.Background(), "", "", 50, 0)
	if err != nil || total != 20 {
		t.Fatalf("saved %d rows (%v), want 20", total, err)
	}
	if items[0].Provider != "Stub Telecom" || items[0].IP != "198.51.100.7" {
		t.Errorf("row: %+v", items[0])
	}
}
