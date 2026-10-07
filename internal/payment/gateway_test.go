package payment

import (
	"testing"
	"time"
)

func TestCheckoutParams(t *testing.T) {
	expires := time.Date(2026, 10, 7, 15, 0, 0, 0, time.UTC)
	p := checkoutParams(CheckoutRequest{
		OrderID:       "order-1",
		CustomerEmail: "ana@example.com",
		Currency:      "brl",
		Lines: []CheckoutLine{
			{Name: "iPhone 15 — 128GB", UnitPriceCents: 499900, Quantity: 2},
			{Name: "Galaxy S24 — 256GB", UnitPriceCents: 399900, Quantity: 1},
		},
		SuccessURL:     "http://shop.test/checkout/success?order_id=order-1",
		CancelURL:      "http://shop.test/checkout/cancel?order_id=order-1",
		ExpiresAt:      expires,
		IdempotencyKey: "checkout-order-1-1",
	})

	if *p.Mode != "payment" || *p.ClientReferenceID != "order-1" || p.Metadata["order_id"] != "order-1" {
		t.Errorf("session params = %+v", p)
	}
	if *p.ExpiresAt != expires.Unix() || *p.IdempotencyKey != "checkout-order-1-1" {
		t.Errorf("expires_at/idempotency key = %d / %s", *p.ExpiresAt, *p.IdempotencyKey)
	}
	if len(p.LineItems) != 2 {
		t.Fatalf("%d line items, want 2", len(p.LineItems))
	}
	li := p.LineItems[0]
	if *li.Quantity != 2 || *li.PriceData.UnitAmount != 499900 || *li.PriceData.Currency != "brl" || *li.PriceData.ProductData.Name != "iPhone 15 — 128GB" {
		t.Errorf("first line item = %+v / %+v", li, li.PriceData)
	}
}
