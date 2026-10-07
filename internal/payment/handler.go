package payment

import (
	"errors"
	"io"
	"net/http"

	"github.com/FernandaSpineli/techstore/internal/auth"
	"github.com/FernandaSpineli/techstore/internal/order"
	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
)

// Handler exposes checkout and the Stripe webhook over HTTP.
type Handler struct {
	svc *Service
}

// NewHandler returns a Handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

func toHTTPError(err error) error {
	switch {
	case errors.Is(err, ErrDisabled):
		return httpx.NewError(http.StatusServiceUnavailable, "PAYMENTS_UNAVAILABLE", "Payments are not available at the moment")
	case errors.Is(err, order.ErrNotFound):
		return httpx.NewError(http.StatusNotFound, "ORDER_NOT_FOUND", "Order not found")
	case errors.Is(err, ErrOrderNotPayable):
		return httpx.NewError(http.StatusConflict, "ORDER_NOT_PAYABLE", "This order is not awaiting payment")
	case errors.Is(err, ErrCheckoutConflict):
		return httpx.NewError(http.StatusConflict, "CHECKOUT_IN_PROGRESS", "A checkout for this order is being created; retry shortly")
	case errors.Is(err, ErrProvider):
		// The details are logged; the client only learns it can retry.
		return httpx.NewError(http.StatusBadGateway, "PAYMENT_PROVIDER_ERROR", "The payment provider could not be reached; try again")
	case errors.Is(err, ErrInvalidSignature):
		return httpx.NewError(http.StatusBadRequest, "INVALID_SIGNATURE", "Webhook signature verification failed")
	}
	return err
}

// Checkout handles POST /api/v1/orders/{id}/checkout. It answers 201 with
// a new session or 200 with the one already open.
func (h *Handler) Checkout(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", toHTTPError(order.ErrNotFound))
	if err != nil {
		return err
	}
	p, _ := auth.PrincipalFrom(r.Context())
	c, created, err := h.svc.StartCheckout(r.Context(), p.UserID, id)
	if err != nil {
		return toHTTPError(err)
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.WriteJSON(w, status, c)
	return nil
}

// maxWebhookBytes is far above any Checkout Session event; Stripe caps
// payloads well below it.
const maxWebhookBytes = 256 << 10

// Webhook handles POST /api/v1/payments/webhook. It is public: the Stripe
// signature, not a session, authenticates the caller.
func (h *Handler) Webhook(w http.ResponseWriter, r *http.Request) error {
	// The signature covers the exact bytes, so the body is read raw.
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBytes))
	if err != nil {
		return httpx.NewError(http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "Request body is too large")
	}
	if err := h.svc.HandleWebhook(r.Context(), payload, r.Header.Get("Stripe-Signature")); err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"received": true})
	return nil
}
