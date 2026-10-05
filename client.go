// Package codeskop is the Codeskop server SDK for Go: errors, panics, incoming requests and
// outgoing HTTP calls (docs/10 in the backend repo).
//
//	codeskop.Init(codeskop.Options{APIKey: os.Getenv("CODESKOP_API_KEY")})
//	defer codeskop.Close(2 * time.Second)
//	http.ListenAndServe(":8080", codeskop.Middleware(mux))
//
// Every function is safe to call before Init (it does nothing) and never panics.
package codeskop

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"path"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Version of the SDK.
const Version = "0.1.0"

const (
	maxBatch        = 100
	maxEventBytes   = 64 * 1024
	maxBodyBytes    = 1024 * 1024
	configRefresh   = 5 * time.Minute
	userAgentPrefix = "codeskop-go/"
)

var publicKey = regexp.MustCompile(`^cs_(live|test)_pk_[A-Za-z0-9_-]{8,}$`)

// Options configure the SDK (docs/10 §10.3). Zero values read CODESKOP_* environment variables.
type Options struct {
	APIKey           string
	Endpoint         string
	Environment      string
	Release          string
	DisableRequests  bool
	DisableOutgoing  bool
	SampleRates      map[string]float64
	IgnoreRoutes     []string
	IgnoreErrors     []string
	BeforeSend       func(Event) Event
	DisableUserID    bool
	MaxQueueEvents   int
	FlushInterval    time.Duration
	Debug            bool
	Disabled         bool
	APITrustResolver func(r *http.Request) string
	TrustProxy       *bool
}

// Client holds the queue and sends events in the background.
type Client struct {
	opts      Options
	enabled   bool
	http      *http.Client
	mu        sync.Mutex
	queue     []Event
	pending   []Event
	attempt   int
	nextSend  time.Time
	wake      chan struct{}
	stop      chan struct{}
	done      chan struct{}
	remoteMu  sync.RWMutex
	remote    map[string]any
	etag      string
	configAt  time.Time
	trust     *trust
	closeOnce sync.Once
	ready     chan struct{}
	framework string
	hostname  string
}

var (
	current   *Client
	currentMu sync.RWMutex
)

func getClient() *Client {
	currentMu.RLock()
	defer currentMu.RUnlock()
	return current
}

func env(k, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return fallback
}

func detectRelease() string {
	for _, k := range []string{"CODESKOP_RELEASE", "RENDER_GIT_COMMIT", "HEROKU_SLUG_COMMIT", "SOURCE_VERSION", "RAILWAY_GIT_COMMIT_SHA", "K_REVISION", "GITHUB_SHA"} {
		if v := os.Getenv(k); v != "" {
			if len(v) > 64 {
				v = v[:64]
			}
			return v
		}
	}
	return ""
}

func keyProblem(key string) string {
	switch {
	case key == "":
		return "no APIKey (set CODESKOP_API_KEY or Options.APIKey)"
	case strings.Contains(key, "_sk_"):
		return "a secret key (_sk_) was given; use the project's public key (cs_..._pk_...)"
	case !publicKey.MatchString(key):
		return "the APIKey doesn't look like a Codeskop public key"
	}
	return ""
}

// Init starts the SDK. Calling it again replaces the previous client.
func Init(o Options) *Client {
	if o.APIKey == "" {
		o.APIKey = env("CODESKOP_API_KEY", "")
	}
	o.APIKey = strings.TrimSpace(o.APIKey)
	o.Endpoint = strings.TrimRight(func() string {
		if o.Endpoint != "" {
			return o.Endpoint
		}
		return env("CODESKOP_ENDPOINT", "https://api.codeskop.com")
	}(), "/")
	if o.Environment == "" {
		o.Environment = env("CODESKOP_ENVIRONMENT", "production")
	}
	if o.Release == "" {
		o.Release = detectRelease()
	}
	if o.IgnoreRoutes == nil {
		o.IgnoreRoutes = []string{"/health*", "/healthz", "/metrics", "/favicon.ico"}
	}
	if o.MaxQueueEvents <= 0 {
		o.MaxQueueEvents = 10000
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = 5 * time.Second
	}
	host, _ := os.Hostname()
	c := &Client{opts: o, http: &http.Client{Timeout: 10 * time.Second}, wake: make(chan struct{}, 1), stop: make(chan struct{}),
		done: make(chan struct{}), remote: map[string]any{}, ready: make(chan struct{}), hostname: host}
	if p := keyProblem(o.APIKey); p != "" {
		log.Printf("codeskop: disabled: %s", p)
	} else if !o.Disabled {
		c.enabled = true
	}
	currentMu.Lock()
	old := current
	current = c
	currentMu.Unlock()
	if old != nil {
		old.Close(500 * time.Millisecond)
	}
	if c.enabled {
		go c.run()
	} else {
		close(c.ready)
		close(c.done)
	}
	return c
}

// Ready blocks until the first remote config fetch finished (or the timeout passes).
func (c *Client) Ready(timeout time.Duration) bool {
	select {
	case <-c.ready:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (c *Client) debugf(format string, args ...any) {
	if c.opts.Debug {
		log.Printf("codeskop: "+format, args...)
	}
}

func (c *Client) userAgent() string { return userAgentPrefix + Version }

func (c *Client) newRequest(method, url string, body []byte) (*http.Request, error) {
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequest(method, url, bytes.NewReader(body))
	} else {
		req, err = http.NewRequest(method, url, nil)
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.opts.APIKey)
	req.Header.Set("User-Agent", c.userAgent())
	return req, nil
}

// -- remote config & sampling -----------------------------------------------------

func (c *Client) refreshConfig(force bool) {
	if !force && time.Since(c.configAt) < configRefresh {
		return
	}
	c.configAt = time.Now()
	req, err := c.newRequest("GET", c.opts.Endpoint+"/v1/config", nil)
	if err != nil {
		return
	}
	if c.etag != "" {
		req.Header.Set("If-None-Match", c.etag)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.debugf("config fetch failed: %v", err)
		return
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case 200:
		var cfg map[string]any
		if json.NewDecoder(res.Body).Decode(&cfg) != nil {
			return
		}
		c.remoteMu.Lock()
		c.remote = cfg
		c.remoteMu.Unlock()
		c.etag = res.Header.Get("ETag")
		if v, ok := cfg["enabled"].(bool); ok && !v {
			c.mu.Lock()
			c.queue, c.pending = nil, nil
			c.mu.Unlock()
		}
		tc, _ := cfg["api_trust"].(map[string]any)
		if tc != nil && tc["enabled"] == true && c.trust == nil {
			c.trust = newTrust(c)
		}
		if c.trust != nil {
			c.trust.configure(tc)
		}
	case 401, 403:
		log.Printf("codeskop: the API key was refused (HTTP %d); events won't be accepted", res.StatusCode)
	}
}

func (c *Client) remoteGet(k string) any {
	c.remoteMu.RLock()
	defer c.remoteMu.RUnlock()
	return c.remote[k]
}

func (c *Client) feature(name string) bool {
	if f, ok := c.remoteGet("features").(map[string]any); ok {
		if v, ok := f[name].(bool); ok {
			return v
		}
	}
	return true
}

func clamp(v float64) float64 { return math.Max(0, math.Min(1, v)) }

func (c *Client) sampleRate(typ string) float64 {
	remote, _ := c.remoteGet("sample_rates").(map[string]any)
	for _, src := range []map[string]float64{toFloats(remote), c.opts.SampleRates} {
		if v, ok := src[typ]; ok {
			return clamp(v)
		}
		if v, ok := src["api_timing"]; ok && typ == "http_request" {
			return clamp(v)
		}
	}
	return 1
}

func toFloats(m map[string]any) map[string]float64 {
	out := map[string]float64{}
	for k, v := range m {
		if f, ok := v.(float64); ok {
			out[k] = f
		}
	}
	return out
}

func (c *Client) keep(e Event) bool {
	typ := e["type"].(string)
	if typ == "exception" || typ == "api_error" || e["severity"] == "high" {
		return true
	}
	if p, ok := e["payload"].(map[string]any); ok && typ == "http_request" {
		if _, has := p["consumer"]; has {
			return true // API Trust audit trail
		}
	}
	r := c.sampleRate(typ)
	return r >= 1 || rand.Float64() < r
}

func (c *Client) ignoredRoute(route string) bool {
	for _, g := range c.opts.IgnoreRoutes {
		if ok, _ := path.Match(g, route); ok {
			return true
		}
	}
	return false
}

// -- capture ------------------------------------------------------------------------

func (c *Client) context() map[string]any {
	app := map[string]any{"environment": c.opts.Environment, "sdk_name": "codeskop-go", "sdk_version": Version}
	if c.opts.Release != "" {
		app["release"] = c.opts.Release
	}
	if c.framework != "" {
		app["framework"] = c.framework
	}
	return map[string]any{
		"device": map[string]any{"platform": "go", "hostname": c.hostname, "os": runtime.GOOS + " " + runtime.GOARCH, "runtime": runtime.Version()},
		"app":    app,
	}
}

func (c *Client) capture(e Event) {
	if c == nil || !c.enabled {
		return
	}
	select {
	case <-c.stop:
		return
	default:
	}
	if v, ok := c.remoteGet("enabled").(bool); ok && !v {
		return
	}
	typ := e["type"].(string)
	if (typ == "http_request" || typ == "api_timing" || typ == "api_error") && !c.feature("network") {
		return
	}
	if !c.keep(e) {
		return
	}
	if c.opts.BeforeSend != nil {
		if e = c.opts.BeforeSend(e); e == nil {
			return
		}
	}
	c.mu.Lock()
	if len(c.queue) >= c.opts.MaxQueueEvents {
		c.queue = c.queue[1:]
	}
	c.queue = append(c.queue, e)
	full := len(c.queue) >= maxBatch
	c.mu.Unlock()
	if full {
		c.signal()
	}
}

func (c *Client) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// -- sending ------------------------------------------------------------------------

func (c *Client) run() {
	defer close(c.done)
	c.refreshConfig(true)
	close(c.ready)
	ticker := time.NewTicker(c.opts.FlushInterval)
	defer ticker.Stop()
	for {
		force := false
		select {
		case <-c.stop:
			c.drain(true)
			return
		case <-ticker.C:
		case <-c.wake:
			force = true
		}
		c.refreshConfig(false)
		c.drain(force)
	}
}

type result struct {
	done       bool
	retryAfter time.Duration
}

func backoff(attempt int) time.Duration {
	base := math.Min(60, math.Pow(2, float64(attempt)))
	return time.Duration(base * (0.8 + rand.Float64()*0.4) * float64(time.Second))
}

func (c *Client) send(body []byte) result {
	req, err := c.newRequest("POST", c.opts.Endpoint+"/v1/events", body)
	if err != nil {
		return result{done: true}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	res, err := c.http.Do(req)
	if err != nil {
		return result{retryAfter: backoff(c.attempt)}
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return result{done: true}
	case res.StatusCode == 429 || res.StatusCode == 503:
		ra, err := strconv.ParseFloat(res.Header.Get("Retry-After"), 64)
		if err != nil || ra < 0 {
			ra = 5
		}
		return result{retryAfter: time.Duration(ra * float64(time.Second))}
	case res.StatusCode >= 500:
		return result{retryAfter: backoff(c.attempt)}
	}
	log.Printf("codeskop: ingest refused a batch (HTTP %d); dropping it", res.StatusCode)
	return result{done: true}
}

func (c *Client) drain(force bool) {
	for {
		if !force && time.Now().Before(c.nextSend) {
			return
		}
		c.mu.Lock()
		if c.pending == nil {
			if len(c.queue) == 0 {
				c.mu.Unlock()
				return
			}
			n := min(maxBatch, len(c.queue))
			c.pending = append([]Event(nil), c.queue[:n]...)
			c.queue = c.queue[n:]
			c.attempt = 0
		}
		batch := c.pending
		c.mu.Unlock()
		var failure *result
		for _, body := range c.bodies(batch) {
			if r := c.send(body); !r.done {
				failure = &r
				break
			}
		}
		if failure == nil {
			c.mu.Lock()
			c.pending, c.attempt = nil, 0
			c.mu.Unlock()
			continue
		}
		c.attempt++
		c.nextSend = time.Now().Add(failure.retryAfter)
		if force && c.attempt < 3 {
			time.Sleep(min(failure.retryAfter, time.Second))
			continue
		}
		return
	}
}

func (c *Client) bodies(batch []Event) [][]byte {
	events := make([]Event, 0, len(batch))
	for _, e := range batch {
		if b, err := json.Marshal(e); err == nil && len(b) <= maxEventBytes {
			events = append(events, e)
		}
	}
	if len(events) == 0 {
		return nil
	}
	return c.split(events)
}

func (c *Client) split(events []Event) [][]byte {
	raw, _ := json.Marshal(map[string]any{"sent_at": nowISO(), "context": c.context(), "batch": events})
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(raw)
	_ = gz.Close()
	if buf.Len() <= maxBodyBytes || len(events) == 1 {
		return [][]byte{buf.Bytes()}
	}
	mid := len(events) / 2
	return append(c.split(events[:mid]), c.split(events[mid:])...)
}

// Flush blocks until queued events are sent or the timeout passes.
func (c *Client) Flush(timeout time.Duration) bool {
	if c == nil || !c.enabled {
		return true
	}
	c.nextSend = time.Time{}
	deadline := time.Now().Add(timeout)
	c.signal()
	for time.Now().Before(deadline) {
		c.mu.Lock()
		empty := len(c.queue) == 0 && c.pending == nil
		c.mu.Unlock()
		if empty {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// Close flushes and stops the background sender.
func (c *Client) Close(timeout time.Duration) {
	if c == nil || !c.enabled {
		return
	}
	c.closeOnce.Do(func() {
		c.Flush(timeout)
		close(c.stop)
		select {
		case <-c.done:
		case <-time.After(timeout):
		}
	})
}

// -- public API ----------------------------------------------------------------------

type userKey struct{}

// WithUser returns a context whose events are attributed to userID (your own ID, never an email).
// The HTTP integrations put a mutable holder in each request context, so SetUser works too.
func WithUser(ctx context.Context, userID string) context.Context {
	if h, ok := ctx.Value(userKey{}).(*userHolder); ok {
		h.set(userID)
		return ctx
	}
	return context.WithValue(ctx, userKey{}, &userHolder{id: userID})
}

// SetUser attributes the current request's events to userID (inside an integration-wrapped handler).
func SetUser(ctx context.Context, userID string) {
	if h, ok := ctx.Value(userKey{}).(*userHolder); ok {
		h.set(userID)
	}
}

type userHolder struct {
	mu sync.Mutex
	id string
}

func (h *userHolder) set(id string) { h.mu.Lock(); h.id = id; h.mu.Unlock() }
func (h *userHolder) get() string   { h.mu.Lock(); defer h.mu.Unlock(); return h.id }

func userFrom(ctx context.Context) string {
	c := getClient()
	if ctx == nil || c == nil || c.opts.DisableUserID {
		return ""
	}
	if h, ok := ctx.Value(userKey{}).(*userHolder); ok {
		return h.get()
	}
	return ""
}

// CaptureError records an error with the stack of the caller.
func CaptureError(ctx context.Context, err error, tags map[string]string) {
	captureError(ctx, err, 3, true, "manual", nil, tags)
}

func captureError(ctx context.Context, err error, skip int, handled bool, mechanism string, request map[string]any, tags map[string]string) {
	c := getClient()
	if c == nil || !c.enabled || err == nil {
		return
	}
	for _, name := range c.opts.IgnoreErrors {
		if errorClass(err) == name {
			return
		}
	}
	pcs := make([]uintptr, maxFrames)
	n := runtime.Callers(skip, pcs)
	c.capture(exceptionEvent(err, pcs[:n], handled, mechanism, request, userFrom(ctx), tags))
}

// CaptureMessage records a message as an issue.
func CaptureMessage(ctx context.Context, message string) {
	c := getClient()
	if c == nil || !c.enabled {
		return
	}
	if len(message) > maxMessage {
		message = message[:maxMessage]
	}
	c.capture(event("exception", "medium", map[string]any{"exception_class": "Message", "message": message,
		"stacktrace": []map[string]any{}, "handled": true, "mechanism": "message"}, userFrom(ctx)))
}

// Recover reports a panic in the current goroutine and re-panics: `defer codeskop.Recover(ctx)`.
func Recover(ctx context.Context) {
	if r := recover(); r != nil {
		capturePanic(ctx, r, "recover", nil)
		Flush(2 * time.Second)
		panic(r)
	}
}

func capturePanic(ctx context.Context, r any, mechanism string, request map[string]any) {
	err, ok := r.(error)
	if !ok {
		err = fmt.Errorf("%v", r)
	}
	if errors.Is(err, http.ErrAbortHandler) {
		return
	}
	captureError(ctx, &panicError{err}, 5, false, mechanism, request, nil)
}

type panicError struct{ err error }

func (p *panicError) Error() string { return p.err.Error() }
func (p *panicError) Unwrap() error { return p.err }

// Flush sends everything queued, waiting up to timeout.
func Flush(timeout time.Duration) bool { return getClient().Flush(timeout) }

// Close flushes and stops the SDK; call it (or defer it) before main returns.
func Close(timeout time.Duration) { getClient().Close(timeout) }
