package httpx

import (
	"net/http"
	"regexp"
	"strconv"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// IsUUID reports whether s is a canonical UUID string.
func IsUUID(s string) bool { return uuidPattern.MatchString(s) }

// PathUUID returns the named path value if it is a UUID. A malformed ID can
// never match a row, so it is reported as notFound instead of reaching the
// database (where it would be a cast error).
func PathUUID(r *http.Request, name string, notFound error) (string, error) {
	id := r.PathValue(name)
	if !IsUUID(id) {
		return "", notFound
	}
	return id, nil
}

// Page is a validated page/per_page pair.
type Page struct {
	Number  int `json:"page"`
	PerPage int `json:"per_page"`
}

// Offset returns the number of rows to skip.
func (p Page) Offset() int { return (p.Number - 1) * p.PerPage }

const (
	defaultPerPage = 20
	maxPerPage     = 100
)

// ParsePage reads ?page= and ?per_page=, recording problems for invalid
// values. per_page is capped so a client cannot ask for the whole table.
func ParsePage(r *http.Request, p *Problems) Page {
	page := Page{Number: 1, PerPage: defaultPerPage}
	q := r.URL.Query()
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			p.Add("page", "must be a positive integer")
		} else {
			page.Number = n
		}
	}
	if v := q.Get("per_page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPerPage {
			p.Add("per_page", "must be between 1 and 100")
		} else {
			page.PerPage = n
		}
	}
	return page
}

// Paginated is the envelope for list responses.
type Paginated[T any] struct {
	Data       []T        `json:"data"`
	Pagination Pagination `json:"pagination"`
}

// Pagination describes where a page sits in the full result set.
type Pagination struct {
	Page       int `json:"page"`
	PerPage    int `json:"per_page"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

// NewPaginated builds a list response. data is never encoded as null.
func NewPaginated[T any](data []T, page Page, total int) Paginated[T] {
	if data == nil {
		data = []T{}
	}
	return Paginated[T]{
		Data: data,
		Pagination: Pagination{
			Page:       page.Number,
			PerPage:    page.PerPage,
			Total:      total,
			TotalPages: (total + page.PerPage - 1) / page.PerPage,
		},
	}
}
