package httpx

import "net/http"

// FieldProblem describes why one input field is invalid.
type FieldProblem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Problems collects validation failures so a client sees every invalid field
// in one response.
type Problems []FieldProblem

// Add records a problem with field.
func (p *Problems) Add(field, message string) {
	*p = append(*p, FieldProblem{Field: field, Message: message})
}

// Err returns a 422 VALIDATION_FAILED error listing the problems, or nil if
// there are none.
func (p Problems) Err() error {
	if len(p) == 0 {
		return nil
	}
	return &Error{
		Status:  http.StatusUnprocessableEntity,
		Code:    "VALIDATION_FAILED",
		Message: "One or more fields are invalid",
		Details: p,
	}
}
