// Package inventory tracks stock per product variant.
//
// available = on_hand - reserved. Units are reserved while an order awaits
// payment, then either committed (removed from on_hand) or released. A
// variant without an inventory row has no stock.
package inventory

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FernandaSpineli/techstore/internal/platform/httpx"
	"github.com/FernandaSpineli/techstore/internal/platform/logging"
	"github.com/FernandaSpineli/techstore/internal/platform/postgres"
)

// Level is the stock of one variant.
type Level struct {
	VariantID string `json:"variant_id"`
	OnHand    int    `json:"on_hand"`
	Reserved  int    `json:"reserved"`
	Available int    `json:"available"`
}

var (
	ErrVariantNotFound = errors.New("inventory: variant not found")
	ErrBelowReserved   = errors.New("inventory: on_hand below reserved units")
)

// Store runs the inventory SQL.
type Store struct {
	db *pgxpool.Pool
}

// NewStore returns a Store.
func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

// Level returns the stock of a variant.
func (s *Store) Level(ctx context.Context, variantID string) (Level, error) {
	l := Level{VariantID: variantID}
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(i.on_hand, 0), COALESCE(i.reserved, 0)
		FROM product_variants v LEFT JOIN inventory i ON i.variant_id = v.id
		WHERE v.id = $1`, variantID).Scan(&l.OnHand, &l.Reserved)
	if errors.Is(err, pgx.ErrNoRows) {
		return Level{}, ErrVariantNotFound
	}
	if err != nil {
		return Level{}, fmt.Errorf("inventory: level: %w", err)
	}
	l.Available = l.OnHand - l.Reserved
	return l, nil
}

// SetOnHand records a stock count, e.g. after a delivery or a stocktake. It
// cannot go below the units reserved by pending orders.
func (s *Store) SetOnHand(ctx context.Context, variantID string, onHand int) (Level, error) {
	l := Level{VariantID: variantID}
	err := s.db.QueryRow(ctx, `
		INSERT INTO inventory (variant_id, on_hand) VALUES ($1, $2)
		ON CONFLICT (variant_id) DO UPDATE SET on_hand = EXCLUDED.on_hand
		RETURNING on_hand, reserved`, variantID, onHand).Scan(&l.OnHand, &l.Reserved)
	switch {
	case postgres.IsForeignKeyViolation(err, "inventory_variant_id_fkey"):
		return Level{}, ErrVariantNotFound
	case postgres.IsCheckViolation(err, "inventory_reserved_within_on_hand"):
		return Level{}, ErrBelowReserved
	case err != nil:
		return Level{}, fmt.Errorf("inventory: set on hand: %w", err)
	}
	l.Available = l.OnHand - l.Reserved
	return l, nil
}

// Handler serves the admin inventory endpoints.
type Handler struct {
	store *Store
}

// NewHandler returns a Handler.
func NewHandler(store *Store) *Handler { return &Handler{store: store} }

var errNotFound = httpx.NewError(http.StatusNotFound, "VARIANT_NOT_FOUND", "Variant not found")

// Get handles GET /api/v1/variants/{id}/inventory.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", errNotFound)
	if err != nil {
		return err
	}
	l, err := h.store.Level(r.Context(), id)
	if errors.Is(err, ErrVariantNotFound) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, l)
	return nil
}

type setRequest struct {
	OnHand *int `json:"on_hand"`
}

// maxOnHand keeps counts within the database integer range with room to
// spare.
const maxOnHand = 1_000_000

// Set handles PUT /api/v1/variants/{id}/inventory.
func (h *Handler) Set(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", errNotFound)
	if err != nil {
		return err
	}
	var req setRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		return err
	}
	var p httpx.Problems
	if req.OnHand == nil || *req.OnHand < 0 || *req.OnHand > maxOnHand {
		p.Add("on_hand", "must be an integer between 0 and 1000000")
	}
	if err := p.Err(); err != nil {
		return err
	}

	l, err := h.store.SetOnHand(r.Context(), id, *req.OnHand)
	switch {
	case errors.Is(err, ErrVariantNotFound):
		return errNotFound
	case errors.Is(err, ErrBelowReserved):
		return httpx.NewError(http.StatusConflict, "BELOW_RESERVED",
			"Stock cannot be lower than the units reserved by pending orders")
	case err != nil:
		return err
	}
	logging.FromContext(r.Context()).InfoContext(r.Context(), "inventory.adjusted",
		"variant_id", id, "on_hand", l.OnHand, "reserved", l.Reserved)
	httpx.WriteJSON(w, http.StatusOK, l)
	return nil
}
