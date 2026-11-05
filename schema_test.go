package main

import (
	"context"
	"testing"
	"time"
)

// firstSchemaSQL is the bookings part of the first schema.sql, whose payment columns
// (card_name, card_last4, card_exp, txn, NOT NULL paid_at) only ever held placeholder data.
const firstSchemaSQL = `
CREATE TABLE IF NOT EXISTS users (
    username      TEXT PRIMARY KEY,
    role          TEXT NOT NULL CHECK (role IN ('admin', 'finance', 'operator')),
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    username   TEXT NOT NULL REFERENCES users (username) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions (expires_at);
CREATE TABLE IF NOT EXISTS faq (
    id         TEXT PRIMARY KEY,
    sort_order BIGSERIAL,
    q_az       TEXT NOT NULL DEFAULT '',
    q_ru       TEXT NOT NULL DEFAULT '',
    q_en       TEXT NOT NULL DEFAULT '',
    a_az       TEXT NOT NULL DEFAULT '',
    a_ru       TEXT NOT NULL DEFAULT '',
    a_en       TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS terms (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    az TEXT NOT NULL DEFAULT '',
    ru TEXT NOT NULL DEFAULT '',
    en TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS bookings (
    ref        TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ad         TEXT NOT NULL,
    soyad      TEXT NOT NULL,
    ata        TEXT NOT NULL DEFAULT '',
    fin        TEXT NOT NULL,
    email      TEXT NOT NULL,
    tel        TEXT NOT NULL,
    country    TEXT NOT NULL,
    tour_date  DATE NOT NULL,
    unvan      TEXT NOT NULL,
    seher      TEXT NOT NULL,
    postal     TEXT NOT NULL DEFAULT '',
    mebleg     INTEGER NOT NULL,
    status     TEXT NOT NULL DEFAULT 'Yeni' CHECK (status IN ('Yeni', 'Təsdiqləndi', 'İmtina')),
    pay_method TEXT NOT NULL,
    card_name  TEXT NOT NULL,
    card_last4 TEXT NOT NULL,
    card_exp   TEXT NOT NULL,
    txn        TEXT NOT NULL,
    paid_at    TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS bookings_created_at_idx ON bookings (created_at DESC);
CREATE TABLE IF NOT EXISTS booking_documents (
    booking_ref TEXT PRIMARY KEY REFERENCES bookings (ref) ON DELETE CASCADE,
    file_name   TEXT NOT NULL DEFAULT '',
    mime_type   TEXT NOT NULL,
    data        BYTEA NOT NULL
);`

// schemaFacts reads what the tests check about the bookings table.
func schemaFacts(t *testing.T, store *Store) (cardColumns, checks int) {
	t.Helper()
	ctx := context.Background()
	err := store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.columns
		 WHERE table_schema = current_schema() AND table_name = 'bookings'
		   AND column_name IN ('card_name', 'card_last4', 'card_exp', 'txn')`).Scan(&cardColumns)
	if err != nil {
		t.Fatal(err)
	}
	err = store.db.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_constraint WHERE conrelid = 'bookings'::regclass AND contype = 'c'`).Scan(&checks)
	if err != nil {
		t.Fatal(err)
	}
	return cardColumns, checks
}

func TestSchemaUpgradeFromFirstVersion(t *testing.T) {
	store := newTestStore(t, false)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, firstSchemaSQL); err != nil {
		t.Fatal(err)
	}
	// A booking as the first version stored it, with placeholder payment values.
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO bookings (ref, ad, soyad, fin, email, tel, country, tour_date, unvan, seher, mebleg,
		                       pay_method, card_name, card_last4, card_exp, txn, paid_at)
		 VALUES ('AZT-111111-2026', 'Old', 'Booking', 'ABC1234', 'old@example.com', '+994 501234567', 'AZ',
		         '2026-12-01', 'Nizami 1', 'Bakı', 390, 'placeholder', 'OLD USER', '4242', '12/30', 'X-1', now());
		 INSERT INTO booking_documents (booking_ref, file_name, mime_type, data)
		 VALUES ('AZT-111111-2026', 'id.jpg', 'image/jpeg', '\xffd8ff'::bytea);
		 INSERT INTO terms (id, az) VALUES (1, 'Köhnə şərtlər');`); err != nil {
		t.Fatal(err)
	}

	for range 3 { // the server runs the schema on every start
		if err := store.migrate(ctx, schemaSQL, seedJSON); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	if cards, checks := schemaFacts(t, store); cards != 0 || checks != 2 {
		t.Errorf("after upgrade: %d card columns, %d CHECK constraints (want 0 and 2)", cards, checks)
	}

	b, ok, err := store.Booking(ctx, "AZT-111111-2026")
	if err != nil || !ok {
		t.Fatalf("old booking: %v %v", ok, err)
	}
	p := b.Payment
	if b.Mebleg != depositAmount || b.PaymentStatus != payPending || p.Method != "" || p.PaidAt != nil || p.BankOrderID != "" ||
		!b.HasDocument || b.Ad != "Old" {
		t.Errorf("old booking after upgrade: %+v payment %+v", b, p)
	}
	if terms, err := store.Terms(ctx); err != nil || terms.AZ != "Köhnə şərtlər" {
		t.Errorf("existing terms were changed: %q %v", terms.AZ, err)
	}

	// The upgraded tables work like fresh ones.
	if err := store.AddPaymentOrder(ctx, "AZT-111111-2026", 77, depositAmount, "AZN", "Preparing"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordOrderStatus(ctx, 77, "FullyPaid", true, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := store.PaymentState(ctx, "AZT-111111-2026"); state != payPaid {
		t.Errorf("payment after upgrade: %q", state)
	}
	if _, err := store.AddBooking(ctx, Booking{TS: time.Now(), Ad: "New", Soyad: "B", FIN: "ABC1234", Email: "n@example.com",
		Tel: "+994 501234567", Country: "AZ", TourDate: "2026-12-02", Unvan: "x", Seher: "Bakı", Mebleg: depositAmount, Status: "Yeni"},
		Document{Name: "id.jpg", Mime: "image/jpeg", Data: []byte{0xFF, 0xD8, 0xFF}}); err != nil {
		t.Errorf("new booking after upgrade: %v", err)
	}
	if err := store.AddVisit(ctx, VisitLog{TS: time.Now(), Kind: kindVisit, DeviceType: deviceUnknown}); err != nil {
		t.Errorf("visit log after upgrade: %v", err)
	}
}

func TestSchemaFreshAndRepeatable(t *testing.T) {
	store := newTestStore(t, true)
	if err := store.migrate(context.Background(), schemaSQL, seedJSON); err != nil {
		t.Fatal(err)
	}
	if cards, checks := schemaFacts(t, store); cards != 0 || checks != 2 {
		t.Errorf("fresh schema: %d card columns, %d CHECK constraints (want 0 and 2)", cards, checks)
	}
}
