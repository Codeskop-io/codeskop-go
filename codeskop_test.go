package codeskop

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Codeskop-io/codeskop-go/internal/mockingest"
)

func start(t *testing.T, ingest *mockingest.Server, o Options) *Client {
	t.Helper()
	o.APIKey, o.Endpoint, o.FlushInterval = mockingest.Key, ingest.URL, 50*time.Millisecond
	c := Init(o)
	c.Ready(2 * time.Second)
	t.Cleanup(func() { Close(500 * time.Millisecond) })
	return c
}

func payload(e map[string]any) map[string]any { return mockingest.Payload(e) }

type orderError struct{ id int }

func (e *orderError) Error() string { return fmt.Sprintf("order %d total can't be negative", e.id) }

func boom() error { return &orderError{42} }

func TestSecretKeyDisables(t *testing.T) {
	ingest := mockingest.New()
	defer ingest.Close()
	c := Init(Options{APIKey: "cs_live_sk_supersecret123", Endpoint: ingest.URL})
	if c.enabled {
		t.Fatal("secret key should disable the SDK")
	}
	CaptureMessage(context.Background(), "ignored")
	if !Flush(200 * time.Millisecond) {
		t.Fatal("flush on a disabled client must succeed")
	}
	if len(ingest.Events()) != 0 {
		t.Fatal("nothing should be sent")
	}
}

func TestErrorWithStackCauseAndContext(t *testing.T) {
	ingest := mockingest.New()
	defer ingest.Close()
	start(t, ingest, Options{Release: "1.4.2", Environment: "test"})
	CaptureError(context.Background(), fmt.Errorf("checkout failed: %w", boom()), map[string]string{"area": "checkout"})
	if !Flush(3 * time.Second) {
		t.Fatal("flush timed out")
	}
	e := ingest.OfType("exception")
	if len(e) != 1 {
		t.Fatalf("want 1 exception, got %d (%v)", len(e), ingest.Errors)
	}
	p := payload(e[0])
	if p["exception_class"] != "error" || p["message"] != "checkout failed: order 42 total can't be negative" {
		t.Fatalf("payload %v", p)
	}
	frames := p["stacktrace"].([]any)
	top := frames[0].(map[string]any)
	if top["method"] != "TestErrorWithStackCauseAndContext" || top["in_app"] != true {
		t.Fatalf("top frame %v", top)
	}
	if cause := p["cause"].(map[string]any); cause["exception_class"] != "*codeskop.orderError" {
		t.Fatalf("cause %v", cause)
	}
	ctx := ingest.Batches[0]["context"].(map[string]any)
	if ctx["device"].(map[string]any)["platform"] != "go" || ctx["app"].(map[string]any)["release"] != "1.4.2" {
		t.Fatalf("context %v", ctx)
	}
}

func TestRetryThenDeliver(t *testing.T) {
	ingest := mockingest.New()
	defer ingest.Close()
	start(t, ingest, Options{})
	ingest.Mu.Lock()
	ingest.Responses = [][2]int{{429, 0}, {503, 0}}
	ingest.Mu.Unlock()
	CaptureMessage(context.Background(), "eventually delivered")
	if !Flush(5 * time.Second) {
		t.Fatal("flush timed out")
	}
	if n := len(ingest.OfType("exception")); n != 1 {
		t.Fatalf("want 1, got %d", n)
	}
	posts := 0
	for _, r := range ingest.Requests {
		if strings.HasPrefix(r, "POST") {
			posts++
		}
	}
	if posts != 3 {
		t.Fatalf("want 3 posts, got %d", posts)
	}
}

func TestBatchesOfAtMost100(t *testing.T) {
	ingest := mockingest.New()
	defer ingest.Close()
	start(t, ingest, Options{})
	for i := 0; i < 250; i++ {
		CaptureMessage(context.Background(), fmt.Sprintf("m%d", i))
	}
	if !Flush(5 * time.Second) {
		t.Fatal("flush timed out")
	}
	total := 0
	for _, b := range ingest.Batches {
		n := len(b["batch"].([]any))
		if n > 100 {
			t.Fatalf("batch of %d", n)
		}
		total += n
	}
	if total != 250 {
		t.Fatalf("want 250, got %d", total)
	}
}

func TestKillSwitchAndSampling(t *testing.T) {
	ingest := mockingest.New()
	defer ingest.Close()
	ingest.Config = map[string]any{"enabled": true, "features": map[string]any{"network": true}, "sample_rates": map[string]any{"http_request": 0.0}}
	start(t, ingest, Options{DisableOutgoing: true})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ok", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /broken", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	srv := httptest.NewServer(Middleware(mux))
	defer srv.Close()
	_, _ = http.Get(srv.URL + "/ok")
	_, _ = http.Get(srv.URL + "/broken")
	Flush(3 * time.Second)
	reqs := ingest.OfType("http_request")
	if len(reqs) != 1 || payload(reqs[0])["route"] != "/broken" {
		t.Fatalf("failures must never be sampled out: %v", reqs)
	}
}

func TestNetHTTPMiddlewareRoutesUsersPanics(t *testing.T) {
	ingest := mockingest.New()
	defer ingest.Close()
	start(t, ingest, Options{DisableOutgoing: true})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		SetUser(r.Context(), "42")
		_, _ = w.Write([]byte(r.PathValue("id")))
	})
	mux.HandleFunc("GET /boom", func(w http.ResponseWriter, r *http.Request) { panic("handler exploded") })
	srv := httptest.NewServer(Middleware(mux))
	defer srv.Close()
	for _, p := range []string{"/orders/7", "/orders/8", "/boom", "/healthz"} {
		res, err := http.Get(srv.URL + p)
		if err == nil {
			res.Body.Close()
		}
	}
	Flush(3 * time.Second)
	var got []string
	for _, e := range ingest.OfType("http_request") {
		got = append(got, fmt.Sprintf("%v %v", payload(e)["route"], payload(e)["status"]))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "/boom 500,/orders/{id} 200,/orders/{id} 200" {
		t.Fatalf("routes %v", got)
	}
	for _, e := range ingest.OfType("http_request") {
		if payload(e)["status"] == float64(200) && e["user"].(map[string]any)["id"] != "42" {
			t.Fatalf("user %v", e["user"])
		}
	}
	exc := ingest.OfType("exception")
	if len(exc) != 1 || payload(exc[0])["exception_class"] != "panic" || payload(exc[0])["mechanism"] != "net/http" {
		t.Fatalf("panic %v", exc)
	}
}

func TestOutgoingTransport(t *testing.T) {
	ingest := mockingest.New()
	defer ingest.Close()
	upstream := mockingest.New()
	defer upstream.Close()
	upstream.Routes["/v1/charges/err"] = [2]any{502, map[string]any{"detail": "bad gateway"}}
	start(t, ingest, Options{})
	client := &http.Client{Transport: Transport(nil)}
	target := strings.Replace(upstream.URL, "127.0.0.1", "localhost", 1)
	for _, p := range []string{"/v1/customers/123?expand=1", "/v1/charges/err"} {
		if res, err := client.Get(target + p); err == nil {
			res.Body.Close()
		}
	}
	_, _ = client.Get("http://localhost:1/unreachable")
	Flush(3 * time.Second)
	var got []string
	for _, e := range ingest.OfType("api_timing") {
		got = append(got, fmt.Sprintf("%v %v", payload(e)["path"], payload(e)["status"]))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "/unreachable <nil>,/v1/charges/err 502,/v1/customers/{id} 200" {
		t.Fatalf("timings %v", got)
	}
	var kinds []string
	for _, e := range ingest.OfType("api_error") {
		kinds = append(kinds, fmt.Sprint(payload(e)["error_kind"]))
	}
	sort.Strings(kinds)
	if strings.Join(kinds, ",") != "http_5xx,network_error" {
		t.Fatalf("errors %v", kinds)
	}
}

func TestRouteHelpers(t *testing.T) {
	cases := map[string]string{"GET /orders/{id}": "/orders/{id}", "/users/:id": "/users/{id}", "/files/{path...}": "/files/{path}", "example.com/x/{id}": "/x/{id}"}
	for in, want := range cases {
		if got := TemplateRoute(in); got != want {
			t.Errorf("TemplateRoute(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NormalizePath("/users/42/files/0f8fad5b-d9cb-469f-a165-70867728950e?x=1"); got != "/users/{id}/files/{id}" {
		t.Errorf("NormalizePath = %q", got)
	}
}

// -- API Trust -----------------------------------------------------------------------

const salt = "ssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssssss"

func h(raw string) string {
	m := hmac.New(sha256.New, []byte(salt))
	m.Write([]byte(raw))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

func trustConfig(blocking bool) map[string]any {
	return map[string]any{"enabled": true, "features": map[string]any{"network": true}, "sample_rates": map[string]any{"http_request": 0.0},
		"api_trust": map[string]any{"enabled": true, "salt": salt, "trust_proxy": true, "blocking": blocking, "verdicts_path": "/v1/trust/verdicts",
			"consumer_sources": []any{map[string]any{"type": "header", "name": "X-API-Key"},
				map[string]any{"type": "jwt", "header": "Authorization", "claims": []any{"client_id", "sub"}},
				map[string]any{"type": "mtls", "header": "X-Client-Cert"}, map[string]any{"type": "query", "name": "api_key"}}}}
}

func TestTrustCaptureAndBlocking(t *testing.T) {
	ingest := mockingest.New()
	defer ingest.Close()
	ingest.Config = trustConfig(true)
	ingest.Routes["/v1/trust/verdicts"] = [2]any{200, map[string]any{"blocked": []string{h("banned-key")}}}
	start(t, ingest, Options{DisableOutgoing: true})
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/charges", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	srv := httptest.NewServer(Middleware(mux))
	defer srv.Close()
	jwt := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"client_id":"partner-42"}`)) + ".sig"
	do := func(path string, headers map[string]string) int {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	do("/v1/charges", map[string]string{"X-API-Key": "live_secret_123", "X-Forwarded-For": "203.0.113.7, 10.0.0.1", "Origin": "https://shop.example.com"})
	do("/v1/charges", map[string]string{"Authorization": "Bearer " + jwt})
	do("/v1/charges", map[string]string{"X-Client-Cert": "sha256:ab12"})
	do("/v1/charges?api_key=qkey", nil)
	if code := do("/v1/charges", map[string]string{"X-API-Key": "banned-key"}); code != 403 {
		t.Fatalf("blocked consumer got %d", code)
	}
	do("/v1/charges", nil) // no consumer: sampled out
	Flush(3 * time.Second)
	var got []string
	for _, e := range ingest.OfType("http_request") {
		c := payload(e)["consumer"].(map[string]any)
		s := fmt.Sprintf("%v:%v", c["auth_type"], c["id_hash"])
		if payload(e)["blocked"] == true {
			s += ":blocked"
		}
		got = append(got, s)
	}
	want := []string{"api_key:" + h("live_secret_123"), "jwt:" + h("partner-42"), "mtls:" + h("sha256:ab12"), "api_key:" + h("qkey"), "api_key:" + h("banned-key") + ":blocked"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("consumers\n got %v\nwant %v", got, want)
	}
	first := payload(ingest.OfType("http_request")[0])["client"].(map[string]any)
	if first["ip"] != "203.0.113.7" || first["origin"] != "https://shop.example.com" {
		t.Fatalf("client %v", first)
	}
	for _, e := range ingest.Events() {
		if strings.Contains(fmt.Sprint(e), "live_secret_123") {
			t.Fatal("raw credential leaked")
		}
	}
}

func TestPanicErrorClass(t *testing.T) {
	if c := errorClass(&panicError{errors.New("x")}); c != "panic" {
		t.Fatalf("got %s", c)
	}
	if c := errorClass(&panicError{&orderError{1}}); c != "*codeskop.orderError" {
		t.Fatalf("got %s", c)
	}
}
