package order

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/FernandaSpineli/techstore/internal/auth"
	"github.com/FernandaSpineli/techstore/internal/inventory"
	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
)

// Handler exposes orders over HTTP.
type Handler struct {
	svc *Service
}

// NewHandler returns a Handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

var errNotFound = httpx.NewError(http.StatusNotFound, "ORDER_NOT_FOUND", "Order not found")

func toHTTPError(err error) error {
	var (
		unavailable *UnavailableItemsError
		shortage    *inventory.ShortageError
		transition  *TransitionError
	)
	switch {
	case errors.Is(err, ErrNotFound):
		return errNotFound
	case errors.Is(err, ErrCartEmpty):
		return httpx.NewError(http.StatusUnprocessableEntity, "CART_EMPTY", "Add items to the cart before placing an order")
	case errors.As(err, &unavailable):
		e := httpx.NewError(http.StatusConflict, "CART_HAS_UNAVAILABLE_ITEMS", "Some items in the cart are no longer for sale")
		e.Details = map[string][]string{"variant_ids": unavailable.VariantIDs}
		return e
	case errors.As(err, &shortage):
		e := httpx.NewError(http.StatusConflict, "INSUFFICIENT_STOCK", "Not enough units in stock for some items")
		e.Details = shortage.Shortages
		return e
	case errors.As(err, &transition):
		return httpx.NewError(http.StatusConflict, "INVALID_STATUS_TRANSITION",
			fmt.Sprintf("An order cannot move from %s to %s", transition.From, transition.To))
	}
	return err
}

func userID(r *http.Request) string {
	p, _ := auth.PrincipalFrom(r.Context())
	return p.UserID
}

// Create handles POST /api/v1/orders: it places an order for the cart.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) error {
	o, err := h.svc.Create(r.Context(), userID(r))
	if err != nil {
		return toHTTPError(err)
	}
	w.Header().Set("Location", "/api/v1/orders/"+o.ID)
	httpx.WriteJSON(w, http.StatusCreated, o)
	return nil
}

// List handles GET /api/v1/orders: the user's order history.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) error {
	return h.list(w, r, ListFilter{UserID: userID(r)})
}

// Get handles GET /api/v1/orders/{id}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", errNotFound)
	if err != nil {
		return err
	}
	o, err := h.svc.Get(r.Context(), userID(r), id)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, o)
	return nil
}

// Cancel handles POST /api/v1/orders/{id}/cancel.
func (h *Handler) Cancel(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", errNotFound)
	if err != nil {
		return err
	}
	o, err := h.svc.Cancel(r.Context(), userID(r), id)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, o)
	return nil
}

// AdminList handles GET /api/v1/admin/orders?status=&user_id=.
func (h *Handler) AdminList(w http.ResponseWriter, r *http.Request) error {
	f := ListFilter{UserID: r.URL.Query().Get("user_id"), Status: Status(r.URL.Query().Get("status"))}
	var p httpx.Problems
	if f.UserID != "" && !httpx.IsUUID(f.UserID) {
		p.Add("user_id", "must be a user ID")
	}
	if f.Status != "" && !f.Status.valid() {
		p.Add("status", "is not a known order status")
	}
	if err := p.Err(); err != nil {
		return err
	}
	return h.list(w, r, f)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, f ListFilter) error {
	var p httpx.Problems
	page := httpx.ParsePage(r, &p)
	if err := p.Err(); err != nil {
		return err
	}
	orders, total, err := h.svc.List(r.Context(), f, page)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, httpx.NewPaginated(orders, page, total))
	return nil
}

// AdminGet handles GET /api/v1/admin/orders/{id}.
func (h *Handler) AdminGet(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", errNotFound)
	if err != nil {
		return err
	}
	o, err := h.svc.AdminGet(r.Context(), id)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, o)
	return nil
}

type setStatusRequest struct {
	Status Status `json:"status"`
	Reason string `json:"reason"`
}

// AdminSetStatus handles PATCH /api/v1/admin/orders/{id}/status.
func (h *Handler) AdminSetStatus(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", errNotFound)
	if err != nil {
		return err
	}
	var req setStatusRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	req.Reason = strings.TrimSpace(req.Reason)
	var p httpx.Problems
	if !adminTargets[req.Status] {
		p.Add("status", "must be one of shipped, delivered, cancelled")
	}
	if utf8.RuneCountInString(req.Reason) > 200 {
		p.Add("reason", "must be at most 200 characters")
	}
	if err := p.Err(); err != nil {
		return err
	}
	if req.Reason == "" {
		req.Reason = "set by admin"
	}

	o, err := h.svc.AdminSetStatus(r.Context(), id, req.Status, req.Reason)
	if err != nil {
		return toHTTPError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, o)
	return nil
}
