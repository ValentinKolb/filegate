package metrics

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"time"
)

// opHolder is a mutable per-request operation label. The middleware
// installs one into the request context before dispatching; the
// adapter fills it via SetOp during dispatch; the middleware reads it
// back after the handler returns. A pointer-in-context (rather than a
// value) is what makes this work even when the handler derives child
// contexts — the holder is reachable from any descendant context, so
// the adapter never has to thread a new request back out.
type opHolder struct{ op string }

type opHolderKey struct{}

// SetOp records the operation label for the current request (e.g.
// "PutObject"). The S3 adapter calls it at dispatch. It is a no-op
// when the request isn't instrumented (metrics disabled / handler not
// wrapped), so adapters can call it unconditionally without a nil
// check or a metrics dependency beyond this one function.
func SetOp(ctx context.Context, op string) {
	if h, ok := ctx.Value(opHolderKey{}).(*opHolder); ok {
		h.op = op
	}
}

// Middleware returns an http middleware that records the HTTP RED
// metrics for the given adapter ("rest" or "s3"). The operation label
// comes from OpFromContext, falling back to the request method when
// the adapter didn't set one.
//
// The middleware is intended to wrap the whole adapter handler. When
// metrics is disabled the caller simply doesn't apply it (zero
// per-request cost).
func (r *Registry) Middleware(adapter string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			start := time.Now()
			r.httpInFlight.WithLabelValues(adapter).Inc()
			// defer the dec so a panic in a handler can't leak the
			// in-flight gauge upward forever.
			defer r.httpInFlight.WithLabelValues(adapter).Dec()

			// Install a mutable op holder the handler can fill via
			// SetOp during dispatch. Reachable from any child context
			// the handler derives.
			holder := &opHolder{}
			req = req.WithContext(context.WithValue(req.Context(), opHolderKey{}, holder))

			sw := &sizeStatusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, req)

			op := holder.op
			if op == "" {
				op = req.Method
			}
			dur := time.Since(start).Seconds()
			class := statusClass(sw.status)

			r.httpRequests.WithLabelValues(adapter, op, class).Inc()
			r.httpDuration.WithLabelValues(adapter, op).Observe(dur)
			r.httpRespBytes.WithLabelValues(adapter, op).Observe(float64(sw.written))
			if size := requestBodySize(req); size >= 0 {
				r.httpReqBytes.WithLabelValues(adapter, op).Observe(float64(size))
			}
		})
	}
}

// statusClass buckets an HTTP status code into "2xx"/"3xx"/"4xx"/
// "5xx" (or "1xx" / "other"). Keeping the cardinality at one series
// per class rather than per exact code is the whole point.
func statusClass(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500 && code < 600:
		return "5xx"
	case code >= 100 && code < 200:
		return "1xx"
	default:
		return "other"
	}
}

// requestBodySize returns the request body size in bytes, or -1 when
// it can't be determined (no Content-Length and a streaming body we
// must not buffer). We deliberately do NOT wrap the body in a
// counting reader: that would interfere with the adapters' own body
// handling (SigV4 chunked decoding, MaxBytesReader). Content-Length
// covers the overwhelming majority of real client requests.
func requestBodySize(req *http.Request) int64 {
	if req.ContentLength >= 0 {
		return req.ContentLength
	}
	if v := req.Header.Get("Content-Length"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return -1
}

// sizeStatusWriter captures the response status code and the number of
// body bytes written, for the duration + size + status_class metrics.
type sizeStatusWriter struct {
	http.ResponseWriter
	status      int
	written     int64
	wroteHeader bool
}

func (w *sizeStatusWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status = status
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *sizeStatusWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		// Implicit 200 on first write without an explicit WriteHeader.
		w.wroteHeader = true
	}
	n, err := w.ResponseWriter.Write(b)
	w.written += int64(n)
	return n, err
}

// ReadFrom preserves net/http's sendfile fast path. io.Copy /
// io.CopyBuffer check whether the destination implements io.ReaderFrom
// and, for an *os.File source, the http response writer uses sendfile.
// Without this method the wrapper would hide ReaderFrom and force every
// large download (GetObject, REST content streaming) through a buffered
// copy. We delegate to the underlying writer's ReaderFrom when present
// and count the bytes it reports; otherwise we copy through Write
// (which counts) via a writer-only shim so io.Copy can't re-enter here.
func (w *sizeStatusWriter) ReadFrom(src io.Reader) (int64, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	if rf, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err := rf.ReadFrom(src)
		w.written += n
		return n, err
	}
	return io.Copy(writeOnly{w}, src)
}

// Flush delegates to the underlying writer so streaming handlers keep
// working when instrumented. A no-op when the underlying writer isn't
// a Flusher.
func (w *sizeStatusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// writeOnly exposes only Write, so io.Copy in the ReadFrom fallback
// uses the buffered loop (counting via sizeStatusWriter.Write) instead
// of recursing into ReadFrom.
type writeOnly struct{ w *sizeStatusWriter }

func (c writeOnly) Write(b []byte) (int, error) { return c.w.Write(b) }
