// Package cart manages each signed-in user's shopping cart.
//
// The cart does not reserve stock: it checks availability when items are
// added or changed, and flags lines that became unavailable since. Stock is
// reserved, and checked again under lock, when an order is created.
package cart

import (
	"errors"
	"fmt"
)

// MaxQuantity is the most units of one variant a cart can hold. It matches
// the CHECK constraint on cart_items.quantity.
const MaxQuantity = 99

// Issue explains why a cart line cannot be checked out as it is.
type Issue string

const (
	// IssueUnavailable: the variant or its product is no longer for sale.
	IssueUnavailable Issue = "unavailable"
	// IssueInsufficientStock: fewer units are available than requested.
	IssueInsufficientStock Issue = "insufficient_stock"
)

// Item is one line of the cart, priced at the variant's current price.
type Item struct {
	VariantID      string `json:"variant_id"`
	SKU            string `json:"sku"`
	ProductName    string `json:"product_name"`
	ProductSlug    string `json:"product_slug"`
	VariantName    string `json:"variant_name"`
	UnitPriceCents int64  `json:"unit_price_cents"`
	Quantity       int    `json:"quantity"`
	LineTotalCents int64  `json:"line_total_cents"`
	Issue          Issue  `json:"issue,omitempty"`
	// AvailableQuantity is only set with IssueInsufficientStock, so the
	// client can offer to lower the quantity.
	AvailableQuantity *int `json:"available_quantity,omitempty"`

	purchasable bool
	available   int
}

// Cart is the priced view of a user's cart.
type Cart struct {
	Items         []Item `json:"items"`
	ItemCount     int    `json:"item_count"`
	SubtotalCents int64  `json:"subtotal_cents"`
	Currency      string `json:"currency"`
	// CheckoutReady is false when the cart is empty or any line has an issue.
	CheckoutReady bool `json:"checkout_ready"`
}

var (
	ErrVariantNotFound    = errors.New("cart: variant not found")
	ErrVariantUnavailable = errors.New("cart: variant not for sale")
	ErrItemNotFound       = errors.New("cart: item not in cart")
	ErrQuantityLimit      = fmt.Errorf("cart: more than %d units of one variant", MaxQuantity)
)

// InsufficientStockError reports that a requested quantity exceeds what is
// available.
type InsufficientStockError struct {
	Requested, Available int
}

func (e *InsufficientStockError) Error() string {
	return fmt.Sprintf("cart: requested %d units, %d available", e.Requested, e.Available)
}

// checkQuantity validates the quantity a line would end up with.
func checkQuantity(quantity, available int) error {
	if quantity > MaxQuantity {
		return ErrQuantityLimit
	}
	if quantity > available {
		return &InsufficientStockError{Requested: quantity, Available: max(available, 0)}
	}
	return nil
}

// summarize prices every line, flags issues and totals the cart.
func summarize(items []Item, currency string) Cart {
	c := Cart{Items: items, Currency: currency, CheckoutReady: len(items) > 0}
	if c.Items == nil {
		c.Items = []Item{}
	}
	for i := range c.Items {
		it := &c.Items[i]
		it.LineTotalCents = it.UnitPriceCents * int64(it.Quantity)
		switch {
		case !it.purchasable:
			it.Issue = IssueUnavailable
		case it.Quantity > it.available:
			it.Issue = IssueInsufficientStock
			avail := max(it.available, 0)
			it.AvailableQuantity = &avail
		}
		if it.Issue != "" {
			c.CheckoutReady = false
		}
		c.ItemCount += it.Quantity
		c.SubtotalCents += it.LineTotalCents
	}
	return c
}
