// Package order turns carts into orders and moves them through their
// lifecycle, keeping inventory reservations in step with each status.
package order

import (
	"errors"
	"fmt"
	"time"

	"github.com/FernandaSpineli/techstore/internal/inventory"
)

// Status is where an order is in its lifecycle.
type Status string

const (
	StatusPendingPayment Status = "pending_payment"
	StatusPaid           Status = "paid"
	StatusShipped        Status = "shipped"
	StatusDelivered      Status = "delivered"
	StatusCancelled      Status = "cancelled"
	StatusExpired        Status = "expired"
)

func (s Status) valid() bool {
	_, ok := transitions[s]
	return ok
}

// stockEffect is what a transition does to the order's inventory
// reservation.
type stockEffect int

const (
	stockNone    stockEffect = iota
	stockCommit              // the units are sold: they leave on_hand
	stockRelease             // the units return to available stock
)

// transitions is the order state machine: for each status, the statuses it
// may move to and what that does to stock. Statuses with no entries are
// terminal.
var transitions = map[Status]map[Status]stockEffect{
	StatusPendingPayment: {
		StatusPaid:      stockCommit,
		StatusCancelled: stockRelease,
		StatusExpired:   stockRelease,
	},
	StatusPaid:      {StatusShipped: stockNone},
	StatusShipped:   {StatusDelivered: stockNone},
	StatusDelivered: {},
	StatusCancelled: {},
	StatusExpired:   {},
}

// adminTargets are the statuses an admin may set by hand. Payment is only
// ever confirmed by the payment provider, and expiry only by the sweeper.
var adminTargets = map[Status]bool{
	StatusShipped:   true,
	StatusDelivered: true,
	StatusCancelled: true,
}

// effect returns what moving from -> to does to stock, or an error if the
// transition is not allowed.
func effect(from, to Status) (stockEffect, error) {
	e, ok := transitions[from][to]
	if !ok {
		return stockNone, &TransitionError{From: from, To: to}
	}
	return e, nil
}

// Order is a placed order.
type Order struct {
	ID            string         `json:"id"`
	UserID        string         `json:"user_id"`
	Status        Status         `json:"status"`
	Currency      string         `json:"currency"`
	TotalCents    int64          `json:"total_cents"`
	ExpiresAt     *time.Time     `json:"expires_at,omitempty"`
	Items         []Item         `json:"items"`
	StatusHistory []StatusChange `json:"status_history"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

// Item is an order line. Name, SKU and price are snapshots taken when the
// order was placed.
type Item struct {
	VariantID      string `json:"variant_id"`
	ProductName    string `json:"product_name"`
	VariantName    string `json:"variant_name"`
	SKU            string `json:"sku"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	Quantity       int    `json:"quantity"`
	LineTotalCents int64  `json:"line_total_cents"`
}

// StatusChange is one entry of an order's audit trail.
type StatusChange struct {
	From   *Status   `json:"from"`
	To     Status    `json:"to"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// Summary is an order as shown in a history listing.
type Summary struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	Status     Status    `json:"status"`
	Currency   string    `json:"currency"`
	TotalCents int64     `json:"total_cents"`
	ItemCount  int       `json:"item_count"`
	CreatedAt  time.Time `json:"created_at"`
}

func (o Order) lines() []inventory.Line {
	lines := make([]inventory.Line, len(o.Items))
	for i, it := range o.Items {
		lines[i] = inventory.Line{VariantID: it.VariantID, Quantity: it.Quantity}
	}
	return lines
}

var (
	ErrNotFound           = errors.New("order: not found")
	ErrCartEmpty          = errors.New("order: cart is empty")
	ErrCartHasUnavailable = errors.New("order: cart has items that are no longer for sale")
	ErrPaymentInProgress  = errors.New("order: a checkout session is still open")
)

// TransitionError reports a status change the state machine forbids.
type TransitionError struct {
	From, To Status
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("order: cannot move from %s to %s", e.From, e.To)
}
