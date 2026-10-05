package codeskop

import (
	"crypto/rand"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const (
	maxMessage = 2048
	maxFrames  = 100
)

// Event is one event in the ingest envelope (docs/10 §10.6).
type Event map[string]any

var (
	reNumeric = regexp.MustCompile(`^\d+$`)
	reUUID    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	reHex     = regexp.MustCompile(`^[0-9a-fA-F]{16,}$`)
	reColon   = regexp.MustCompile(`:([A-Za-z0-9_]+)`)
	reWild    = regexp.MustCompile(`\{([A-Za-z0-9_]+)(\.\.\.)?\}`)
	reMethod  = regexp.MustCompile(`^[A-Z]+ `)
	goroot    = runtime.GOROOT()
)

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// NormalizePath applies the server's templating: no query; numeric, UUID and long-hex segments become {id}.
func NormalizePath(path string) string {
	if path == "" {
		path = "/"
	}
	path = strings.SplitN(strings.SplitN(path, "?", 2)[0], "#", 2)[0]
	segs := strings.Split(path, "/")
	for i, s := range segs {
		if s != "" && (reNumeric.MatchString(s) || reUUID.MatchString(s) || reHex.MatchString(s)) {
			segs[i] = "{id}"
		}
	}
	out := strings.Join(segs, "/")
	if !strings.HasPrefix(out, "/") {
		out = "/" + out
	}
	return out
}

// TemplateRoute turns a router pattern into our {name} form: "GET /users/{id}", "/users/:id", "/files/{path...}".
func TemplateRoute(route string) string {
	if route == "" {
		return "/"
	}
	route = reMethod.ReplaceAllString(route, "")
	route = reColon.ReplaceAllString(route, "{$1}")
	route = reWild.ReplaceAllString(route, "{$1}")
	if i := strings.Index(route, " "); i >= 0 { // "host/path" patterns keep only the path
		route = route[i+1:]
	}
	if !strings.HasPrefix(route, "/") {
		if i := strings.Index(route, "/"); i >= 0 {
			route = route[i:]
		} else {
			route = "/" + route
		}
	}
	return route
}

func inApp(file, function string) bool {
	if file == "" || strings.HasPrefix(function, "runtime.") || strings.HasPrefix(function, "testing.") {
		return false
	}
	if goroot != "" && strings.HasPrefix(file, goroot) {
		return false
	}
	if strings.Contains(file, "/pkg/mod/") || strings.Contains(file, "/vendor/") {
		return false
	}
	if strings.Contains(function, "github.com/Codeskop-io/codeskop-go") && !strings.HasSuffix(file, "_test.go") {
		return false
	}
	return true
}

func framesFrom(pcs []uintptr) []map[string]any {
	out := []map[string]any{}
	frames := runtime.CallersFrames(pcs)
	for {
		f, more := frames.Next()
		pkg, fn := splitFunction(f.Function)
		out = append(out, map[string]any{"class": pkg, "method": fn, "file": shortFile(f.File), "line": f.Line, "in_app": inApp(f.File, f.Function)})
		if !more || len(out) >= maxFrames {
			break
		}
	}
	return out // innermost first
}

// splitFunction: "github.com/acme/shop/orders.(*Svc).Create" → ("github.com/acme/shop/orders.(*Svc)", "Create").
func splitFunction(full string) (string, string) {
	slash := strings.LastIndex(full, "/")
	dot := strings.LastIndex(full[slash+1:], ".")
	if dot < 0 {
		return full, ""
	}
	return full[:slash+1+dot], full[slash+2+dot:]
}

func shortFile(file string) string {
	if i := strings.Index(file, "/pkg/mod/"); i >= 0 {
		return file[i+9:]
	}
	return file
}

func errorClass(err error) string {
	if err == nil {
		return "error"
	}
	if pe, ok := err.(*panicError); ok {
		if c := errorClass(pe.err); c != "error" {
			return c
		}
		return "panic"
	}
	t := reflect.TypeOf(err)
	name := t.String()
	if name == "*errors.errorString" || name == "*fmt.wrapError" || name == "*fmt.wrapErrors" {
		return "error"
	}
	return name
}

func exceptionPayload(err error, pcs []uintptr, depth int) map[string]any {
	msg := err.Error()
	if len(msg) > maxMessage {
		msg = msg[:maxMessage]
	}
	p := map[string]any{"exception_class": errorClass(err), "message": msg, "stacktrace": []map[string]any{}}
	if pcs != nil {
		p["stacktrace"] = framesFrom(pcs)
	}
	cause := errors.Unwrap(err)
	if pe, ok := err.(*panicError); ok {
		cause = errors.Unwrap(pe.err)
	}
	if cause != nil && depth < 3 {
		p["cause"] = exceptionPayload(cause, nil, depth+1)
	}
	return p
}

func event(typ, severity string, payload map[string]any, userID string) Event {
	e := Event{"event_id": newID(), "type": typ, "severity": severity, "occurred_at": nowISO(), "payload": payload}
	if userID != "" {
		if len(userID) > 128 {
			userID = userID[:128]
		}
		e["user"] = map[string]any{"id": userID}
	}
	return e
}

func exceptionEvent(err error, pcs []uintptr, handled bool, mechanism string, request map[string]any, userID string, tags map[string]string) Event {
	p := exceptionPayload(err, pcs, 0)
	p["handled"] = handled
	p["mechanism"] = mechanism
	if request != nil {
		p["request"] = request
	}
	if len(tags) > 0 {
		p["tags"] = tags
	}
	sev := "medium"
	if !handled {
		sev = "high"
	}
	return event("exception", sev, p, userID)
}

func requestEvent(method, route string, status int, d time.Duration, reqBytes, resBytes int64, requestID string, failed bool, userID string, extra map[string]any) Event {
	p := map[string]any{"method": strings.ToUpper(method), "route": route, "status": status, "duration_ms": float64(d.Microseconds()) / 1000}
	if reqBytes >= 0 {
		p["request_bytes"] = reqBytes
	}
	if resBytes >= 0 {
		p["response_bytes"] = resBytes
	}
	if requestID != "" {
		p["request_id"] = requestID
	}
	for k, v := range extra {
		p[k] = v
	}
	sev := "low"
	if failed || status >= 500 {
		sev = "high"
	}
	return event("http_request", sev, p, userID)
}

func outgoingEvents(method, host, path string, status int, d time.Duration, errorKind, userID string) []Event {
	p := map[string]any{"method": strings.ToUpper(method), "host": host, "path": NormalizePath(path), "duration_ms": float64(d.Microseconds()) / 1000}
	if status > 0 {
		p["status"] = status
	}
	if errorKind == "" && status >= 400 {
		errorKind = "http_4xx"
		if status >= 500 {
			errorKind = "http_5xx"
		}
	}
	timing := map[string]any{}
	for k, v := range p {
		timing[k] = v
	}
	out := []Event{event("api_timing", "low", timing, userID)}
	if errorKind != "" {
		p["error_kind"] = errorKind
		out = append(out, event("api_error", "high", p, userID))
	}
	return out
}
