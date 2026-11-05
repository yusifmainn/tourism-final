package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	dateLayout     = "2006-01-02"
	maxTextLen     = 300
	maxFileName    = 200
	maxDocBytes    = 8 << 20  // 8 MB, same limit as the upload form
	maxBookingBody = 12 << 20 // the document arrives base64-encoded (about 4/3 of its size)
	maxContentBody = 1 << 20
)

// Azerbaijan is UTC+4 all year; a fixed zone avoids needing tzdata on the server.
var baku = time.FixedZone("Asia/Baku", 4*60*60)

var (
	bookingStatuses = []string{"Yeni", "Təsdiqləndi", "İmtina"}
	azMonths        = []string{"Yanvar", "Fevral", "Mart", "Aprel", "May", "İyun", "İyul", "Avqust", "Sentyabr", "Oktyabr", "Noyabr", "Dekabr"}
	finPattern      = regexp.MustCompile(`^[A-Z0-9]{7}$`)
	emailPattern    = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	digitsPattern   = regexp.MustCompile(`^[0-9]+$`)
	documentTypes   = []string{"image/jpeg", "image/png", "image/webp"}
)

// phoneRules mirrors the country list in assets/js/rezervasiya.js.
var phoneRules = map[string]struct {
	dial   string
	digits int
}{
	"AZ": {"+994", 9},
	"TR": {"+90", 10},
	"GE": {"+995", 9},
	"RU": {"+7", 10},
	"IR": {"+98", 10},
}

/* ---------- JSON helpers ---------- */

type apiError struct {
	Error string `json:"error"`
	Field string `json:"field,omitempty"` // the form field the error belongs to
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message, field string) {
	writeJSON(w, status, apiError{Error: message, Field: field})
}

func writeInternalError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		return // the visitor closed the page; nothing went wrong on our side
	}
	log.Printf("internal error: %v", err)
	writeError(w, http.StatusInternalServerError, "Server xətası. Bir az sonra yenidən yoxlayın.", "")
}

// readJSON decodes the body and answers the request itself when that fails.
func readJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "Sorğu JSON formatında olmalıdır.", "")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "Göndərilən məlumat çox böyükdür.", "idfile")
			return false
		}
		writeError(w, http.StatusBadRequest, "Sorğu oxuna bilmədi.", "")
		return false
	}
	return true
}

// cleanText trims a value and removes NUL characters, which PostgreSQL text cannot store.
func cleanText(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\x00", ""))
}

func pathValue(r *http.Request, name string) string {
	return strings.ReplaceAll(r.PathValue(name), "\x00", "")
}

/* ---------- Health check ---------- */

func (a *App) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		log.Printf("health check: %v", err)
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok"))
}

/* ---------- Public content ---------- */

func (a *App) getFAQ(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.FAQ(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *App) getTerms(w http.ResponseWriter, r *http.Request) {
	terms, err := a.store.Terms(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, terms)
}

func (a *App) getStats(w http.ResponseWriter, r *http.Request) {
	stats, err := a.store.Stats(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

/* ---------- Creating a booking ---------- */

// bookingRequest never holds the full card number or the CVV. While online payment is
// not connected the browser sends only the card's last 4 digits and expiry (MM/YY), which
// only the admin role can see; once Kapital Bank is connected the card is entered on the
// bank's page and these two fields are ignored.
type bookingRequest struct {
	Ad            string `json:"ad"`
	Soyad         string `json:"soyad"`
	Ata           string `json:"ata"`
	FIN           string `json:"fin"`
	Email         string `json:"email"`
	Tel           string `json:"tel"`
	Country       string `json:"country"`
	TourDate      string `json:"tourDate"`
	Unvan         string `json:"unvan"`
	Seher         string `json:"seher"`
	Postal        string `json:"postal"`
	TermsAccepted bool   `json:"termsAccepted"`
	IDImage       string `json:"idImage"` // data URL
	IDName        string `json:"idName"`
	Lang          string `json:"lang"` // site language, also used for the bank's payment page
	CardLast4     string `json:"cardLast4"`
	CardExp       string `json:"cardExp"` // MM/YY
}

// validate cleans the request. It returns the first bad field and a message,
// or empty strings when everything is valid. Field names match the form's input IDs.
func (req *bookingRequest) validate(now time.Time) (field, message string) {
	texts := []struct {
		field string
		value *string
	}{
		{"ad", &req.Ad}, {"soyad", &req.Soyad}, {"ata", &req.Ata}, {"fin", &req.FIN},
		{"email", &req.Email}, {"tel", &req.Tel}, {"tel", &req.Country}, {"tarix", &req.TourDate},
		{"unvan", &req.Unvan}, {"seher", &req.Seher}, {"postal", &req.Postal},
		{"cardNo", &req.CardLast4}, {"cardExp", &req.CardExp},
	}
	for _, t := range texts {
		*t.value = cleanText(*t.value)
		if len(*t.value) > maxTextLen {
			return t.field, "Mətn çox uzundur."
		}
	}
	req.FIN = strings.ToUpper(req.FIN)
	req.IDName = cleanText(req.IDName)
	if name := []rune(req.IDName); len(name) > maxFileName {
		req.IDName = string(name[:maxFileName]) // a long file name is no reason to refuse a booking
	}

	if req.Ad == "" {
		return "ad", "Ad tələb olunur."
	}
	if req.Soyad == "" {
		return "soyad", "Soyad tələb olunur."
	}
	if !finPattern.MatchString(req.FIN) {
		return "fin", "FİN 7 simvol olmalıdır."
	}
	if !emailPattern.MatchString(req.Email) {
		return "email", "E-poçt düzgün deyil."
	}
	rule, ok := phoneRules[req.Country]
	if !ok || len(req.Tel) != rule.digits || !digitsPattern.MatchString(req.Tel) {
		return "tel", "Telefon nömrəsi düzgün deyil."
	}
	tourDate, err := time.ParseInLocation(dateLayout, req.TourDate, baku)
	if err != nil {
		return "tarix", "Tur tarixi düzgün deyil."
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, baku)
	if tourDate.Before(today) {
		return "tarix", "Keçmiş tarix seçilə bilməz."
	}
	if !req.TermsAccepted {
		return "terms", "Şərtlər qəbul edilməlidir."
	}
	if req.Unvan == "" {
		return "unvan", "Ünvan tələb olunur."
	}
	if req.Seher == "" {
		return "seher", "Şəhər tələb olunur."
	}
	req.Lang = siteLanguage(req.Lang)
	return "", ""
}

// validateCard checks the card's last 4 digits and expiry, sent while online payment is not
// connected. Field names match the form's input IDs.
func (req *bookingRequest) validateCard(now time.Time) (field, message string) {
	if len(req.CardLast4) != 4 || !digitsPattern.MatchString(req.CardLast4) {
		return "cardNo", "Kart nömrəsi düzgün deyil."
	}
	expiry, err := time.Parse("01/06", req.CardExp)
	if err != nil {
		return "cardExp", "Bitmə tarixi düzgün deyil."
	}
	// A card is valid until the end of its expiry month.
	if !time.Date(expiry.Year(), expiry.Month()+1, 1, 0, 0, 0, 0, baku).After(now) {
		return "cardExp", "Kartın müddəti bitib."
	}
	return "", ""
}

// decodeDocument checks the uploaded ID photo by its actual bytes, not by what
// the browser claims, so only real images are stored and later shown to staff.
func decodeDocument(dataURL string) (Document, error) {
	_, encoded, ok := strings.Cut(dataURL, ";base64,")
	if !ok {
		return Document{}, errors.New("Şəxsiyyət vəsiqəsinin şəklini yükləyin.")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 {
		return Document{}, errors.New("Şəkil oxuna bilmədi.")
	}
	if len(data) > maxDocBytes {
		return Document{}, errors.New("Şəkil 8 MB-dan böyükdür.")
	}
	mime := http.DetectContentType(data)
	if !slices.Contains(documentTypes, mime) {
		return Document{}, errors.New("Yalnız JPG, PNG və ya WebP şəkil qəbul olunur.")
	}
	return Document{Mime: mime, Data: data}, nil
}

// tourRange formats the 2-day tour for the admin panel, e.g. "5–6 Noyabr 2026".
func tourRange(isoDate string) string {
	start, err := time.Parse(dateLayout, isoDate)
	if err != nil {
		return isoDate
	}
	end := start.AddDate(0, 0, 1)
	if start.Month() == end.Month() {
		return fmt.Sprintf("%d–%d %s %d", start.Day(), end.Day(), azMonths[end.Month()-1], end.Year())
	}
	return fmt.Sprintf("%d %s–%d %s %d", start.Day(), azMonths[start.Month()-1], end.Day(), azMonths[end.Month()-1], end.Year())
}

// bookingCreated is the answer to a new booking. PaymentURL is the bank's payment
// page; PaymentError means online payment is on but the bank could not be reached
// (the booking is kept and the customer can retry with POST /api/bookings/{ref}/pay).
type bookingCreated struct {
	Ref           string `json:"ref"`
	PaymentStatus string `json:"paymentStatus"`
	PaymentURL    string `json:"paymentUrl,omitempty"`
	PaymentError  bool   `json:"paymentError,omitempty"`
}

func (a *App) createBooking(w http.ResponseWriter, r *http.Request) {
	if !a.bookingLimit.allow(a.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "Çox sayda sorğu. Bir az sonra yenidən yoxlayın.", "")
		return
	}
	var req bookingRequest
	if !readJSON(w, r, maxBookingBody, &req) {
		return
	}
	now := time.Now().In(baku)
	if field, message := req.validate(now); field != "" {
		writeError(w, http.StatusBadRequest, message, field)
		return
	}
	if a.bank != nil {
		req.CardLast4, req.CardExp = "", "" // the card is entered on the bank's own page
	} else if field, message := req.validateCard(now); field != "" {
		writeError(w, http.StatusBadRequest, message, field)
		return
	}
	doc, err := decodeDocument(req.IDImage)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error(), "idfile")
		return
	}
	doc.Name = req.IDName

	ref, err := a.store.AddBooking(r.Context(), Booking{
		TS:        now,
		Ad:        req.Ad,
		Soyad:     req.Soyad,
		Ata:       req.Ata,
		FIN:       req.FIN,
		Email:     req.Email,
		Tel:       phoneRules[req.Country].dial + " " + req.Tel,
		Country:   req.Country,
		TourDate:  req.TourDate,
		Unvan:     req.Unvan,
		Seher:     req.Seher,
		Postal:    req.Postal,
		Mebleg:    depositAmount, // always the server's amount, never one sent by the browser
		Status:    bookingStatuses[0],
		CardLast4: req.CardLast4,
		CardExp:   req.CardExp,
	}, doc)
	if err != nil {
		writeInternalError(w, err)
		return
	}

	res := bookingCreated{Ref: ref, PaymentStatus: payPending}
	if a.bank != nil {
		paymentURL, err := a.startPayment(r.Context(), ref, req.Lang)
		if err != nil {
			log.Printf("payment %s: %v", ref, err)
			res.PaymentError = true
		} else {
			res.PaymentURL = paymentURL
		}
	}
	writeJSON(w, http.StatusCreated, res)
}

/* ---------- Admin: bookings ---------- */

type bookingSummary struct {
	Ref           string    `json:"ref"`
	Ad            string    `json:"ad"`
	Soyad         string    `json:"soyad"`
	Tarix         string    `json:"tarix"`
	Status        string    `json:"status"`
	TS            time.Time `json:"ts"`
	Mebleg        int       `json:"mebleg"`
	PaymentStatus string    `json:"paymentStatus,omitempty"` // admin and finance only
}

// seesPayments reports whether a role may see deposit payment details.
func seesPayments(role string) bool { return role == roleAdmin || role == roleFinance }

func (a *App) listBookings(w http.ResponseWriter, r *http.Request, s session) {
	all, err := a.store.Bookings(r.Context())
	if err != nil {
		writeInternalError(w, err)
		return
	}
	rows := make([]bookingSummary, 0, len(all))
	for _, b := range all {
		row := bookingSummary{
			Ref: b.Ref, Ad: b.Ad, Soyad: b.Soyad, Tarix: b.Tarix,
			Status: b.Status, TS: b.TS, Mebleg: b.Mebleg,
		}
		if seesPayments(s.Role) {
			row.PaymentStatus = b.PaymentStatus
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, rows)
}

type bookingDetail struct {
	Booking
	IDImage string    `json:"idImage,omitempty"` // URL of the ID document
	Card    *cardInfo `json:"card,omitempty"`    // admin role only
}

// cardInfo is all that is ever kept of a card: never the full number or the CVV.
type cardInfo struct {
	Last4 string `json:"last4"`
	Exp   string `json:"exp"` // MM/YY
}

// detailFor removes what the role may not see. This is the real access control;
// the admin panel only mirrors it.
func detailFor(b Booking, role string) bookingDetail {
	d := bookingDetail{Booking: b}
	if role == roleAdmin || role == roleOperator {
		if b.HasDocument {
			d.IDImage = "/api/admin/bookings/" + b.Ref + "/document"
		}
	} else {
		d.IDName = ""
	}
	if !seesPayments(role) {
		d.Payment = nil
	}
	// Card details are for the admin role only (not finance, not operator).
	if role == roleAdmin && b.CardLast4 != "" {
		d.Card = &cardInfo{Last4: b.CardLast4, Exp: b.CardExp}
	}
	return d
}

func (a *App) getBooking(w http.ResponseWriter, r *http.Request, s session) {
	b, ok, err := a.store.Booking(r.Context(), pathValue(r, "ref"))
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "Qeyd tapılmadı.", "")
		return
	}
	writeJSON(w, http.StatusOK, detailFor(b, s.Role))
}

func (a *App) getDocument(w http.ResponseWriter, r *http.Request, _ session) {
	doc, ok, err := a.store.Document(r.Context(), pathValue(r, "ref"))
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "Sənəd tapılmadı.", "")
		return
	}
	w.Header().Set("Content-Type", doc.Mime)
	w.Header().Set("Cache-Control", "no-store")
	if _, err := w.Write(doc.Data); err != nil {
		log.Printf("write document: %v", err)
	}
}

// updateBooking changes the booking status (any role) and/or the deposit state
// (admin and finance): "paid" records a deposit received outside the website,
// "pending" undoes such a mark. A payment confirmed by the bank cannot be undone.
func (a *App) updateBooking(w http.ResponseWriter, r *http.Request, s session) {
	var req struct {
		Status        *string `json:"status"`
		PaymentStatus *string `json:"paymentStatus"`
	}
	if !readJSON(w, r, 4<<10, &req) {
		return
	}
	if req.Status == nil && req.PaymentStatus == nil {
		writeError(w, http.StatusBadRequest, "Dəyişiklik göstərilməyib.", "")
		return
	}
	if req.Status != nil && !slices.Contains(bookingStatuses, *req.Status) {
		writeError(w, http.StatusBadRequest, "Status düzgün deyil.", "")
		return
	}
	if req.PaymentStatus != nil {
		if *req.PaymentStatus != payPaid && *req.PaymentStatus != payPending {
			writeError(w, http.StatusBadRequest, "Ödəniş statusu düzgün deyil.", "")
			return
		}
		if !seesPayments(s.Role) {
			writeError(w, http.StatusForbidden, "Bu əməliyyat üçün icazəniz yoxdur.", "")
			return
		}
	}

	ref := pathValue(r, "ref")
	if req.PaymentStatus != nil {
		result, err := a.store.SetManualPayment(r.Context(), ref, *req.PaymentStatus == payPaid)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		switch result {
		case manualNotFound:
			writeError(w, http.StatusNotFound, "Qeyd tapılmadı.", "")
			return
		case manualBankConfirmed:
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "Bu ödəniş Kapital Bank tərəfindən təsdiqlənib, ləğv edilə bilməz.",
				"code":  "bank",
			})
			return
		}
	}
	if req.Status != nil {
		found, err := a.store.SetBookingStatus(r.Context(), ref, *req.Status)
		if err != nil {
			writeInternalError(w, err)
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "Qeyd tapılmadı.", "")
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

/* ---------- Admin: FAQ and terms ---------- */

func cleanI18n(t I18n) I18n {
	return I18n{AZ: cleanText(t.AZ), RU: cleanText(t.RU), EN: cleanText(t.EN)}
}

func readFAQ(w http.ResponseWriter, r *http.Request, item *FAQ) bool {
	if !readJSON(w, r, maxContentBody, item) {
		return false
	}
	item.Q, item.A = cleanI18n(item.Q), cleanI18n(item.A)
	if item.Q.AZ == "" && item.Q.RU == "" && item.Q.EN == "" {
		writeError(w, http.StatusBadRequest, "Ən azı bir dildə sual daxil edin.", "")
		return false
	}
	return true
}

func (a *App) createFAQ(w http.ResponseWriter, r *http.Request, _ session) {
	var item FAQ
	if !readFAQ(w, r, &item) {
		return
	}
	item.ID = "f" + randomHex(8)
	if err := a.store.AddFAQ(r.Context(), item); err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *App) updateFAQ(w http.ResponseWriter, r *http.Request, _ session) {
	var item FAQ
	if !readFAQ(w, r, &item) {
		return
	}
	item.ID = pathValue(r, "id")
	found, err := a.store.UpdateFAQ(r.Context(), item)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "Sual tapılmadı.", "")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *App) deleteFAQ(w http.ResponseWriter, r *http.Request, _ session) {
	found, err := a.store.DeleteFAQ(r.Context(), pathValue(r, "id"))
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "Sual tapılmadı.", "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Limits for the trust strip; the page has room for short texts only.
const (
	maxStatValue = 20 // characters, e.g. "3 500+"
	maxStatLabel = 80
)

// putStats replaces the home page's trust-strip figures (PUT /api/admin/stats).
func (a *App) putStats(w http.ResponseWriter, r *http.Request, _ session) {
	var stats []Stat
	if !readJSON(w, r, maxContentBody, &stats) {
		return
	}
	if len(stats) != statCount {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%d göstərici göndərilməlidir.", statCount), "")
		return
	}
	for i := range stats {
		st := &stats[i]
		st.Value, st.Label = cleanText(st.Value), cleanI18n(st.Label)
		if utf8.RuneCountInString(st.Value) > maxStatValue {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("%d-ci göstəricinin rəqəmi çox uzundur (ən çox %d simvol).", i+1, maxStatValue), "")
			return
		}
		for _, label := range []string{st.Label.AZ, st.Label.RU, st.Label.EN} {
			if utf8.RuneCountInString(label) > maxStatLabel {
				writeError(w, http.StatusBadRequest,
					fmt.Sprintf("%d-ci göstəricinin mətni çox uzundur (ən çox %d simvol).", i+1, maxStatLabel), "")
				return
			}
		}
		if st.Value != "" && st.Label.AZ == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%d-ci göstəricinin AZ mətnini yazın.", i+1), "")
			return
		}
	}
	if err := a.store.SetStats(r.Context(), stats); err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (a *App) putTerms(w http.ResponseWriter, r *http.Request, _ session) {
	var terms I18n
	if !readJSON(w, r, maxContentBody, &terms) {
		return
	}
	terms = cleanI18n(terms)
	if err := a.store.SetTerms(r.Context(), terms); err != nil {
		writeInternalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, terms)
}
