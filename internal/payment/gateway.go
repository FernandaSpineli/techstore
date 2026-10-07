// Package payment takes payment for orders through Stripe Checkout and
// applies the outcome reported by Stripe webhooks.
//
// Card data never touches this service: customers pay on Stripe's hosted
// page, and the order is only marked paid when a signed webhook confirms
// it. The redirect back to the shop proves nothing.
package payment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stripe/stripe-go/v86"
)

// Gateway creates hosted checkout sessions. It is an interface so tests can
// run the whole payment flow without network access.
type Gateway interface {
	CreateCheckoutSession(ctx context.Context, req CheckoutRequest) (Session, error)
}

// CheckoutRequest describes the session to create. Every amount comes from
// the stored order, never from the client.
type CheckoutRequest struct {
	OrderID        string
	CustomerEmail  string
	Currency       string
	Lines          []CheckoutLine
	SuccessURL     string
	CancelURL      string
	ExpiresAt      time.Time
	IdempotencyKey string
}

// CheckoutLine is one line on the hosted checkout page.
type CheckoutLine struct {
	Name           string
	UnitPriceCents int64
	Quantity       int
}

// Session is a created checkout session.
type Session struct {
	ID        string
	URL       string
	ExpiresAt time.Time
}

// StripeGateway is the Gateway backed by the Stripe API.
type StripeGateway struct {
	client *stripe.Client
}

// NewStripeGateway returns a gateway using secretKey.
func NewStripeGateway(secretKey string) *StripeGateway {
	return &StripeGateway{client: stripe.NewClient(secretKey)}
}

// CreateCheckoutSession implements Gateway.
func (g *StripeGateway) CreateCheckoutSession(ctx context.Context, req CheckoutRequest) (Session, error) {
	s, err := g.client.V1CheckoutSessions.Create(ctx, checkoutParams(req))
	if err != nil {
		return Session{}, describeStripeError(err)
	}
	return Session{ID: s.ID, URL: s.URL, ExpiresAt: time.Unix(s.ExpiresAt, 0)}, nil
}

func checkoutParams(req CheckoutRequest) *stripe.CheckoutSessionCreateParams {
	p := &stripe.CheckoutSessionCreateParams{
		Mode:              stripe.String(string(stripe.CheckoutSessionModePayment)),
		ClientReferenceID: stripe.String(req.OrderID),
		CustomerEmail:     stripe.String(req.CustomerEmail),
		SuccessURL:        stripe.String(req.SuccessURL),
		CancelURL:         stripe.String(req.CancelURL),
		ExpiresAt:         stripe.Int64(req.ExpiresAt.Unix()),
		Metadata:          map[string]string{"order_id": req.OrderID},
	}
	for _, l := range req.Lines {
		p.LineItems = append(p.LineItems, &stripe.CheckoutSessionCreateLineItemParams{
			Quantity: stripe.Int64(int64(l.Quantity)),
			PriceData: &stripe.CheckoutSessionCreateLineItemPriceDataParams{
				Currency:    stripe.String(req.Currency),
				UnitAmount:  stripe.Int64(l.UnitPriceCents),
				ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{Name: stripe.String(l.Name)},
			},
		})
	}
	// Retries with the same key return the original session instead of
	// creating a second one.
	p.SetIdempotencyKey(req.IdempotencyKey)
	return p
}

// describeStripeError keeps the parts of a Stripe error that are useful in
// logs (type, code, request ID) and safe to record.
func describeStripeError(err error) error {
	var se *stripe.Error
	if errors.As(err, &se) {
		return fmt.Errorf("stripe: %s (type=%s code=%s status=%d request_id=%s): %w",
			se.Msg, se.Type, se.Code, se.HTTPStatusCode, se.RequestID, err)
	}
	return fmt.Errorf("stripe: %w", err)
}
