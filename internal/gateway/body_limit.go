package gateway

import (
	"errors"
	"io"
	"net/http"
	"sync/atomic"
)

// limitedBody records when a read of a request body went over the
// http.MaxBytesReader limit that wraps it.
type limitedBody struct {
	io.ReadCloser
	exceeded atomic.Bool
}

func (b *limitedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	var tooLarge *http.MaxBytesError
	if err != nil && errors.As(err, &tooLarge) {
		b.exceeded.Store(true)
	}
	return n, err
}

// tooLargeWriter replaces the error response of a handler whose request body
// went over the limit with the standard 413 response. Handlers map a body
// read error to different statuses (usually 400), so a streamed body with no
// Content-Length would otherwise not get the 413 that a declared one gets.
type tooLargeWriter struct {
	http.ResponseWriter
	body     *limitedBody
	replaced bool
}

func (w *tooLargeWriter) WriteHeader(status int) {
	if status >= http.StatusBadRequest && w.body.exceeded.Load() {
		w.replaced = true
		w.Header().Del("Content-Length")
		writeError(w.ResponseWriter, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *tooLargeWriter) Write(p []byte) (int, error) {
	if w.replaced {
		// Drop the handler's own error body; the 413 body is already sent.
		return len(p), nil
	}
	return w.ResponseWriter.Write(p)
}

func (w *tooLargeWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *tooLargeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
