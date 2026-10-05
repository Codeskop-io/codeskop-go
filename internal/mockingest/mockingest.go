// Package mockingest is a mock Codeskop ingest API for tests (docs/10 §10.10).
package mockingest

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Key is the public key the mock accepts.
const Key = "cs_test_pk_abcdefgh12345678"

var types = map[string]bool{"api_error": true, "api_timing": true, "exception": true, "crash": true, "http_request": true}

// Server records accepted envelopes.
type Server struct {
	*httptest.Server
	Mu        sync.Mutex
	Batches   []map[string]any
	Requests  []string
	Responses [][2]int // {status, retryAfter}
	Config    map[string]any
	Routes    map[string][2]any // prefix -> {status, body}
	Errors    []string
}

// New starts a mock server.
func New() *Server {
	s := &Server{Config: map[string]any{"enabled": true, "features": map[string]any{"network": true}}, Routes: map[string][2]any{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *Server) json(w http.ResponseWriter, status int, body any, headers map[string]string) {
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.Mu.Lock()
	s.Requests = append(s.Requests, r.Method+" "+r.URL.RequestURI())
	cfg, routes := s.Config, s.Routes
	var scripted *[2]int
	if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/events") && len(s.Responses) > 0 {
		x := s.Responses[0]
		s.Responses = s.Responses[1:]
		scripted = &x
	}
	s.Mu.Unlock()
	if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1/config") {
		s.json(w, 200, cfg, map[string]string{"ETag": `"v1"`})
		return
	}
	for prefix, v := range routes {
		if strings.HasPrefix(r.URL.Path, prefix) {
			s.json(w, v[0].(int), v[1], nil)
			return
		}
	}
	if r.Method != "POST" || !strings.HasPrefix(r.URL.Path, "/v1/events") {
		s.json(w, 200, map[string]any{"ok": true}, nil)
		return
	}
	if scripted != nil && scripted[0] != 200 {
		s.json(w, scripted[0], map[string]any{"detail": "scripted"}, map[string]string{"Retry-After": fmt.Sprint(scripted[1])})
		return
	}
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			s.json(w, 400, nil, nil)
			return
		}
		body = gz
	}
	var env map[string]any
	if err := json.NewDecoder(body).Decode(&env); err != nil {
		s.json(w, 400, nil, nil)
		return
	}
	if err := validate(env, r); err != nil {
		s.Mu.Lock()
		s.Errors = append(s.Errors, err.Error())
		s.Mu.Unlock()
		s.json(w, 400, map[string]any{"errors": err.Error()}, nil)
		return
	}
	s.Mu.Lock()
	s.Batches = append(s.Batches, env)
	s.Mu.Unlock()
	s.json(w, 200, map[string]any{}, nil)
}

func validate(env map[string]any, r *http.Request) error {
	if r.Header.Get("Authorization") != "Bearer "+Key {
		return fmt.Errorf("auth")
	}
	if !strings.HasPrefix(r.Header.Get("User-Agent"), "codeskop-go/") {
		return fmt.Errorf("user agent")
	}
	batch, ok := env["batch"].([]any)
	if !ok || len(batch) == 0 || len(batch) > 100 {
		return fmt.Errorf("batch size")
	}
	for _, x := range batch {
		e := x.(map[string]any)
		if !types[fmt.Sprint(e["type"])] {
			return fmt.Errorf("type %v", e["type"])
		}
		if !strings.HasSuffix(fmt.Sprint(e["occurred_at"]), "Z") {
			return fmt.Errorf("occurred_at")
		}
	}
	return nil
}

// Events returns all accepted events.
func (s *Server) Events() []map[string]any {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	var out []map[string]any
	for _, b := range s.Batches {
		for _, e := range b["batch"].([]any) {
			out = append(out, e.(map[string]any))
		}
	}
	return out
}

// OfType returns accepted events of one type.
func (s *Server) OfType(t string) []map[string]any {
	var out []map[string]any
	for _, e := range s.Events() {
		if e["type"] == t {
			out = append(out, e)
		}
	}
	return out
}

// Payload of an event.
func Payload(e map[string]any) map[string]any { return e["payload"].(map[string]any) }
