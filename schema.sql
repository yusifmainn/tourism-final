-- Aze-Tourism database schema (PostgreSQL 14+).
-- The server runs this file on every start, so tables are created automatically.
-- IF NOT EXISTS makes repeated runs safe. Apart from the one-time upgrade of the bookings
-- table below (old placeholder payment columns), it never changes or deletes existing data.

CREATE TABLE IF NOT EXISTS users (
    username      TEXT PRIMARY KEY,
    role          TEXT NOT NULL CHECK (role IN ('admin', 'finance', 'operator')),
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Only a hash of the session token is stored, so a database leak does not expose live logins.
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

-- Always exactly one row (id = 1).
CREATE TABLE IF NOT EXISTS terms (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    az TEXT NOT NULL DEFAULT '',
    ru TEXT NOT NULL DEFAULT '',
    en TEXT NOT NULL DEFAULT ''
);

-- The full card number and the CVV never reach this server. card_tail (last 4 digits) and
-- card_expiry (MM/YY) are kept only while online payment is not connected, and only the admin
-- role sees them.
-- mebleg is the deposit in AZN. payment_status is derived from payment_orders (or a staff mark):
-- pay_method is 'Kapital Bank' (confirmed by the bank), 'Əl ilə' (marked by staff) or ''.
CREATE TABLE IF NOT EXISTS bookings (
    ref            TEXT PRIMARY KEY,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    ad             TEXT NOT NULL,
    soyad          TEXT NOT NULL,
    ata            TEXT NOT NULL DEFAULT '',
    fin            TEXT NOT NULL,
    email          TEXT NOT NULL,
    tel            TEXT NOT NULL,
    country        TEXT NOT NULL,
    tour_date      DATE NOT NULL,
    unvan          TEXT NOT NULL,
    seher          TEXT NOT NULL,
    postal         TEXT NOT NULL DEFAULT '',
    mebleg         INTEGER NOT NULL,
    status         TEXT NOT NULL DEFAULT 'Yeni' CHECK (status IN ('Yeni', 'Təsdiqləndi', 'İmtina')),
    payment_status TEXT NOT NULL DEFAULT 'pending' CHECK (payment_status IN ('pending', 'paid', 'failed')),
    pay_method     TEXT NOT NULL DEFAULT '',
    paid_at        TIMESTAMPTZ,
    card_tail      TEXT NOT NULL DEFAULT '',
    card_expiry    TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS bookings_created_at_idx ON bookings (created_at DESC);

-- Upgrade of a database created by the first version of this file. Its card_* / txn columns and
-- payment fields only ever held placeholder values (no money was taken), so they are cleared once,
-- the old price in mebleg becomes the 10 AZN deposit, and the card columns are dropped.
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS payment_status TEXT NOT NULL DEFAULT 'pending'
    CHECK (payment_status IN ('pending', 'paid', 'failed'));
ALTER TABLE bookings ALTER COLUMN pay_method SET DEFAULT '';
ALTER TABLE bookings ALTER COLUMN paid_at DROP NOT NULL;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = current_schema() AND table_name = 'bookings' AND column_name = 'card_last4') THEN
        UPDATE bookings SET payment_status = 'pending', pay_method = '', paid_at = NULL, mebleg = 10;
    END IF;
END $$;
ALTER TABLE bookings
    DROP COLUMN IF EXISTS card_name,
    DROP COLUMN IF EXISTS card_last4,
    DROP COLUMN IF EXISTS card_exp,
    DROP COLUMN IF EXISTS txn;

-- Last 4 digits and expiry of the card (see the bookings comment). These names must stay different
-- from card_last4 / card_exp: the upgrade above treats those as the old placeholder columns,
-- resets every booking's deposit and drops them.
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS card_tail TEXT NOT NULL DEFAULT '';
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS card_expiry TEXT NOT NULL DEFAULT '';

-- One row per Kapital Bank order. A booking can have several (the customer may retry);
-- it counts as paid when any of its orders is paid. Neither the merchant password nor
-- the per-order payment page password is stored.
CREATE TABLE IF NOT EXISTS payment_orders (
    order_id    BIGINT PRIMARY KEY,
    booking_ref TEXT NOT NULL REFERENCES bookings (ref) ON DELETE CASCADE,
    amount      NUMERIC(10, 2) NOT NULL,
    currency    TEXT NOT NULL DEFAULT 'AZN',
    bank_status TEXT NOT NULL DEFAULT '',
    final       BOOLEAN NOT NULL DEFAULT false,
    paid        BOOLEAN NOT NULL DEFAULT false,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    checked_at  TIMESTAMPTZ,
    paid_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS payment_orders_booking_ref_idx ON payment_orders (booking_ref, created_at DESC);
CREATE INDEX IF NOT EXISTS payment_orders_open_idx ON payment_orders (created_at) WHERE NOT final;

-- Site visits and admin panel logins ("Giriş loqları", visible to the admin role only).
-- Rows older than 180 days are deleted by the server at start-up and once a day.
CREATE TABLE IF NOT EXISTS visit_logs (
    id          BIGSERIAL PRIMARY KEY,
    ts          TIMESTAMPTZ NOT NULL DEFAULT now(),
    kind        TEXT NOT NULL CHECK (kind IN ('visit', 'login_ok', 'login_fail')),
    username    TEXT NOT NULL DEFAULT '',
    ip          TEXT NOT NULL DEFAULT '',
    provider    TEXT NOT NULL DEFAULT '',
    asn         TEXT NOT NULL DEFAULT '',
    country     TEXT NOT NULL DEFAULT '',
    hostname    TEXT NOT NULL DEFAULT '',
    path        TEXT NOT NULL DEFAULT '',
    referrer    TEXT NOT NULL DEFAULT '',
    device_type TEXT NOT NULL DEFAULT 'unknown',
    device      TEXT NOT NULL DEFAULT '',
    os          TEXT NOT NULL DEFAULT '',
    browser     TEXT NOT NULL DEFAULT '',
    screen      TEXT NOT NULL DEFAULT '',
    language    TEXT NOT NULL DEFAULT '',
    timezone    TEXT NOT NULL DEFAULT '',
    hardware    TEXT NOT NULL DEFAULT '',
    connection  TEXT NOT NULL DEFAULT '',
    user_agent  TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS visit_logs_ts_idx ON visit_logs (ts DESC);
CREATE INDEX IF NOT EXISTS visit_logs_kind_ts_idx ON visit_logs (kind, ts DESC);

-- ID card photos, kept apart so listing bookings never touches these large values.
CREATE TABLE IF NOT EXISTS booking_documents (
    booking_ref TEXT PRIMARY KEY REFERENCES bookings (ref) ON DELETE CASCADE,
    file_name   TEXT NOT NULL DEFAULT '',
    mime_type   TEXT NOT NULL,
    data        BYTEA NOT NULL
);

-- The four figures of the home page's trust strip (for example "12+ illik təcrübə"), edited in the
-- admin panel. id is the position 1-4 on the page; an empty value hides that position.
CREATE TABLE IF NOT EXISTS site_stats (
    id       SMALLINT PRIMARY KEY CHECK (id BETWEEN 1 AND 4),
    value    TEXT NOT NULL DEFAULT '',
    label_az TEXT NOT NULL DEFAULT '',
    label_ru TEXT NOT NULL DEFAULT '',
    label_en TEXT NOT NULL DEFAULT ''
);
