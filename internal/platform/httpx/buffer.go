package httpx

import (
	"bytes"
	"net/http"
)

// BufferedResponse captures a handler's response so middleware can inspect
// or store it before (or instead of) sending it.
type BufferedResponse struct {
	header http.Header
	Status int
	Body   bytes.Buffer
}

// NewBufferedResponse returns an empty buffer with status 200.
func NewBufferedResponse() *BufferedResponse {
	return &BufferedResponse{header: http.Header{}, Status: http.StatusOK}
}

func (b *BufferedResponse) Header() http.Header         { return b.header }
func (b *BufferedResponse) Write(p []byte) (int, error) { return b.Body.Write(p) }
func (b *BufferedResponse) WriteHeader(status int)      { b.Status = status }

// CopyTo sends the captured response to w.
func (b *BufferedResponse) CopyTo(w http.ResponseWriter) {
	for k, v := range b.header {
		w.Header()[k] = v
	}
	w.WriteHeader(b.Status)
	_, _ = w.Write(b.Body.Bytes())
}
