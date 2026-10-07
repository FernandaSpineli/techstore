package cart

import (
	"errors"
	"net/http"

	"github.com/FernandaSpineli/techstore/internal/auth"
	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
	"github.com/FernandaSpineli/techstore/internal/platform/logging"
)

// Handler serves /api/v1/cart. Every route requires authentication.
type Handler struct {
	svc *Service
}

// NewHandler returns a Handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

var (
	errVariantNotFound = httpx.NewError(http.StatusNotFound, "VARIANT_NOT_FOUND", "Variant not found")
	errItemNotFound    = httpx.NewError(http.StatusNotFound, "CART_ITEM_NOT_FOUND", "This variant is not in the cart")
)

func toHTTPError(err error) error {
	var stockErr *InsufficientStockError
	switch {
	case errors.Is(err, ErrVariantNotFound):
		return errVariantNotFound
	case errors.Is(err, ErrItemNotFound):
		return errItemNotFound
	case errors.Is(err, ErrVariantUnavailable):
		return httpx.NewError(http.StatusConflict, "VARIANT_UNAVAILABLE", "This product is not for sale at the moment")
	case errors.Is(err, ErrQuantityLimit):
		var p httpx.Problems
		p.Add("quantity", "the cart holds at most 99 units of each variant")
		return p.Err()
	case errors.As(err, &stockErr):
		e := httpx.NewError(http.StatusConflict, "INSUFFICIENT_STOCK", "Not enough units in stock")
		e.Details = map[string]int{"requested": stockErr.Requested, "available": stockErr.Available}
		return e
	}
	return err
}

func userID(r *http.Request) string {
	p, _ := auth.PrincipalFrom(r.Context())
	return p.UserID
}

// Get handles GET /api/v1/cart.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) error {
	c, err := h.svc.Get(r.Context(), userID(r))
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, c)
	return nil
}

func checkQuantityField(p *httpx.Problems, q *int) {
	if q == nil || *q < 1 || *q > MaxQuantity {
		p.Add("quantity", "must be an integer between 1 and 99")
	}
}

type addRequest struct {
	VariantID string `json:"variant_id"`
	Quantity  *int   `json:"quantity"`
}

// AddItem handles POST /api/v1/cart/items. Adding a variant that is
// already in the cart increases its quantity.
func (h *Handler) AddItem(w http.ResponseWriter, r *http.Request) error {
	var req addRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	var p httpx.Problems
	if !httpx.IsUUID(req.VariantID) {
		p.Add("variant_id", "must be a variant ID")
	}
	checkQuantityField(&p, req.Quantity)
	if err := p.Err(); err != nil {
		return err
	}

	c, err := h.svc.Add(r.Context(), userID(r), req.VariantID, *req.Quantity)
	if err != nil {
		return toHTTPError(err)
	}
	logging.FromContext(r.Context()).InfoContext(r.Context(), "cart.item.added",
		"variant_id", req.VariantID, "quantity", *req.Quantity)
	httpx.WriteJSON(w, http.StatusOK, c)
	return nil
}

type updateRequest struct {
	Quantity *int `json:"quantity"`
}

// UpdateItem handles PATCH /api/v1/cart/items/{variantId}.
func (h *Handler) UpdateItem(w http.ResponseWriter, r *http.Request) error {
	variantID, err := httpx.PathUUID(r, "variantId", errItemNotFound)
	if err != nil {
		return err
	}
	var req updateRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	var p httpx.Problems
	checkQuantityField(&p, req.Quantity)
	if err := p.Err(); err != nil {
		return err
	}

	c, err := h.svc.SetQuantity(r.Context(), userID(r), variantID, *req.Quantity)
	if err != nil {
		return toHTTPError(err)
	}
	logging.FromContext(r.Context()).InfoContext(r.Context(), "cart.item.updated",
		"variant_id", variantID, "quantity", *req.Quantity)
	httpx.WriteJSON(w, http.StatusOK, c)
	return nil
}

// RemoveItem handles DELETE /api/v1/cart/items/{variantId}.
func (h *Handler) RemoveItem(w http.ResponseWriter, r *http.Request) error {
	variantID, err := httpx.PathUUID(r, "variantId", errItemNotFound)
	if err != nil {
		return err
	}
	c, err := h.svc.Remove(r.Context(), userID(r), variantID)
	if err != nil {
		return toHTTPError(err)
	}
	logging.FromContext(r.Context()).InfoContext(r.Context(), "cart.item.removed", "variant_id", variantID)
	httpx.WriteJSON(w, http.StatusOK, c)
	return nil
}

// Clear handles DELETE /api/v1/cart.
func (h *Handler) Clear(w http.ResponseWriter, r *http.Request) error {
	if err := h.svc.Clear(r.Context(), userID(r)); err != nil {
		return err
	}
	logging.FromContext(r.Context()).InfoContext(r.Context(), "cart.cleared")
	w.WriteHeader(http.StatusNoContent)
	return nil
}
