package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	_ "github.com/lib/pq" // PostgreSQL driver for database/sql
)

// I18n is a text in the three site languages.
type I18n struct {
	AZ string `json:"az"`
	RU string `json:"ru"`
	EN string `json:"en"`
}

// FAQ is one question and answer on the home page.
type FAQ struct {
	ID string `json:"id"`
	Q  I18n   `json:"q"`
	A  I18n   `json:"a"`
}

// Deposit payment states (bookings.payment_status) and methods (bookings.pay_method).
const (
	payPending = "pending"
	payPaid    = "paid"
	payFailed  = "failed"

	methodBank   = "Kapital Bank" // confirmed by the bank's API
	methodManual = "Əl ilə"       // staff marked the deposit as received outside the website
)

// Payment is the deposit state shown to admin and finance staff. It holds no card data;
// the card's last 4 digits and expiry are in Booking and go to the admin role only.
type Payment struct {
	Status      string     `json:"status"`      // pending | paid | failed
	Method      string     `json:"method"`      // methodBank, methodManual or ""
	BankOrderID string     `json:"bankOrderId"` // the paid (or else the latest) bank order, "" if none
	PaidAt      *time.Time `json:"paidAt"`
	Amount      int        `json:"amount"`
}

// Booking is one person's registration. The JSON names match the frontend.
type Booking struct {
	Ref           string    `json:"ref"`
	TS            time.Time `json:"ts"`
	Ad            string    `json:"ad"`
	Soyad         string    `json:"soyad"`
	Ata           string    `json:"ata"`
	FIN           string    `json:"fin"`
	Email         string    `json:"email"`
	Tel           string    `json:"tel"`
	Country       string    `json:"country"`
	TourDate      string    `json:"tourDate"` // YYYY-MM-DD
	Tarix         string    `json:"tarix"`    // the same date as text for the admin panel
	Unvan         string    `json:"unvan"`
	Seher         string    `json:"seher"`
	Postal        string    `json:"postal"`
	IDName        string    `json:"idName,omitempty"`
	HasDocument   bool      `json:"-"`
	Mebleg        int       `json:"mebleg"` // the deposit in AZN
	Status        string    `json:"status"`
	PaymentStatus string    `json:"-"` // also inside Payment; the list sends it per role
	Payment       *Payment  `json:"payment,omitempty"`
	CardLast4     string    `json:"-"` // last 4 digits; sent to the admin role only, see detailFor
	CardExp       string    `json:"-"` // MM/YY
}

// Document is an uploaded ID card photo.
type Document struct {
	Name string
	Mime string
	Data []byte
}

// User is an admin panel account.
type User struct {
	Role string
	Hash string
}

// Store is the PostgreSQL data layer.
type Store struct {
	db *sql.DB
}

// openStore connects to PostgreSQL, waiting for it if it is still starting.
func openStore(ctx context.Context, databaseURL string) (*Store, error) {
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	const attempts = 30
	for attempt := 1; ; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = db.PingContext(pingCtx)
		cancel()
		if err == nil {
			return &Store{db: db}, nil
		}
		if attempt == attempts {
			db.Close()
			return nil, fmt.Errorf("PostgreSQL-ə qoşulmaq olmadı: %w", err)
		}
		log.Printf("PostgreSQL gözlənilir (%d/%d): %v", attempt, attempts, err)
		select {
		case <-ctx.Done():
			db.Close()
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// Close releases the database connections.
func (s *Store) Close() error { return s.db.Close() }

// Ping reports whether the database answers. It backs the /healthz check.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// migrate creates the tables and, on the very first run, loads the default content.
func (s *Store) migrate(ctx context.Context, schema string, seed []byte) error {
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	var defaults struct {
		FAQ   []FAQ  `json:"faq"`
		Terms I18n   `json:"terms"`
		Stats []Stat `json:"stats"`
	}
	if err := json.Unmarshal(seed, &defaults); err != nil {
		return fmt.Errorf("seed: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // does nothing after a successful Commit

	// The terms row doubles as the "first run" marker, so FAQ items that staff
	// delete later do not come back when the server restarts.
	res, err := tx.ExecContext(ctx,
		`INSERT INTO terms (id, az, ru, en) VALUES (1, $1, $2, $3) ON CONFLICT (id) DO NOTHING`,
		defaults.Terms.AZ, defaults.Terms.RU, defaults.Terms.EN)
	if err != nil {
		return err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 1 {
		for _, item := range defaults.FAQ {
			if _, err := tx.ExecContext(ctx, insertFAQSQL, faqArgs(item)...); err != nil {
				return err
			}
		}
	}
	// Each trust-strip position gets its default once (also on a database made before this
	// table existed); figures edited in the admin panel are never overwritten.
	for i, st := range defaults.Stats {
		if i >= statCount {
			break
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO site_stats (id, value, label_az, label_ru, label_en) VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (id) DO NOTHING`, i+1, st.Value, st.Label.AZ, st.Label.RU, st.Label.EN); err != nil {
			return err
		}
	}
	return tx.Commit()
}

/* ---------- Users ---------- */

// User returns false when no account has that name.
func (s *Store) User(ctx context.Context, username string) (User, bool, error) {
	var u User
	err := s.db.QueryRowContext(ctx,
		`SELECT role, password_hash FROM users WHERE username = $1`, username).Scan(&u.Role, &u.Hash)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	return u, true, nil
}

// SetUser creates or updates an account and ends its open sessions.
func (s *Store) SetUser(ctx context.Context, username string, u User) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, role, password_hash) VALUES ($1, $2, $3)
		 ON CONFLICT (username) DO UPDATE SET role = EXCLUDED.role, password_hash = EXCLUDED.password_hash`,
		username, u.Role, u.Hash)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE username = $1`, username)
	return err
}

/* ---------- Sessions ---------- */

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSession stores a new login and returns its token, also clearing expired logins.
func (s *Store) CreateSession(ctx context.Context, username string, ttl time.Duration) (string, time.Time, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < now()`); err != nil {
		return "", time.Time{}, err
	}
	token := randomHex(32)
	expires := time.Now().Add(ttl)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, username, expires_at) VALUES ($1, $2, $3)`,
		hashToken(token), username, expires)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

// Session returns false when the token is unknown or expired.
func (s *Store) Session(ctx context.Context, token string) (session, bool, error) {
	var sess session
	err := s.db.QueryRowContext(ctx,
		`SELECT u.username, u.role
		 FROM sessions s JOIN users u ON u.username = s.username
		 WHERE s.token_hash = $1 AND s.expires_at > now()`,
		hashToken(token)).Scan(&sess.Username, &sess.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return session{}, false, nil
	}
	if err != nil {
		return session{}, false, err
	}
	return sess, true, nil
}

// DeleteSession ends a login.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hashToken(token))
	return err
}

/* ---------- FAQ ---------- */

const insertFAQSQL = `INSERT INTO faq (id, q_az, q_ru, q_en, a_az, a_ru, a_en) VALUES ($1, $2, $3, $4, $5, $6, $7)`

func faqArgs(item FAQ) []any {
	return []any{item.ID, item.Q.AZ, item.Q.RU, item.Q.EN, item.A.AZ, item.A.RU, item.A.EN}
}

// FAQ returns all items in display order. The result is never nil.
func (s *Store) FAQ(ctx context.Context) ([]FAQ, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, q_az, q_ru, q_en, a_az, a_ru, a_en FROM faq ORDER BY sort_order`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []FAQ{}
	for rows.Next() {
		var it FAQ
		if err := rows.Scan(&it.ID, &it.Q.AZ, &it.Q.RU, &it.Q.EN, &it.A.AZ, &it.A.RU, &it.A.EN); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// AddFAQ appends an item after the existing ones.
func (s *Store) AddFAQ(ctx context.Context, item FAQ) error {
	_, err := s.db.ExecContext(ctx, insertFAQSQL, faqArgs(item)...)
	return err
}

// UpdateFAQ returns false when no item has that ID.
func (s *Store) UpdateFAQ(ctx context.Context, item FAQ) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE faq SET q_az = $2, q_ru = $3, q_en = $4, a_az = $5, a_ru = $6, a_en = $7 WHERE id = $1`,
		faqArgs(item)...)
	return changedOneRow(res, err)
}

// DeleteFAQ returns false when no item has that ID.
func (s *Store) DeleteFAQ(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM faq WHERE id = $1`, id)
	return changedOneRow(res, err)
}

func changedOneRow(res sql.Result, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

/* ---------- Terms ---------- */

// Terms returns the terms text in all languages.
func (s *Store) Terms(ctx context.Context) (I18n, error) {
	var t I18n
	err := s.db.QueryRowContext(ctx, `SELECT az, ru, en FROM terms WHERE id = 1`).Scan(&t.AZ, &t.RU, &t.EN)
	if errors.Is(err, sql.ErrNoRows) {
		return I18n{}, nil
	}
	return t, err
}

// SetTerms replaces the terms text.
func (s *Store) SetTerms(ctx context.Context, t I18n) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO terms (id, az, ru, en) VALUES (1, $1, $2, $3)
		 ON CONFLICT (id) DO UPDATE SET az = EXCLUDED.az, ru = EXCLUDED.ru, en = EXCLUDED.en`,
		t.AZ, t.RU, t.EN)
	return err
}

/* ---------- Trust strip (home page figures) ---------- */

// statCount is the number of positions in the home page's trust strip.
const statCount = 4

// Stat is one figure of the trust strip, e.g. value "12+" with label "illik təcrübə".
// An empty value hides the position on the site.
type Stat struct {
	Value string `json:"value"`
	Label I18n   `json:"label"`
}

// Stats returns exactly statCount figures in page order (missing rows are empty).
func (s *Store) Stats(ctx context.Context) ([]Stat, error) {
	out := make([]Stat, statCount)
	rows, err := s.db.QueryContext(ctx, `SELECT id, value, label_az, label_ru, label_en FROM site_stats ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var st Stat
		if err := rows.Scan(&id, &st.Value, &st.Label.AZ, &st.Label.RU, &st.Label.EN); err != nil {
			return nil, err
		}
		if id >= 1 && id <= statCount {
			out[id-1] = st
		}
	}
	return out, rows.Err()
}

// SetStats replaces all statCount figures.
func (s *Store) SetStats(ctx context.Context, stats []Stat) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // does nothing after a successful Commit
	for i, st := range stats {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO site_stats (id, value, label_az, label_ru, label_en) VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (id) DO UPDATE SET value = EXCLUDED.value, label_az = EXCLUDED.label_az,
			     label_ru = EXCLUDED.label_ru, label_en = EXCLUDED.label_en`,
			i+1, st.Value, st.Label.AZ, st.Label.RU, st.Label.EN); err != nil {
			return err
		}
	}
	return tx.Commit()
}

/* ---------- Bookings ---------- */

// Bookings returns the columns the admin list needs, newest first. The result is never nil.
func (s *Store) Bookings(ctx context.Context) ([]Booking, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ref, created_at, ad, soyad, to_char(tour_date, 'YYYY-MM-DD'), status, mebleg, payment_status
		 FROM bookings ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Booking{}
	for rows.Next() {
		var b Booking
		if err := rows.Scan(&b.Ref, &b.TS, &b.Ad, &b.Soyad, &b.TourDate, &b.Status, &b.Mebleg, &b.PaymentStatus); err != nil {
			return nil, err
		}
		b.Tarix = tourRange(b.TourDate)
		out = append(out, b)
	}
	return out, rows.Err()
}

// Booking returns false when no booking has that reference.
func (s *Store) Booking(ctx context.Context, ref string) (Booking, bool, error) {
	var b Booking
	var p Payment
	var paidAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT b.ref, b.created_at, b.ad, b.soyad, b.ata, b.fin, b.email, b.tel, b.country,
		        to_char(b.tour_date, 'YYYY-MM-DD'), b.unvan, b.seher, b.postal, b.mebleg, b.status,
		        b.payment_status, b.pay_method, b.paid_at, b.card_tail, b.card_expiry,
		        COALESCE((SELECT o.order_id::text FROM payment_orders o WHERE o.booking_ref = b.ref
		                  ORDER BY o.paid DESC, o.created_at DESC, o.order_id DESC LIMIT 1), ''),
		        COALESCE(d.file_name, ''), d.booking_ref IS NOT NULL
		 FROM bookings b LEFT JOIN booking_documents d ON d.booking_ref = b.ref
		 WHERE b.ref = $1`, ref).Scan(
		&b.Ref, &b.TS, &b.Ad, &b.Soyad, &b.Ata, &b.FIN, &b.Email, &b.Tel, &b.Country,
		&b.TourDate, &b.Unvan, &b.Seher, &b.Postal, &b.Mebleg, &b.Status,
		&p.Status, &p.Method, &paidAt, &b.CardLast4, &b.CardExp, &p.BankOrderID,
		&b.IDName, &b.HasDocument)
	if errors.Is(err, sql.ErrNoRows) {
		return Booking{}, false, nil
	}
	if err != nil {
		return Booking{}, false, err
	}
	b.Tarix = tourRange(b.TourDate)
	if paidAt.Valid {
		p.PaidAt = &paidAt.Time
	}
	p.Amount = b.Mebleg
	b.PaymentStatus = p.Status
	b.Payment = &p
	return b, true, nil
}

// AddBooking saves the booking and its document together and returns the new reference.
// The deposit starts as pending.
func (s *Store) AddBooking(ctx context.Context, b Booking, doc Document) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() // does nothing after a successful Commit

	var ref string
	for {
		ref = fmt.Sprintf("AZT-%d-%d", 100000+randomInt(900000), b.TS.Year())
		res, err := tx.ExecContext(ctx,
			`INSERT INTO bookings (ref, created_at, ad, soyad, ata, fin, email, tel, country, tour_date,
			                       unvan, seher, postal, mebleg, status, payment_status, card_tail, card_expiry)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, 'pending', $16, $17)
			 ON CONFLICT (ref) DO NOTHING`,
			ref, b.TS, b.Ad, b.Soyad, b.Ata, b.FIN, b.Email, b.Tel, b.Country, b.TourDate,
			b.Unvan, b.Seher, b.Postal, b.Mebleg, b.Status, b.CardLast4, b.CardExp)
		inserted, err := changedOneRow(res, err)
		if err != nil {
			return "", err
		}
		if inserted {
			break // otherwise this reference is taken: try another one
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO booking_documents (booking_ref, file_name, mime_type, data) VALUES ($1, $2, $3, $4)`,
		ref, doc.Name, doc.Mime, doc.Data); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return ref, nil
}

// SetBookingStatus returns false when no booking has that reference.
func (s *Store) SetBookingStatus(ctx context.Context, ref, status string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE bookings SET status = $1 WHERE ref = $2`, status, ref)
	return changedOneRow(res, err)
}

// Document returns false when the booking has no document.
func (s *Store) Document(ctx context.Context, ref string) (Document, bool, error) {
	var d Document
	err := s.db.QueryRowContext(ctx,
		`SELECT file_name, mime_type, data FROM booking_documents WHERE booking_ref = $1`, ref).Scan(&d.Name, &d.Mime, &d.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, false, nil
	}
	if err != nil {
		return Document{}, false, err
	}
	return d, true, nil
}

/* ---------- Deposit payments ---------- */

// syncPaymentSQL derives a booking's deposit state ($1 = ref) from its bank orders:
// a paid bank order wins; otherwise a staff mark ($2 = methodManual) stays; otherwise
// the newest order decides (finished without payment: failed; open or none: pending).
const syncPaymentSQL = `
UPDATE bookings b SET
    payment_status = CASE WHEN x.bank_paid_at IS NOT NULL OR b.pay_method = $2 THEN 'paid'
                          WHEN x.last_final THEN 'failed'
                          ELSE 'pending' END,
    pay_method     = CASE WHEN x.bank_paid_at IS NOT NULL THEN $3
                          WHEN b.pay_method = $2 THEN b.pay_method
                          ELSE '' END,
    paid_at        = CASE WHEN x.bank_paid_at IS NOT NULL THEN x.bank_paid_at
                          WHEN b.pay_method = $2 THEN b.paid_at END
FROM (SELECT (SELECT min(paid_at) FROM payment_orders WHERE booking_ref = $1 AND paid) AS bank_paid_at,
             COALESCE((SELECT final FROM payment_orders WHERE booking_ref = $1
                       ORDER BY created_at DESC, order_id DESC LIMIT 1), false) AS last_final) x
WHERE b.ref = $1`

func syncPayment(ctx context.Context, tx *sql.Tx, ref string) error {
	_, err := tx.ExecContext(ctx, syncPaymentSQL, ref, methodManual, methodBank)
	return err
}

// AddPaymentOrder records a new bank order for a booking. A booking whose earlier
// attempt failed becomes pending again while the new order is open.
func (s *Store) AddPaymentOrder(ctx context.Context, ref string, orderID int64, amount int, currency, bankStatus string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // does nothing after a successful Commit
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO payment_orders (order_id, booking_ref, amount, currency, bank_status) VALUES ($1, $2, $3, $4, $5)`,
		orderID, ref, amount, currency, bankStatus); err != nil {
		return err
	}
	if err := syncPayment(ctx, tx, ref); err != nil {
		return err
	}
	return tx.Commit()
}

// PaymentOrderRef returns the booking a bank order belongs to, or false for an unknown order.
func (s *Store) PaymentOrderRef(ctx context.Context, orderID int64) (string, bool, error) {
	var ref string
	err := s.db.QueryRowContext(ctx, `SELECT booking_ref FROM payment_orders WHERE order_id = $1`, orderID).Scan(&ref)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return ref, true, nil
}

// RecordOrderStatus saves what the bank reported for an order and updates its booking.
func (s *Store) RecordOrderStatus(ctx context.Context, orderID int64, bankStatus string, final, paid bool, paidAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // does nothing after a successful Commit
	var wasPaid bool
	if err := tx.QueryRowContext(ctx,
		`SELECT paid FROM payment_orders WHERE order_id = $1 FOR UPDATE`, orderID).Scan(&wasPaid); err != nil {
		return err
	}
	var ref string
	err = tx.QueryRowContext(ctx,
		`UPDATE payment_orders
		 SET bank_status = $2, final = $3, paid = $4, checked_at = now(),
		     paid_at = CASE WHEN $4 THEN COALESCE(paid_at, $5) END
		 WHERE order_id = $1
		 RETURNING booking_ref`,
		orderID, bankStatus, final, paid, paidAt).Scan(&ref)
	if err != nil {
		return err
	}
	// A second payment for the same deposit (two bank orders, or a bank payment after
	// staff already marked it as received) is money the customer must get back.
	if paid && !wasPaid {
		var others int
		var method string
		if err := tx.QueryRowContext(ctx,
			`SELECT (SELECT count(*) FROM payment_orders WHERE booking_ref = $1 AND paid AND order_id <> $2), pay_method
			 FROM bookings WHERE ref = $1`, ref, orderID).Scan(&others, &method); err != nil {
			return err
		}
		if others > 0 || method == methodManual {
			log.Printf("DİQQƏT: %s rezervasiyasının depoziti ikinci dəfə ödənildi (bank sifarişi %d). Maliyyə yoxlayıb artıq məbləği geri qaytarmalıdır.", ref, orderID)
		}
	}
	if err := syncPayment(ctx, tx, ref); err != nil {
		return err
	}
	return tx.Commit()
}

// OpenPaymentOrders lists unfinished orders created after `since`, least recently checked first.
func (s *Store) OpenPaymentOrders(ctx context.Context, since time.Time, limit int) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT order_id FROM payment_orders
		 WHERE NOT final AND created_at > $1
		 ORDER BY checked_at NULLS FIRST, created_at
		 LIMIT $2`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// OrderToRecheck returns the booking's newest unfinished order created after `since`,
// unless it was already checked after `checkedAfter`.
func (s *Store) OrderToRecheck(ctx context.Context, ref string, since, checkedAfter time.Time) (int64, bool, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT order_id FROM payment_orders
		 WHERE booking_ref = $1 AND NOT final AND created_at > $2
		   AND (checked_at IS NULL OR checked_at < $3)
		 ORDER BY created_at DESC, order_id DESC LIMIT 1`, ref, since, checkedAfter).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// PaymentState returns a booking's deposit state, or false when the reference is unknown.
func (s *Store) PaymentState(ctx context.Context, ref string) (string, bool, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT payment_status FROM bookings WHERE ref = $1`, ref).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return status, true, nil
}

// manualResult is the outcome of SetManualPayment.
type manualResult int

const (
	manualNotFound      manualResult = iota
	manualDone                       // changed, or already in the requested state
	manualBankConfirmed              // undo refused: the bank itself confirmed the payment
)

// SetManualPayment lets staff mark a deposit as received outside the website (paid = true),
// or undo such a mark (paid = false). A payment confirmed by the bank cannot be undone here.
func (s *Store) SetManualPayment(ctx context.Context, ref string, paid bool) (manualResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return manualNotFound, err
	}
	defer tx.Rollback() // does nothing after a successful Commit
	var status, method string
	err = tx.QueryRowContext(ctx,
		`SELECT payment_status, pay_method FROM bookings WHERE ref = $1 FOR UPDATE`, ref).Scan(&status, &method)
	if errors.Is(err, sql.ErrNoRows) {
		return manualNotFound, nil
	}
	if err != nil {
		return manualNotFound, err
	}
	switch {
	case paid && status != payPaid:
		_, err = tx.ExecContext(ctx,
			`UPDATE bookings SET payment_status = 'paid', pay_method = $2, paid_at = now() WHERE ref = $1`,
			ref, methodManual)
	case !paid && method == methodBank:
		return manualBankConfirmed, nil
	case !paid && method == methodManual:
		if _, err = tx.ExecContext(ctx,
			`UPDATE bookings SET payment_status = 'pending', pay_method = '', paid_at = NULL WHERE ref = $1`, ref); err == nil {
			err = syncPayment(ctx, tx, ref)
		}
	}
	if err != nil {
		return manualNotFound, err
	}
	return manualDone, tx.Commit()
}

/* ---------- Visit logs ---------- */

// Visit log kinds.
const (
	kindVisit     = "visit"
	kindLoginOK   = "login_ok"
	kindLoginFail = "login_fail"
)

// VisitLog is one site visit or admin login attempt. The JSON names match the admin panel.
type VisitLog struct {
	ID         int64     `json:"id"`
	TS         time.Time `json:"ts"`
	Kind       string    `json:"kind"`
	Username   string    `json:"username"`
	IP         string    `json:"ip"`
	Provider   string    `json:"provider"`
	ASN        string    `json:"asn"`
	Country    string    `json:"country"`
	Hostname   string    `json:"hostname"`
	Path       string    `json:"path"`
	Referrer   string    `json:"referrer"`
	DeviceType string    `json:"deviceType"` // desktop | mobile | tablet | bot | unknown
	Device     string    `json:"device"`
	OS         string    `json:"os"`
	Browser    string    `json:"browser"`
	Screen     string    `json:"screen"`
	Language   string    `json:"language"`
	Timezone   string    `json:"timezone"`
	Hardware   string    `json:"hardware"`
	Connection string    `json:"connection"`
	UserAgent  string    `json:"userAgent"`
}

const visitColumns = `id, ts, kind, username, ip, provider, asn, country, hostname, path, referrer,
	device_type, device, os, browser, screen, language, timezone, hardware, connection, user_agent`

// AddVisit stores one log row.
func (s *Store) AddVisit(ctx context.Context, v VisitLog) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO visit_logs (ts, kind, username, ip, provider, asn, country, hostname, path, referrer,
		                         device_type, device, os, browser, screen, language, timezone, hardware, connection, user_agent)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)`,
		v.TS, v.Kind, v.Username, v.IP, v.Provider, v.ASN, v.Country, v.Hostname, v.Path, v.Referrer,
		v.DeviceType, v.Device, v.OS, v.Browser, v.Screen, v.Language, v.Timezone, v.Hardware, v.Connection, v.UserAgent)
	return err
}

// likePattern turns search text into an ILIKE pattern that matches it literally.
func likePattern(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// Visits returns one page of log rows, newest first, and the number of matching rows.
// kind "" means every kind; q searches several text columns, ignoring case.
func (s *Store) Visits(ctx context.Context, kind, q string, limit, offset int) ([]VisitLog, int, error) {
	pattern := ""
	if q != "" {
		pattern = likePattern(q)
	}
	const where = `WHERE ($1 = '' OR kind = $1)
	  AND ($2 = '' OR ip ILIKE $2 OR provider ILIKE $2 OR device ILIKE $2 OR os ILIKE $2
	       OR browser ILIKE $2 OR path ILIKE $2 OR username ILIKE $2)`
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM visit_logs `+where, kind, pattern).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+visitColumns+` FROM visit_logs `+where+` ORDER BY ts DESC, id DESC LIMIT $3 OFFSET $4`,
		kind, pattern, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []VisitLog{}
	for rows.Next() {
		var v VisitLog
		if err := rows.Scan(&v.ID, &v.TS, &v.Kind, &v.Username, &v.IP, &v.Provider, &v.ASN, &v.Country,
			&v.Hostname, &v.Path, &v.Referrer, &v.DeviceType, &v.Device, &v.OS, &v.Browser, &v.Screen,
			&v.Language, &v.Timezone, &v.Hardware, &v.Connection, &v.UserAgent); err != nil {
			return nil, 0, err
		}
		items = append(items, v)
	}
	return items, total, rows.Err()
}

// VisitStats are the counters above the log table.
type VisitStats struct {
	Today           int `json:"today"`           // site visits since midnight (Baku time)
	TodayUniqueIPs  int `json:"todayUniqueIPs"`  // distinct visitor IPs since midnight
	Week            int `json:"week"`            // site visits in the last 7 days
	FailedLogins24h int `json:"failedLogins24h"` // failed admin logins in the last 24 hours
}

// VisitStats counts visits since `today` (midnight) and in the 7 days and 24 hours before `now`.
func (s *Store) VisitStats(ctx context.Context, today, now time.Time) (VisitStats, error) {
	var st VisitStats
	weekAgo, dayAgo := now.AddDate(0, 0, -7), now.Add(-24*time.Hour)
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FILTER (WHERE kind = 'visit' AND ts >= $1),
		        count(DISTINCT ip) FILTER (WHERE kind = 'visit' AND ts >= $1),
		        count(*) FILTER (WHERE kind = 'visit' AND ts >= $2),
		        count(*) FILTER (WHERE kind = 'login_fail' AND ts >= $3)
		 FROM visit_logs WHERE ts >= LEAST($1, $2, $3)`,
		today, weekAgo, dayAgo).Scan(&st.Today, &st.TodayUniqueIPs, &st.Week, &st.FailedLogins24h)
	return st, err
}

// PruneVisits deletes log rows older than `before` and returns how many were deleted.
func (s *Store) PruneVisits(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM visit_logs WHERE ts < $1`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

/* ---------- Random helpers ---------- */

func randomInt(limit int64) int64 {
	n, err := rand.Int(rand.Reader, big.NewInt(limit))
	if err != nil {
		panic(err) // the system's random source is broken; nothing can continue safely
	}
	return n.Int64()
}

func randomHex(numBytes int) string {
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}
