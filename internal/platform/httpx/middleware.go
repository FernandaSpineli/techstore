package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/FernandaSpineli/techstore/internal/platform/logging"
)

// Middleware wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middleware so that the first one listed runs first.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

const requestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// RequestIDFrom returns the request ID stored in ctx, if any.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// RequestID propagates a well-formed incoming X-Request-ID or generates a new
// one, and echoes it in the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// validRequestID accepts short IDs made of safe characters only, so a client
// cannot inject arbitrary content into our logs.
func validRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		isAlnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !isAlnum && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	return hex.EncodeToString(b)
}

type accessLogAttrsKey struct{}

type accessLogAttrs struct{ attrs []slog.Attr }

// AddAccessLogAttrs attaches attributes (such as the authenticated user ID)
// to the access log line of the current request. Inner middleware learns
// things the outer access logger cannot see on its own.
func AddAccessLogAttrs(ctx context.Context, attrs ...slog.Attr) {
	if a, ok := ctx.Value(accessLogAttrsKey{}).(*accessLogAttrs); ok {
		a.attrs = append(a.attrs, attrs...)
	}
}

// AccessLog stores a request-scoped logger in the context and logs one line
// per request. Request bodies and headers are never logged.
func AccessLog(base *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			logger := base.With("request_id", RequestIDFrom(r.Context()))
			extra := &accessLogAttrs{}
			ctx := logging.WithLogger(r.Context(), logger)
			r = r.WithContext(context.WithValue(ctx, accessLogAttrsKey{}, extra))
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			level := slog.LevelInfo
			if rec.status >= http.StatusInternalServerError {
				level = slog.LevelError
			}
			logger.LogAttrs(r.Context(), level, "http.request",
				slog.String("method", r.Method),
				slog.String("route", r.Pattern), // set by ServeMux; keeps IDs out of the route dimension
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int("bytes", rec.bytes),
				slog.Int64("duration_ms", time.Since(start).Milliseconds()),
				slog.String("remote_ip", ClientIP(r)),
				slog.GroupAttrs("", extra.attrs...), // an empty group key inlines the attrs
			)
		})
	}
}

// ClientIP returns the IP address of the connection. Behind a reverse proxy
// this is the proxy; see the README for the trade-off.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// Recover turns a panic into a logged error and a generic 500 response. The
// stack trace goes to the log, never to the client.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			if v == http.ErrAbortHandler { //nolint:errorlint // sentinel compared as recovered value, per net/http docs
				panic(v)
			}
			logging.FromContext(r.Context()).ErrorContext(r.Context(), "http.panic",
				"panic", v, "stack", string(debug.Stack()))
			WriteError(w, r, errInternal)
		}()
		next.ServeHTTP(w, r)
	})
}
