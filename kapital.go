package main

// Kapital Bank e-commerce REST API client (hosted payment page).
//
// EVERYTHING this program assumes about the bank's API is in this file. Sources: the bank's
// documentation at https://pg.kapitalbank.az/docs, its Postman collection "Ecommerce API
// Documentation Simple", and five open-source clients that agree with them. Points the
// documentation leaves open are marked "ASSUMPTION"; confirm them with the bank's curator
// when the production terminal is issued.
//
//   - Base URL: production https://e-commerce.kapitalbank.az/api (KAPITAL_API_URL overrides it;
//     the bank's sandbox is https://txpgtst.kapitalbank.az/api).
//   - Every call uses HTTP Basic auth with the merchant login and password, and JSON bodies.
//     There is no request signing. Errors come back as non-2xx with
//     {"errorCode","errorDescription","errorDetails"} (the body may also be non-JSON).
//   - Create order: POST {base}/order with {"order":{typeRid, amount, currency, language,
//     title, description, hppRedirectUrl}}. typeRid "Order_SMS" is a purchase with immediate
//     capture. The amount is a string in manats ("10.00"), not qəpik. The answer is
//     {"order":{"id":123,"hppUrl":"https://…/flex","password":"…","status":"Preparing",…}}.
//     ASSUMPTION: typeRid values can be terminal-specific; Order_SMS is the documented purchase type.
//   - The customer is sent to <hppUrl>?id=<id>&password=<order password>. ASSUMPTION: the docs'
//     template says "{{hppUrl}}/flex?…" while hppUrl already ends in /flex, so "/flex" is
//     appended only when it is missing (what all reviewed clients do).
//   - After the payment page, the bank redirects the browser to hppRedirectUrl with ?ID=<id>&STATUS=<…>.
//     STATUS is unsigned and "can be temporary", so it is never trusted: the order is always
//     re-read with GET {base}/order/{id}. ASSUMPTION: parameter names are upper case; lower case is
//     accepted too. No server-to-server notification exists, hence the background reconciler.
//   - Order statuses and how they are treated: see classifyOrder below.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	kapitalProductionURL = "https://e-commerce.kapitalbank.az/api"
	kapitalOrderType     = "Order_SMS" // purchase, single message (immediate capture)
	kapitalCurrency      = "AZN"
	kapitalTimeLayout    = "2006-01-02 15:04:05" // createTime / finishTime, Baku time
	kapitalMaxResponse   = 1 << 20
)

// kapitalClient talks to the bank. Its password is never logged or returned in errors.
type kapitalClient struct {
	baseURL  string // without a trailing slash
	username string
	password string
	http     *http.Client
}

func newKapitalClient(baseURL, username, password string) *kapitalClient {
	return &kapitalClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		http:     &http.Client{Timeout: 20 * time.Second},
	}
}

// bankError is a refusal from the bank (non-2xx answer).
type bankError struct {
	HTTPStatus  int
	Code        string
	Description string
}

func (e *bankError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("Kapital Bank: HTTP %d", e.HTTPStatus)
	}
	return fmt.Sprintf("Kapital Bank: HTTP %d %s: %s", e.HTTPStatus, e.Code, e.Description)
}

// flexNumber accepts a JSON number or a numeric string ("amount": 10 or "10.00").
type flexNumber float64

func (n *flexNumber) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*n = flexNumber(f)
	return nil
}

// do sends one request and decodes a 2xx JSON answer into out.
func (k *kapitalClient) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, k.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.SetBasicAuth(k.username, k.password)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := k.http.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err // the URL holds nothing secret, but keep messages short
		}
		return fmt.Errorf("Kapital Bank: %s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, kapitalMaxResponse))
	if err != nil {
		return fmt.Errorf("Kapital Bank: read answer: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		e := &bankError{HTTPStatus: res.StatusCode}
		var payload struct {
			ErrorCode        string `json:"errorCode"`
			ErrorDescription string `json:"errorDescription"`
		}
		if json.Unmarshal(raw, &payload) == nil {
			e.Code, e.Description = payload.ErrorCode, payload.ErrorDescription
		}
		return e
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("Kapital Bank: unreadable answer (HTTP %d): %w", res.StatusCode, err)
	}
	return nil
}

// bankOrder is a newly created order. PaymentURL contains the order's own password:
// it goes to the customer's browser only, never to logs or the database.
type bankOrder struct {
	ID         int64
	Status     string
	PaymentURL string
}

// createOrder registers a purchase of `amount` manats and returns where to send the customer.
func (k *kapitalClient) createOrder(ctx context.Context, amount int, lang, description, redirectURL string) (bankOrder, error) {
	type orderIn struct {
		TypeRid        string `json:"typeRid"`
		Amount         string `json:"amount"`
		Currency       string `json:"currency"`
		Language       string `json:"language"`
		Title          string `json:"title"`
		Description    string `json:"description"`
		HppRedirectURL string `json:"hppRedirectUrl"`
	}
	var out struct {
		Order struct {
			ID       flexNumber `json:"id"`
			HppURL   string     `json:"hppUrl"`
			Password string     `json:"password"`
			Status   string     `json:"status"`
		} `json:"order"`
	}
	in := struct {
		Order orderIn `json:"order"`
	}{orderIn{
		TypeRid:        kapitalOrderType,
		Amount:         formatAmount(amount),
		Currency:       kapitalCurrency,
		Language:       bankLanguage(lang),
		Title:          "Aze-Tourism",
		Description:    description,
		HppRedirectURL: redirectURL,
	}}
	if err := k.do(ctx, http.MethodPost, "/order", in, &out); err != nil {
		return bankOrder{}, err
	}
	id := int64(out.Order.ID)
	if id <= 0 || out.Order.HppURL == "" || out.Order.Password == "" {
		return bankOrder{}, errors.New("Kapital Bank: order answer without id, hppUrl or password")
	}
	pageURL, err := hostedPageURL(out.Order.HppURL, id, out.Order.Password)
	if err != nil {
		return bankOrder{}, err
	}
	return bankOrder{ID: id, Status: out.Order.Status, PaymentURL: pageURL}, nil
}

// bankOrderState is what GET /order/{id} reports.
type bankOrderState struct {
	ID         int64
	Status     string
	Amount     float64
	Currency   string
	TypeRid    string
	FinishTime time.Time // zero when the bank gives none
}

// orderState asks the bank for an order's current state.
func (k *kapitalClient) orderState(ctx context.Context, id int64) (bankOrderState, error) {
	var out struct {
		Order struct {
			ID         flexNumber `json:"id"`
			Status     string     `json:"status"`
			Amount     flexNumber `json:"amount"`
			Currency   string     `json:"currency"`
			TypeRid    string     `json:"typeRid"`
			FinishTime string     `json:"finishTime"`
			Type       struct {
				Rid string `json:"rid"` // the detailed answer has type.rid instead of typeRid
			} `json:"type"`
		} `json:"order"`
	}
	if err := k.do(ctx, http.MethodGet, "/order/"+strconv.FormatInt(id, 10), nil, &out); err != nil {
		return bankOrderState{}, err
	}
	st := bankOrderState{
		ID:       int64(out.Order.ID),
		Status:   out.Order.Status,
		Amount:   float64(out.Order.Amount),
		Currency: out.Order.Currency,
		TypeRid:  out.Order.TypeRid,
	}
	if st.TypeRid == "" {
		st.TypeRid = out.Order.Type.Rid
	}
	if t, err := time.ParseInLocation(kapitalTimeLayout, out.Order.FinishTime, baku); err == nil {
		st.FinishTime = t
	}
	return st, nil
}

// formatAmount writes whole manats the way the bank's examples do: 10 -> "10.00".
func formatAmount(manats int) string {
	return strconv.Itoa(manats) + ".00"
}

// bankLanguage maps the site language to the payment page language.
func bankLanguage(lang string) string {
	switch lang {
	case "ru", "en":
		return lang
	}
	return "az"
}

// hostedPageURL builds the customer's payment page address from the create-order answer.
func hostedPageURL(hppURL string, id int64, orderPassword string) (string, error) {
	u, err := url.Parse(strings.TrimRight(hppURL, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New("Kapital Bank: invalid hppUrl in order answer")
	}
	if !strings.HasSuffix(u.Path, "/flex") {
		u.Path += "/flex"
	}
	u.RawQuery = "id=" + strconv.FormatInt(id, 10) + "&password=" + url.QueryEscape(orderPassword)
	return u.String(), nil
}

// returnOrderID reads the bank order id from the redirect back to our site (?ID=…&STATUS=…).
func returnOrderID(q url.Values) (int64, bool) {
	raw := q.Get("ID")
	if raw == "" {
		raw = q.Get("id")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return id, err == nil && id > 0
}

// orderOutcome is what an order's state means for the deposit.
type orderOutcome int

const (
	outcomeOpen   orderOutcome = iota // not finished yet: keep checking
	outcomePaid                       // the deposit was taken
	outcomeFailed                     // finished without payment
)

// classifyOrder decides what a bank order state means for a deposit of `deposit` manats.
// Status meanings (from the bank's list; which ones are final is inferred, the bank does not say):
//
//	paid:   FullyPaid, Closed (closed after payment)
//	failed: Cancelled, Rejected, Refused, Expired, Declined, Voided (reversed), Refunded,
//	        Funded (a transfer-only status that never applies to a purchase)
//	open:   Preparing (customer has not paid yet), Authorized (not expected for Order_SMS),
//	        PartPaid / PartiallyPaid (both spellings are seen in clients), and anything unknown.
//
// A paid status is accepted only if amount, currency and order type match what we created;
// a mismatch is logged and the order stays open so staff can look at it.
func classifyOrder(st bankOrderState, deposit int) orderOutcome {
	switch st.Status {
	case "FullyPaid", "Closed":
		amountOK := math.Abs(st.Amount-float64(deposit)) < 0.005
		currencyOK := strings.EqualFold(st.Currency, kapitalCurrency)
		typeOK := st.TypeRid == "" || st.TypeRid == kapitalOrderType // ASSUMPTION: an absent type is not a mismatch
		if amountOK && currencyOK && typeOK {
			return outcomePaid
		}
		log.Printf("Kapital Bank: sifariş %d %s statusundadır, amma məbləğ/valyuta/növ uyğun deyil (%.2f %s %s): əl ilə yoxlayın",
			st.ID, st.Status, st.Amount, st.Currency, st.TypeRid)
		return outcomeOpen
	case "Cancelled", "Rejected", "Refused", "Expired", "Declined", "Voided", "Refunded", "Funded":
		return outcomeFailed
	case "Preparing", "Authorized":
		return outcomeOpen
	case "PartPaid", "PartiallyPaid":
		log.Printf("Kapital Bank: sifariş %d qismən ödənilib (%s): əl ilə yoxlayın", st.ID, st.Status)
		return outcomeOpen
	}
	log.Printf("Kapital Bank: sifariş %d naməlum statusdadır (%q)", st.ID, st.Status)
	return outcomeOpen
}
