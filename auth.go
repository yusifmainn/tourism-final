package main

import (
	"context"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	roleAdmin    = "admin"
	roleFinance  = "finance"
	roleOperator = "operator"

	sessionCookie = "aze_session"
	cookiePath    = "/api/admin"
	sessionTTL    = 12 * time.Hour

	pbkdf2Iterations = 600_000

	// The client asked for admin / admin as the starting login. ADMIN_PASSWORD
	// replaces it, and the server warns in its log for as long as it is in use.
	defaultAdminPassword = "admin"
)

// accounts are the fixed admin panel users.
var accounts = []struct{ username, role, envVar, defaultPassword string }{
	{"admin", roleAdmin, "ADMIN_PASSWORD", defaultAdminPassword},
	{"maliyye", roleFinance, "FINANCE_PASSWORD", ""},
	{"isci", roleOperator, "OPERATOR_PASSWORD", ""},
}

// App holds what the handlers share.
type App struct {
	cfg    Config
	store  *Store
	bank   *kapitalClient // nil when online payment is off (see Config.onlinePayment)
	visits *visitLogger

	loginLimit   *rateLimiter
	bookingLimit *rateLimiter
	payLimit     *rateLimiter // new payment attempts
	statusLimit  *rateLimiter // payment status checks
	returnLimit  *rateLimiter // bank re-checks triggered by the return URL
	visitLimit   *rateLimiter // page-view beacons; generous because mobile carriers share IPs (CGNAT)

	background sync.WaitGroup
}

func newApp(cfg Config, store *Store) *App {
	a := &App{
		cfg:          cfg,
		store:        store,
		visits:       newVisitLogger(store, newProviderLookup(net.DefaultResolver).lookup),
		loginLimit:   newRateLimiter(10, 15*time.Minute),
		bookingLimit: newRateLimiter(60, time.Hour),
		payLimit:     newRateLimiter(30, time.Hour),
		statusLimit:  newRateLimiter(600, time.Hour),
		returnLimit:  newRateLimiter(120, time.Hour),
		visitLimit:   newRateLimiter(600, time.Hour),
	}
	if cfg.onlinePayment() {
		a.bank = newKapitalClient(cfg.KapitalURL, cfg.KapitalUsername, cfg.KapitalPassword)
	}
	return a
}

// startBackground starts the visit log workers, the daily log cleanup and, with online
// payment on, the payment reconciler. The jobs stop when ctx ends; then call stopBackground.
func (a *App) startBackground(ctx context.Context) {
	a.visits.start()
	a.background.Add(1)
	go func() {
		defer a.background.Done()
		a.runRetention(ctx)
	}()
	if a.bank != nil {
		a.background.Add(1)
		go func() {
			defer a.background.Done()
			a.runReconciler(ctx)
		}()
	}
}

// stopBackground saves the queued visit logs and waits for the jobs, at most until ctx ends.
func (a *App) stopBackground(ctx context.Context) {
	a.visits.stop(ctx)
	done := make(chan struct{})
	go func() {
		a.background.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// ensureUsers applies passwords from the environment. A new account with no
// password set uses its default or, if it has none, a random one printed once to the log.
func (a *App) ensureUsers(ctx context.Context) error {
	for _, acc := range accounts {
		existing, exists, err := a.store.User(ctx, acc.username)
		if err != nil {
			return err
		}
		password := a.cfg.Passwords[acc.username]
		switch {
		case password == "" && exists:
			continue
		case password == "" && acc.defaultPassword != "":
			password = acc.defaultPassword
		case password == "":
			password = randomHex(10)
			log.Printf("Yeni parol yaradıldı: %s / %s (%s ilə dəyişmək olar)", acc.username, password, acc.envVar)
		case exists && checkPassword(existing.Hash, password):
			continue
		}
		hash, err := hashPassword(password)
		if err != nil {
			return err
		}
		if err := a.store.SetUser(ctx, acc.username, User{Role: acc.role, Hash: hash}); err != nil {
			return err
		}
	}

	admin, exists, err := a.store.User(ctx, "admin")
	if err != nil {
		return err
	}
	if exists && checkPassword(admin.Hash, defaultAdminPassword) {
		log.Print("DİQQƏT: admin parolu standart \"admin\" dəyərindədir. Real müştərilərdən əvvəl ADMIN_PASSWORD ilə dəyişin.")
	}
	return nil
}

/* ---------- Passwords ---------- */

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d$%s$%s", pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func checkPassword(stored, password string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 3 {
		return false
	}
	iterations, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

/* ---------- Sessions ---------- */

type session struct {
	Username string
	Role     string
}

type authedHandler func(w http.ResponseWriter, r *http.Request, s session)

// auth requires a logged-in user and, when roles are given, one of those roles.
func (a *App) auth(h authedHandler, roles ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "Giriş tələb olunur.", "")
			return
		}
		s, ok, err := a.store.Session(r.Context(), c.Value)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "Giriş tələb olunur.", "")
			return
		}
		if len(roles) > 0 && !slices.Contains(roles, s.Role) {
			writeError(w, http.StatusForbidden, "Bu əməliyyat üçün icazəniz yoxdur.", "")
			return
		}
		h(w, r, s)
	}
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	ip := a.clientIP(r)
	if !a.loginLimit.allow(ip) {
		writeError(w, http.StatusTooManyRequests, "Çox sayda cəhd. Bir az sonra yenidən yoxlayın.", "")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, 4<<10, &req) {
		return
	}
	username := strings.ToLower(cleanText(req.Username))
	user, ok, err := a.store.User(r.Context(), username)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !ok || !checkPassword(user.Hash, req.Password) {
		a.logLogin(r, kindLoginFail, username)
		writeError(w, http.StatusUnauthorized, "İstifadəçi adı və ya parol yanlışdır.", "")
		return
	}
	a.loginLimit.reset(ip)

	token, expires, err := a.store.CreateSession(r.Context(), username, sessionTTL)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     cookiePath,
		Expires:  expires,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode, // this also protects the admin API from CSRF
	})
	a.logLogin(r, kindLoginOK, username)
	writeJSON(w, http.StatusOK, map[string]string{"username": username, "role": user.Role})
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := a.store.DeleteSession(r.Context(), c.Value); err != nil {
			writeInternalError(w, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Path:     cookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) me(w http.ResponseWriter, _ *http.Request, s session) {
	writeJSON(w, http.StatusOK, map[string]string{"username": s.Username, "role": s.Role})
}

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// clientIP uses the proxy's header only when TRUST_PROXY=true. The last
// X-Forwarded-For entry is the one our own proxy added; earlier ones can be forged.
func (a *App) clientIP(r *http.Request) string {
	if a.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

/* ---------- Rate limiting ---------- */

type rateEntry struct {
	count   int
	resetAt time.Time
}

// rateLimiter allows `limit` calls per key in each time window.
type rateLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	hits   map[string]*rateEntry
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, hits: map[string]*rateEntry{}}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.hits) > 10000 {
		for k, e := range l.hits {
			if now.After(e.resetAt) {
				delete(l.hits, k)
			}
		}
	}
	e, ok := l.hits[key]
	if !ok || now.After(e.resetAt) {
		l.hits[key] = &rateEntry{count: 1, resetAt: now.Add(l.window)}
		return true
	}
	e.count++
	return e.count <= l.limit
}

func (l *rateLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.hits, key)
}
