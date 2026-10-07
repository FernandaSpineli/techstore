package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// maxBodyBytes caps request bodies; the API never needs large payloads.
const maxBodyBytes = 1 << 20

// DecodeJSON decodes a single JSON object from the request body into dst.
// Unknown fields are rejected so client typos fail loudly instead of being
// silently ignored.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &maxErr):
			return NewError(http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "Request body is too large")
		case errors.Is(err, io.EOF):
			return NewError(http.StatusBadRequest, "INVALID_JSON", "Request body must not be empty")
		case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
			return NewError(http.StatusBadRequest, "INVALID_JSON", "Request body is not valid JSON")
		case errors.As(err, &typeErr):
			return NewError(http.StatusBadRequest, "INVALID_JSON",
				fmt.Sprintf("Field %q has the wrong type", typeErr.Field))
		default:
			// Unknown fields and other decoder errors carry no sensitive data.
			return NewError(http.StatusBadRequest, "INVALID_JSON", err.Error())
		}
	}
	if dec.More() {
		return NewError(http.StatusBadRequest, "INVALID_JSON", "Request body must contain a single JSON object")
	}
	return nil
}
