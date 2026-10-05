package codeskop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// BlockedBody is the JSON body sent to consumers blocked by API Trust.
const BlockedBody = `{"error":"consumer_blocked"}`

// Recorder tracks one incoming request; framework integrations (net/http, Gin, Echo) use it.
type Recorder struct {
	Method    string
	RequestID string
	started   time.Time
	failed    bool
	blocked   bool
	extra     map[string]any
	finished  bool
	holder    *userHolder
}

// StartRequest begins recording r and returns the recorder plus a context carrying the request's user holder.
func StartRequest(r *http.Request) (*Recorder, context.Context) {
	id := r.Header.Get("X-Request-Id")
	if id == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		id = hex.EncodeToString(b)
	} else if len(id) > 128 {
		id = id[:128]
	}
	h := &userHolder{}
	return &Recorder{Method: r.Method, RequestID: id, started: time.Now(), holder: h}, context.WithValue(r.Context(), userKey{}, h)
}

// Trust runs API Trust capture; true means the request must be refused (opt-in blocking, fails open).
func (rec *Recorder) Trust(r *http.Request) (blocked bool) {
	c := getClient()
	if c == nil || c.trust == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			blocked = false
		}
	}()
	extra, b := c.trust.inspect(r)
	rec.extra, rec.blocked = extra, b
	return b
}

// Error reports err as an unhandled error of this request.
func (rec *Recorder) Error(ctx context.Context, err error, route, mechanism string) {
	rec.failed = true
	captureError(ctx, err, 3, false, mechanism, map[string]any{"method": rec.Method, "route": route, "request_id": rec.RequestID}, nil)
}

// Panic reports a recovered panic of this request.
func (rec *Recorder) Panic(ctx context.Context, p any, route, mechanism string) {
	rec.failed = true
	capturePanic(ctx, p, mechanism, map[string]any{"method": rec.Method, "route": route, "request_id": rec.RequestID})
}

// Finish records the request (once).
func (rec *Recorder) Finish(route string, status int, reqBytes, resBytes int64) {
	if rec.finished {
		return
	}
	rec.finished = true
	c := getClient()
	if c == nil || !c.enabled || c.opts.DisableRequests || c.ignoredRoute(route) {
		return
	}
	extra := map[string]any{}
	for k, v := range rec.extra {
		extra[k] = v
	}
	if rec.blocked {
		extra["blocked"] = true
	}
	user := ""
	if !c.opts.DisableUserID {
		user = rec.holder.get()
	}
	c.capture(requestEvent(rec.Method, route, status, time.Since(rec.started), reqBytes, resBytes, rec.RequestID, rec.failed, user, extra))
}

// SetFramework names the framework on events (integrations call it).
func SetFramework(name string) {
	if c := getClient(); c != nil && (c.framework == "" || c.framework == "net/http") {
		c.framework = name
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Middleware records every request (by ServeMux pattern on Go 1.23+), reports panics (answering 500)
// and handles API Trust. Wrap your mux: http.ListenAndServe(":8080", codeskop.Middleware(mux)).
func Middleware(next http.Handler) http.Handler {
	SetFramework("net/http")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, ctx := StartRequest(r)
		r = r.WithContext(ctx)
		if rec.Trust(r) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(BlockedBody))
			rec.Finish(NormalizePath(r.URL.Path), http.StatusForbidden, r.ContentLength, int64(len(BlockedBody)))
			return
		}
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			route := routeOf(r)
			if p := recover(); p != nil {
				if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(p)
				}
				rec.Panic(ctx, p, route, "net/http")
				if sw.status == 0 {
					http.Error(sw, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				}
				sw.status = http.StatusInternalServerError
			}
			if sw.status == 0 {
				sw.status = http.StatusOK
			}
			rec.Finish(route, sw.status, r.ContentLength, sw.bytes)
		}()
		next.ServeHTTP(sw, r)
	})
}

func routeOf(r *http.Request) string {
	if r.Pattern != "" {
		return TemplateRoute(r.Pattern)
	}
	return NormalizePath(r.URL.Path)
}

// Transport wraps an http.RoundTripper so outgoing calls become api_timing / api_error events:
// client := &http.Client{Transport: codeskop.Transport(http.DefaultTransport)}.
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTripper{base}
}

type roundTripper struct{ base http.RoundTripper }

func (rt roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	started := time.Now()
	res, err := rt.base.RoundTrip(req)
	c := getClient()
	if c == nil || !c.enabled || c.opts.DisableOutgoing || req.URL == nil {
		return res, err
	}
	if ep, perr := url.Parse(c.opts.Endpoint); perr == nil && strings.EqualFold(ep.Host, req.URL.Host) {
		return res, err
	}
	status, kind := 0, ""
	if err != nil {
		kind = "network_error"
		var ne interface{ Timeout() bool }
		if errors.As(err, &ne) && ne.Timeout() {
			kind = "timeout"
		}
	} else {
		status = res.StatusCode
	}
	for _, e := range outgoingEvents(req.Method, req.URL.Hostname(), req.URL.Path, status, time.Since(started), kind, userFrom(req.Context())) {
		c.capture(e)
	}
	return res, err
}
