package main

// The 10 AZN booking deposit, paid on Kapital Bank's hosted payment page.
// Flow: the booking is saved (pending) -> a bank order is created -> the customer pays on
// the bank's page -> the bank sends the browser to /odenis/netice -> the server re-reads the
// order from the bank API and marks the booking paid or failed. A background job re-checks
// unfinished orders, because a customer may close the tab before coming back.
// Without bank credentials online payment is off: bookings are still saved and staff mark
// the deposit as received in the admin panel. Bank API details are in kapital.go.

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"
)

const (
	depositAmount   = 10 // AZN, the only amount paid online
	paymentReturn   = "/odenis/netice"
	reconcileEvery  = 5 * time.Minute
	reconcileWindow = 48 * time.Hour   // orders older than this are no longer checked
	recheckInterval = 10 * time.Second // status page: ask the bank at most this often per order
)

// depositDescriptions is the order text shown on the bank's page, per language.
var depositDescriptions = map[string]string{
	"az": "Rezervasiya depoziti",
	"ru": "Депозит за бронирование",
	"en": "Booking deposit",
}

// siteLanguage keeps "az", "ru" or "en"; anything else becomes "az".
func siteLanguage(lang string) string {
	if _, ok := depositDescriptions[lang]; ok {
		return lang
	}
	return "az"
}

func (a *App) getConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"deposit":       depositAmount,
		"currency":      kapitalCurrency,
		"onlinePayment": a.bank != nil,
	})
}

// startPayment creates a bank order for the booking and returns the customer's
// payment page URL. It keeps going if the visitor disconnects meanwhile, so an order
// created at the bank is always recorded.
func (a *App) startPayment(ctx context.Context, ref, lang string) (string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	lang = siteLanguage(lang)
	order, err := a.bank.createOrder(ctx, depositAmount, lang,
		depositDescriptions[lang]+" "+ref, a.cfg.PublicURL+paymentReturn)
	if err != nil {
		return "", err
	}
	if err := a.store.AddPaymentOrder(ctx, ref, order.ID, depositAmount, kapitalCurrency, order.Status); err != nil {
		return "", fmt.Errorf("save bank order %d: %w", order.ID, err)
	}
	return order.PaymentURL, nil
}

// refreshOrder asks the bank for an order's state and saves what it means for the booking.
func (a *App) refreshOrder(ctx context.Context, orderID int64) error {
	st, err := a.bank.orderState(ctx, orderID)
	if err != nil {
		return err
	}
	st.ID = orderID
	outcome := classifyOrder(st, depositAmount)
	paidAt := st.FinishTime
	if paidAt.IsZero() {
		paidAt = time.Now()
	}
	status := logText(st.Status, 64)
	return a.store.RecordOrderStatus(ctx, orderID, status, outcome != outcomeOpen, outcome == outcomePaid, paidAt)
}

/* ---------- Public endpoints ---------- */

// payBooking starts a new payment attempt for an existing booking (POST /api/bookings/{ref}/pay).
func (a *App) payBooking(w http.ResponseWriter, r *http.Request) {
	if !a.payLimit.allow(a.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "Çox sayda sorğu. Bir az sonra yenidən yoxlayın.", "")
		return
	}
	var req struct {
		Lang string `json:"lang"`
	}
	if r.ContentLength != 0 && !readJSON(w, r, 4<<10, &req) {
		return
	}
	ref := pathValue(r, "ref")
	status, ok, err := a.store.PaymentState(r.Context(), ref)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "Rezervasiya tapılmadı.", "")
		return
	}
	if status == payPaid {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Depozit artıq ödənilib.", "code": "paid"})
		return
	}
	if a.bank == nil {
		writeError(w, http.StatusServiceUnavailable,
			"Onlayn ödəniş hazırda mümkün deyil. Əməkdaşlarımız depozit üçün sizinlə əlaqə saxlayacaq.", "")
		return
	}
	// The previous attempt may have been paid even though the booking still says pending
	// (the return check failed or the bank was slow). Ask the bank about the newest open
	// order first, ignoring recheckInterval, so a customer who paid is not charged twice.
	now := time.Now()
	openID, open, err := a.store.OrderToRecheck(r.Context(), ref, now.Add(-reconcileWindow), now)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if open {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		refreshErr := a.refreshOrder(ctx, openID)
		cancel()
		if refreshErr != nil {
			log.Printf("payment %s: check of order %d before retry: %v", ref, openID, refreshErr)
		}
		if status, _, err = a.store.PaymentState(r.Context(), ref); err != nil {
			writeInternalError(w, err)
			return
		}
		if status == payPaid {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Depozit artıq ödənilib.", "code": "paid"})
			return
		}
	}
	paymentURL, err := a.startPayment(r.Context(), ref, req.Lang)
	if err != nil {
		log.Printf("payment %s: %v", ref, err)
		writeError(w, http.StatusBadGateway, "Bankla əlaqə alınmadı. Bir az sonra yenidən yoxlayın.", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"paymentUrl": paymentURL})
}

// paymentInfo tells the booking page whether the deposit is paid (GET /api/bookings/{ref}/payment).
// It contains nothing personal. While the deposit is pending it asks the bank again, at most
// once per recheckInterval, so "check again" on the page gives a fresh answer.
func (a *App) paymentInfo(w http.ResponseWriter, r *http.Request) {
	if !a.statusLimit.allow(a.clientIP(r)) {
		writeError(w, http.StatusTooManyRequests, "Çox sayda sorğu. Bir az sonra yenidən yoxlayın.", "")
		return
	}
	ref := pathValue(r, "ref")
	status, ok, err := a.store.PaymentState(r.Context(), ref)
	if err != nil {
		writeInternalError(w, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "Rezervasiya tapılmadı.", "")
		return
	}
	if status == payPending && a.bank != nil {
		now := time.Now()
		id, found, err := a.store.OrderToRecheck(r.Context(), ref, now.Add(-reconcileWindow), now.Add(-recheckInterval))
		if err == nil && found {
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			err = a.refreshOrder(ctx, id)
			cancel()
		}
		if err != nil {
			log.Printf("payment status %s: %v", ref, err)
		}
		if status, _, err = a.store.PaymentState(r.Context(), ref); err != nil {
			writeInternalError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ref":           ref,
		"paymentStatus": status,
		"deposit":       depositAmount,
		"onlinePayment": a.bank != nil,
	})
}

// paymentReturned handles the bank sending the customer back (GET /odenis/netice?ID=…&STATUS=…).
// STATUS is ignored: the order is re-read from the bank before anything is saved.
func (a *App) paymentReturned(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	failed := func() { http.Redirect(w, r, "/rezervasiya/?odenis=xeta", http.StatusSeeOther) }
	orderID, ok := returnOrderID(r.URL.Query())
	if !ok {
		failed()
		return
	}
	ref, found, err := a.store.PaymentOrderRef(r.Context(), orderID)
	if err != nil {
		log.Printf("payment return %d: %v", orderID, err)
		failed()
		return
	}
	if !found {
		failed()
		return
	}
	if a.bank != nil && a.returnLimit.allow(a.clientIP(r)) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
		if err := a.refreshOrder(ctx, orderID); err != nil {
			log.Printf("payment return %d (%s): %v", orderID, ref, err) // the reconciler tries again
		}
		cancel()
	}
	http.Redirect(w, r, "/rezervasiya/?ref="+url.QueryEscape(ref), http.StatusSeeOther)
}

/* ---------- Background reconciliation ---------- */

// reconcilePayments re-checks every unfinished order of the last 48 hours once.
func (a *App) reconcilePayments(ctx context.Context) {
	ids, err := a.store.OpenPaymentOrders(ctx, time.Now().Add(-reconcileWindow), 500)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("payment reconcile: %v", err)
		}
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		orderCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		if err := a.refreshOrder(orderCtx, id); err != nil && ctx.Err() == nil {
			log.Printf("payment reconcile %d: %v", id, err)
		}
		cancel()
	}
}

// runReconciler checks open orders at start-up and then every 5 minutes until ctx ends.
func (a *App) runReconciler(ctx context.Context) {
	ticker := time.NewTicker(reconcileEvery)
	defer ticker.Stop()
	for {
		a.reconcilePayments(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
