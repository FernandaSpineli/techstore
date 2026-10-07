package httpx

import (
	"errors"
	"net/http"

	"github.com/FernandaSpineli/techstore/internal/platform/logging"
)

// Error is an error whose code and message are safe to show to API clients.
// Handlers translate domain errors into *Error; anything else is treated as
// an internal failure and hidden behind a generic 500.
type Error struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// NewError returns an *Error with the given status, code and message.
func NewError(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

var errInternal = NewError(http.StatusInternalServerError, "INTERNAL", "Internal server error")

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Details   any    `json:"details,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// WriteError writes err using the API error envelope. Errors that are not an
// *Error are logged with full detail and reported to the client as a generic
// 500, so internal messages never leak.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		logging.FromContext(r.Context()).ErrorContext(r.Context(), "http.unhandled_error", "error", err)
		apiErr = errInternal
	}
	WriteJSON(w, apiErr.Status, errorEnvelope{Error: errorBody{
		Code:      apiErr.Code,
		Message:   apiErr.Message,
		Details:   apiErr.Details,
		RequestID: RequestIDFrom(r.Context()),
	}})
}

// HandlerFunc is an http.HandlerFunc that returns an error instead of writing
// it, keeping error responses uniform across handlers.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

func (f HandlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := f(w, r); err != nil {
		WriteError(w, r, err)
	}
}
