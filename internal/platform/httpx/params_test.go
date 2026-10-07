package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParsePage(t *testing.T) {
	tests := []struct {
		query    string
		want     Page
		problems int
	}{
		{"", Page{1, 20}, 0},
		{"?page=3&per_page=50", Page{3, 50}, 0},
		{"?page=0", Page{1, 20}, 1},
		{"?page=abc&per_page=101", Page{1, 20}, 2},
		{"?per_page=-1", Page{1, 20}, 1},
	}
	for _, tt := range tests {
		var p Problems
		got := ParsePage(httptest.NewRequest(http.MethodGet, "/"+tt.query, nil), &p)
		if got != tt.want || len(p) != tt.problems {
			t.Errorf("%q: page = %+v, problems = %d; want %+v, %d", tt.query, got, len(p), tt.want, tt.problems)
		}
	}
	if (Page{Number: 3, PerPage: 20}).Offset() != 40 {
		t.Error("Offset is wrong")
	}
}

func TestNewPaginated(t *testing.T) {
	got := NewPaginated[int](nil, Page{Number: 2, PerPage: 20}, 41)
	if got.Data == nil || got.Pagination.TotalPages != 3 || got.Pagination.Total != 41 {
		t.Errorf("unexpected pagination: %+v", got)
	}
}

func TestIsUUID(t *testing.T) {
	for s, want := range map[string]bool{
		"01a11699-7a06-7d19-ace9-45b3da7b647b": true,
		"01A11699-7A06-7D19-ACE9-45B3DA7B647B": true,
		"01a11699-7a06-7d19-ace9-45b3da7b647":  false,
		"' OR 1=1 --":                          false,
		"":                                     false,
	} {
		if IsUUID(s) != want {
			t.Errorf("IsUUID(%q) = %v, want %v", s, !want, want)
		}
	}
}
