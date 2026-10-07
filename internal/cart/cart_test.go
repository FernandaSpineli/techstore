package cart

import (
	"errors"
	"testing"
)

func TestCheckQuantity(t *testing.T) {
	tests := []struct {
		name                string
		quantity, available int
		wantErr             error
	}{
		{"within stock", 2, 5, nil},
		{"exactly the stock", 5, 5, nil},
		{"more than stock", 6, 5, &InsufficientStockError{}},
		{"none available", 1, 0, &InsufficientStockError{}},
		{"over the per-line cap", 100, 1000, ErrQuantityLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkQuantity(tt.quantity, tt.available)
			var stockErr *InsufficientStockError
			switch {
			case tt.wantErr == nil && err != nil:
				t.Fatalf("err = %v, want nil", err)
			case errors.As(tt.wantErr, &stockErr):
				if !errors.As(err, &stockErr) || stockErr.Requested != tt.quantity || stockErr.Available != tt.available {
					t.Fatalf("err = %v, want InsufficientStockError{%d, %d}", err, tt.quantity, tt.available)
				}
			case tt.wantErr != nil && !errors.Is(err, tt.wantErr):
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	c := summarize([]Item{
		{VariantID: "a", UnitPriceCents: 499900, Quantity: 2, purchasable: true, available: 5},
		{VariantID: "b", UnitPriceCents: 199900, Quantity: 1, purchasable: true, available: 1},
	}, "brl")

	if c.SubtotalCents != 2*499900+199900 || c.ItemCount != 3 || !c.CheckoutReady {
		t.Errorf("cart = %+v", c)
	}
	if c.Items[0].LineTotalCents != 999800 {
		t.Errorf("line total = %d, want 999800", c.Items[0].LineTotalCents)
	}
}

func TestSummarizeFlagsIssues(t *testing.T) {
	c := summarize([]Item{
		{VariantID: "ok", UnitPriceCents: 100, Quantity: 1, purchasable: true, available: 3},
		{VariantID: "gone", UnitPriceCents: 100, Quantity: 1, purchasable: false, available: 3},
		{VariantID: "short", UnitPriceCents: 100, Quantity: 4, purchasable: true, available: 2},
		{VariantID: "oversold", UnitPriceCents: 100, Quantity: 1, purchasable: true, available: -1},
	}, "brl")

	if c.CheckoutReady {
		t.Error("cart with issues is checkout ready")
	}
	want := []Issue{"", IssueUnavailable, IssueInsufficientStock, IssueInsufficientStock}
	for i, it := range c.Items {
		if it.Issue != want[i] {
			t.Errorf("%s: issue = %q, want %q", it.VariantID, it.Issue, want[i])
		}
	}
	if *c.Items[2].AvailableQuantity != 2 || *c.Items[3].AvailableQuantity != 0 {
		t.Error("available quantity not reported (or negative)")
	}
}

func TestSummarizeEmptyCart(t *testing.T) {
	c := summarize(nil, "brl")
	if c.Items == nil || c.CheckoutReady || c.SubtotalCents != 0 {
		t.Errorf("empty cart = %+v", c)
	}
}
