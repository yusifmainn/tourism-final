// Aze-Tourism backend: a single program that serves the website and its JSON API,
// with the data in PostgreSQL. Setup instructions are in DEPLOY.md.
package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

//go:embed index.html 404.html favicon.ico admin rezervasiya sertler assets
var webFiles embed.FS

//go:embed schema.sql
var schemaSQL string

//go:embed seed.json
var seedJSON []byte

// Config is read from environment variables, or from a .env file (see loadDotEnv).
type Config struct {
	Port        string
	DatabaseURL string
	TrustProxy  bool              // true when running behind Caddy/nginx
	Passwords   map[string]string // username -> password from the environment

	// Online deposit payment (Kapital Bank). It is on only when all three of
	// KapitalUsername, KapitalPassword and PublicURL are set.
	PublicURL       string // the site's own address, e.g. https://aze-tourism.az (no trailing slash)
	KapitalURL      string // the bank's API base URL
	KapitalUsername string
	KapitalPassword string // never logged
}

func (c Config) onlinePayment() bool {
	return c.KapitalUsername != "" && c.KapitalPassword != "" && c.PublicURL != ""
}

func loadConfig() Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	kapitalURL := strings.TrimRight(strings.TrimSpace(os.Getenv("KAPITAL_API_URL")), "/")
	if kapitalURL == "" {
		kapitalURL = kapitalProductionURL
	}
	return Config{
		Port:        port,
		DatabaseURL: os.Getenv("DATABASE_URL"),
		TrustProxy:  os.Getenv("TRUST_PROXY") == "true",
		Passwords: map[string]string{
			"admin":   os.Getenv("ADMIN_PASSWORD"),
			"maliyye": os.Getenv("FINANCE_PASSWORD"),
			"isci":    os.Getenv("OPERATOR_PASSWORD"),
		},
		PublicURL:       publicURL(os.Getenv("PUBLIC_URL"), os.Getenv("DOMAIN")),
		KapitalURL:      kapitalURL,
		KapitalUsername: strings.TrimSpace(os.Getenv("KAPITAL_USERNAME")),
		KapitalPassword: os.Getenv("KAPITAL_PASSWORD"),
	}
}

var hostnamePattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)

// publicURL returns PUBLIC_URL without a trailing slash or, when it is empty and DOMAIN
// (the Docker/Caddy setting) is a plain host name, "https://DOMAIN". "" means unknown.
func publicURL(explicit, domain string) string {
	if s := strings.TrimRight(strings.TrimSpace(explicit), "/"); s != "" {
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" {
			log.Printf("PUBLIC_URL düzgün deyil (%q): https://domen.az formasında yazın", s)
			return ""
		}
		return s
	}
	if d := strings.ToLower(strings.TrimSpace(domain)); hostnamePattern.MatchString(d) {
		return "https://" + d
	}
	return ""
}

// logPaymentMode writes the one start-up line about online payment. Secrets are never printed.
func logPaymentMode(cfg Config) {
	if cfg.onlinePayment() {
		log.Printf("Onlayn ödəniş: aktivdir (Kapital Bank %s, qayıdış ünvanı %s%s)", cfg.KapitalURL, cfg.PublicURL, paymentReturn)
		return
	}
	var missing []string
	for name, value := range map[string]string{
		"KAPITAL_USERNAME": cfg.KapitalUsername, "KAPITAL_PASSWORD": cfg.KapitalPassword, "PUBLIC_URL": cfg.PublicURL,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	slices.Sort(missing)
	log.Printf("Onlayn ödəniş: söndürülüb (%s təyin edilməyib). Rezervasiyalar qəbul olunur, depoziti əməkdaşlar admin paneldə qeyd edir.",
		strings.Join(missing, ", "))
}

// loadDotEnv reads KEY=VALUE lines from a file, if it exists. Real environment
// variables win, so settings from Docker or systemd are never overridden.
func loadDotEnv(path string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, strings.TrimSpace(value))
		}
	}
}

func main() {
	loadDotEnv(".env")
	cfg := loadConfig()
	if cfg.DatabaseURL == "" {
		log.Fatal("DATABASE_URL təyin edilməyib. Quraşdırma üçün DEPLOY.md faylına baxın.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := openStore(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer store.Close()
	if err := store.migrate(ctx, schemaSQL, seedJSON); err != nil {
		log.Fatalf("database: %v", err)
	}

	app := newApp(cfg, store)
	if err := app.ensureUsers(ctx); err != nil {
		log.Fatalf("users: %v", err)
	}
	logPaymentMode(cfg)
	app.startBackground(ctx) // stops when ctx ends (Ctrl+C / SIGTERM)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           app.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	go func() {
		log.Printf("Aze-Tourism: http://localhost:%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	app.stopBackground(shutdownCtx) // saves the visit logs still in the queue
}

// routes wires the whole HTTP surface. Role rules:
//   - admin:    everything, including the visit and login log
//   - finance:  bookings + deposit payment details (no ID document, no content editing)
//   - operator: bookings + ID document + FAQ/terms/stats editing (no payment details)
func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	content := []string{roleAdmin, roleOperator}
	document := []string{roleAdmin, roleOperator}

	mux.HandleFunc("GET /healthz", a.healthz)

	// Public API
	mux.HandleFunc("GET /api/config", a.getConfig)
	mux.HandleFunc("GET /api/faq", a.getFAQ)
	mux.HandleFunc("GET /api/terms", a.getTerms)
	mux.HandleFunc("GET /api/stats", a.getStats)
	mux.HandleFunc("POST /api/bookings", a.createBooking)
	mux.HandleFunc("POST /api/bookings/{ref}/pay", a.payBooking)
	mux.HandleFunc("GET /api/bookings/{ref}/payment", a.paymentInfo)
	mux.HandleFunc("POST /api/visit", a.recordVisit)

	// Kapital Bank sends the customer back here after the payment page.
	mux.HandleFunc("GET "+paymentReturn, a.paymentReturned)

	// Admin API
	mux.HandleFunc("POST /api/admin/login", a.login)
	mux.HandleFunc("POST /api/admin/logout", a.logout)
	mux.HandleFunc("GET /api/admin/me", a.auth(a.me))
	mux.HandleFunc("GET /api/admin/bookings", a.auth(a.listBookings))
	mux.HandleFunc("GET /api/admin/bookings/{ref}", a.auth(a.getBooking))
	mux.HandleFunc("PATCH /api/admin/bookings/{ref}", a.auth(a.updateBooking)) // paymentStatus: admin/finance (checked inside)
	mux.HandleFunc("GET /api/admin/bookings/{ref}/document", a.auth(a.getDocument, document...))
	mux.HandleFunc("GET /api/admin/visits", a.auth(a.listVisits, roleAdmin))
	mux.HandleFunc("POST /api/admin/faq", a.auth(a.createFAQ, content...))
	mux.HandleFunc("PUT /api/admin/faq/{id}", a.auth(a.updateFAQ, content...))
	mux.HandleFunc("DELETE /api/admin/faq/{id}", a.auth(a.deleteFAQ, content...))
	mux.HandleFunc("PUT /api/admin/terms", a.auth(a.putTerms, content...))
	mux.HandleFunc("PUT /api/admin/stats", a.auth(a.putStats, content...))

	// Website (the handler itself rejects methods other than GET and HEAD).
	mux.Handle("/", staticHandler(webFiles))

	return logRequests(securityHeaders(mux))
}

// staticHandler serves the embedded website: no directory listings, and the
// site's own 404 page for anything that does not exist.
func staticHandler(files fs.FS) http.Handler {
	fileServer := http.FileServerFS(files)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" || strings.HasSuffix(name, "/") {
			name += "index.html"
		}
		if _, err := fs.Stat(files, name); err != nil {
			page, err := fs.ReadFile(files, "404.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			w.Write(page)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src https://fonts.gstatic.com; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		// %q keeps a decoded newline in the path from forging extra log lines.
		log.Printf("%s %q %d %s", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Millisecond))
	})
}
