package main

// Site visit and admin login logging ("Giriş loqları" in the admin panel).
// The browser sends one small beacon per page (assets/js/visit.js); admin logins are
// recorded by the login handler. The provider lookup and the database insert happen in
// a small pool of background workers, so a slow DNS answer never delays a page.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	visitWorkers     = 4
	visitQueueSize   = 1000
	visitRetention   = 180 * 24 * time.Hour
	maxVisitBody     = 8 << 10
	maxUserAgentLen  = 512
	maxLogTextLen    = 300
	maxLogUsername   = 100
	visitsPageSize   = 50
	visitsMaxPerPage = 200
)

var visitKinds = []string{kindVisit, kindLoginOK, kindLoginFail}

// visitLogger is the bounded worker pool that completes and saves log rows.
type visitLogger struct {
	store  *Store
	lookup lookupFunc // replaced by tests before start
	jobs   chan VisitLog

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func newVisitLogger(store *Store, lookup lookupFunc) *visitLogger {
	return &visitLogger{store: store, lookup: lookup, jobs: make(chan VisitLog, visitQueueSize)}
}

func (v *visitLogger) start() {
	for range visitWorkers {
		v.wg.Add(1)
		go func() {
			defer v.wg.Done()
			for rec := range v.jobs {
				v.save(rec)
			}
		}()
	}
}

func (v *visitLogger) save(rec VisitLog) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if rec.IP != "" {
		info := v.lookup(ctx, rec.IP)
		rec.Provider = logText(info.Provider, maxLogTextLen)
		rec.ASN = logText(info.ASN, maxLogTextLen)
		rec.Country = logText(info.Country, maxLogTextLen)
		rec.Hostname = logText(info.Hostname, maxLogTextLen)
	}
	if err := v.store.AddVisit(ctx, rec); err != nil {
		log.Printf("visit log: %v", err)
	}
}

// add queues a row without waiting. It is dropped when the queue is full or closed.
func (v *visitLogger) add(rec VisitLog) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return false
	}
	select {
	case v.jobs <- rec:
		return true
	default:
		return false
	}
}

// stop accepts no more rows and waits until the queued ones are saved, or ctx ends.
func (v *visitLogger) stop(ctx context.Context) {
	v.mu.Lock()
	if !v.closed {
		v.closed = true
		close(v.jobs)
	}
	v.mu.Unlock()
	done := make(chan struct{})
	go func() {
		v.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		log.Print("visit log: shutdown before all queued rows were saved")
	}
}

/* ---------- Cleaning untrusted text ---------- */

// logText removes control characters (and invalid UTF-8), trims, and cuts the
// text to at most max characters. Everything stored in visit_logs passes through it.
func logText(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		s = strings.TrimSpace(string([]rune(s)[:max]))
	}
	return s
}

// formatNumber writes 8 as "8" and 0.5 as "0.5".
func formatNumber(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

/* ---------- Page-view beacon ---------- */

// visitRequest is the beacon body. Every field is optional; numbers may arrive as strings.
type visitRequest struct {
	Path     string      `json:"path"`
	Referrer string      `json:"referrer"`
	Lang     string      `json:"lang"`
	TZ       string      `json:"tz"`
	Screen   string      `json:"screen"`
	DPR      flexNumber  `json:"dpr"`
	Touch    flexNumber  `json:"touch"`
	Cores    flexNumber  `json:"cores"`
	Memory   flexNumber  `json:"memory"`
	Conn     string      `json:"conn"`
	Hints    clientHints `json:"hints"`
}

// hardware summarises the device's capabilities, e.g. "8 nüvə · 8 GB RAM · DPR 3 · toxunma: 5 · arm 64-bit".
func (req *visitRequest) hardware() string {
	var parts []string
	if req.Cores > 0 && req.Cores < 1024 {
		parts = append(parts, formatNumber(float64(req.Cores))+" nüvə")
	}
	if req.Memory > 0 && req.Memory < 4096 {
		parts = append(parts, formatNumber(float64(req.Memory))+" GB RAM")
	}
	if req.DPR > 0 && req.DPR < 100 {
		parts = append(parts, "DPR "+formatNumber(float64(req.DPR)))
	}
	if req.Touch > 0 && req.Touch < 1000 {
		parts = append(parts, "toxunma: "+formatNumber(float64(req.Touch)))
	}
	if arch := logText(req.Hints.Architecture, 20); arch != "" {
		if bits := logText(req.Hints.Bitness, 5); bits != "" {
			arch += " " + bits + "-bit"
		}
		parts = append(parts, arch)
	}
	return strings.Join(parts, " · ")
}

// recordVisit answers 204 even when the row is dropped (rate limit, full queue, foreign
// site), so the page never waits for or learns about logging. Only unreadable JSON gets 400.
func (a *App) recordVisit(w http.ResponseWriter, r *http.Request) {
	var req visitRequest
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusBadRequest, "Sorğu JSON formatında olmalıdır.", "")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxVisitBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Sorğu oxuna bilmədi.", "")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	ip := a.clientIP(r)
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" || !a.visitLimit.allow(ip) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	ua := r.UserAgent()
	dev := parseDevice(ua, req.Hints, int(req.Touch))
	path := logText(req.Path, maxLogTextLen)
	if !strings.HasPrefix(path, "/") {
		path = ""
	}
	language := logText(req.Lang, maxLogTextLen)
	if language == "" {
		language = acceptLanguage(r)
	}
	a.visits.add(VisitLog{
		TS:         time.Now(),
		Kind:       kindVisit,
		IP:         logText(ip, maxLogTextLen),
		Path:       path,
		Referrer:   logText(req.Referrer, maxLogTextLen),
		DeviceType: dev.Type,
		Device:     logText(dev.Device, maxLogTextLen),
		OS:         logText(dev.OS, maxLogTextLen),
		Browser:    logText(dev.Browser, maxLogTextLen),
		Screen:     logText(req.Screen, maxLogTextLen),
		Language:   language,
		Timezone:   logText(req.TZ, maxLogTextLen),
		Hardware:   logText(req.hardware(), maxLogTextLen),
		Connection: logText(req.Conn, maxLogTextLen),
		UserAgent:  logText(ua, maxUserAgentLen),
	})
	w.WriteHeader(http.StatusNoContent)
}

/* ---------- Admin logins ---------- */

// logLogin records an admin panel login attempt. The password is never part of it.
func (a *App) logLogin(r *http.Request, kind, username string) {
	ua := r.UserAgent()
	dev := parseDevice(ua, headerHints(r), 0)
	rec := VisitLog{
		TS:         time.Now(),
		Kind:       kind,
		Username:   logText(username, maxLogUsername),
		IP:         logText(a.clientIP(r), maxLogTextLen),
		Path:       logText(r.URL.Path, maxLogTextLen),
		Referrer:   logText(r.Referer(), maxLogTextLen),
		DeviceType: dev.Type,
		Device:     logText(dev.Device, maxLogTextLen),
		OS:         logText(dev.OS, maxLogTextLen),
		Browser:    logText(dev.Browser, maxLogTextLen),
		Language:   acceptLanguage(r),
		UserAgent:  logText(ua, maxUserAgentLen),
	}
	if a.visits.add(rec) {
		return
	}
	// The queue is full (for example during a beacon flood). Login records are security
	// evidence, so save this one now without the provider lookup instead of dropping it.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.store.AddVisit(ctx, rec); err != nil {
		log.Printf("login log (%s, %q, %s) could not be saved: %v", kind, rec.Username, rec.IP, err)
	}
}

// headerHints reads the low-entropy client hints that Chromium browsers send as headers
// on HTTPS requests: Sec-CH-UA, Sec-CH-UA-Mobile and Sec-CH-UA-Platform.
func headerHints(r *http.Request) clientHints {
	h := clientHints{
		Platform: strings.Trim(r.Header.Get("Sec-CH-UA-Platform"), `"`),
		Mobile:   r.Header.Get("Sec-CH-UA-Mobile") == "?1",
	}
	// Sec-CH-UA: "Chromium";v="130", "Google Chrome";v="130", "Not?A_Brand";v="99"
	for _, item := range strings.Split(r.Header.Get("Sec-CH-UA"), ",") {
		brand, params, _ := strings.Cut(strings.TrimSpace(item), ";")
		version := strings.TrimPrefix(strings.TrimSpace(params), "v=")
		if brand = strings.Trim(brand, `"`); brand != "" {
			h.Brands = append(h.Brands, hintBrand{Brand: brand, Version: strings.Trim(version, `"`)})
		}
	}
	return h
}

// acceptLanguage returns the first language of the Accept-Language header, e.g. "az-AZ".
func acceptLanguage(r *http.Request) string {
	first, _, _ := strings.Cut(r.Header.Get("Accept-Language"), ",")
	first, _, _ = strings.Cut(first, ";")
	return logText(first, 35)
}

/* ---------- Admin: log list ---------- */

// queryInt reads a whole number from the query string, kept within [lo, hi].
func queryInt(r *http.Request, name string, fallback, lo, hi int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return fallback
	}
	return max(lo, min(n, hi))
}

func (a *App) listVisits(w http.ResponseWriter, r *http.Request, _ session) {
	page := queryInt(r, "page", 1, 1, 100000)
	size := queryInt(r, "size", visitsPageSize, 1, visitsMaxPerPage)
	kind := r.URL.Query().Get("kind")
	if !slices.Contains(visitKinds, kind) {
		kind = "" // "all" or anything unknown
	}
	q := logText(r.URL.Query().Get("q"), 100)

	items, total, err := a.store.Visits(r.Context(), kind, q, size, (page-1)*size)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	now := time.Now().In(baku)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, baku)
	stats, err := a.store.VisitStats(r.Context(), today, now)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": total,
		"page":  page,
		"size":  size,
		"stats": stats,
	})
}

/* ---------- Retention ---------- */

// pruneVisits deletes log rows older than the retention period.
func (a *App) pruneVisits(ctx context.Context) {
	n, err := a.store.PruneVisits(ctx, time.Now().Add(-visitRetention))
	switch {
	case err != nil && !errors.Is(err, context.Canceled):
		log.Printf("visit log cleanup: %v", err)
	case n > 0:
		log.Printf("visit log cleanup: %d köhnə qeyd silindi", n)
	}
}

// runRetention cleans the log at start-up and then once a day until ctx ends.
func (a *App) runRetention(ctx context.Context) {
	a.pruneVisits(ctx)
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.pruneVisits(ctx)
		}
	}
}
