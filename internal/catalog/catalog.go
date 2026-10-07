// Package catalog manages categories, products and their sellable variants.
// Stock levels belong to the inventory package; the catalog only reads them
// to report availability.
package catalog

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Currency is the single currency the store sells in.
const Currency = "brl"

// Status controls a product's visibility. Only active products are public.
type Status string

const (
	StatusDraft    Status = "draft"
	StatusActive   Status = "active"
	StatusArchived Status = "archived"
)

func (s Status) valid() bool {
	return s == StatusDraft || s == StatusActive || s == StatusArchived
}

// Category groups products.
type Category struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CategoryRef is the category as embedded in a product.
type CategoryRef struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// Variant is the sellable unit of a product, e.g. "256GB / Black".
type Variant struct {
	ID         string            `json:"id"`
	SKU        string            `json:"sku"`
	Name       string            `json:"name"`
	Attributes map[string]string `json:"attributes"`
	PriceCents int64             `json:"price_cents"`
	Active     bool              `json:"active"`
	InStock    bool              `json:"in_stock"`
}

// Product is the full product with its variants.
type Product struct {
	ID          string      `json:"id"`
	Slug        string      `json:"slug"`
	Name        string      `json:"name"`
	Brand       string      `json:"brand"`
	Description string      `json:"description"`
	Status      Status      `json:"status"`
	Category    CategoryRef `json:"category"`
	Currency    string      `json:"currency"`
	Variants    []Variant   `json:"variants"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// ProductSummary is a product as shown in listings.
type ProductSummary struct {
	ID       string      `json:"id"`
	Slug     string      `json:"slug"`
	Name     string      `json:"name"`
	Brand    string      `json:"brand"`
	Status   Status      `json:"status"`
	Category CategoryRef `json:"category"`
	Currency string      `json:"currency"`
	// MinPriceCents is the cheapest active variant ("from R$ ..."). It is
	// null only in admin listings, for products without active variants.
	MinPriceCents *int64    `json:"min_price_cents"`
	InStock       bool      `json:"in_stock"`
	CreatedAt     time.Time `json:"created_at"`
}

var (
	ErrCategoryNotFound = errors.New("catalog: category not found")
	ErrCategoryInUse    = errors.New("catalog: category has products")
	ErrProductNotFound  = errors.New("catalog: product not found")
	ErrProductHasOrders = errors.New("catalog: product has been ordered")
	ErrVariantNotFound  = errors.New("catalog: variant not found")
	ErrSlugTaken        = errors.New("catalog: slug already in use")
	ErrSKUTaken         = errors.New("catalog: SKU already in use")
)

// slugPattern matches the CHECK constraint on categories.slug and
// products.slug.
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Slugify turns a name into a URL slug: "Fones Bluetooth Pró" becomes
// "fones-bluetooth-pro".
func Slugify(name string) string {
	var b strings.Builder
	dash := false
	// NFD splits "ó" into "o" plus a combining accent, which is then dropped.
	for _, r := range norm.NFD.String(strings.ToLower(name)) {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		default:
			dash = true
		}
	}
	return b.String()
}

// maxSearchTerms bounds the work a single search can cause.
const maxSearchTerms = 8

// prefixQuery turns free text into a PostgreSQL tsquery that matches every
// word as a prefix ("iph 15" becomes "iph:* & 15:*"), so results appear
// while the user is still typing. Only letters and digits survive, so user
// input can never inject tsquery operators.
func prefixQuery(q string) string {
	words := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) > maxSearchTerms {
		words = words[:maxSearchTerms]
	}
	for i, w := range words {
		words[i] = w + ":*"
	}
	return strings.Join(words, " & ")
}
