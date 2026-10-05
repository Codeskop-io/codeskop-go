package codeskop

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// API Trust capture (docs/10 §10.9), active only when remote config contains api_trust.
// Consumer credentials are HMAC-hashed here; raw values never leave your server.
// Blocking is opt-in and fails open.

const verdictRefresh = time.Minute

type trust struct {
	c          *Client
	mu         sync.RWMutex
	active     bool
	sources    []map[string]any
	salt       string
	trustProxy bool
	blocking   bool
	path       string
	blocked    map[string]bool
	etag       string
	fetchedAt  time.Time
	refreshing bool
}

func newTrust(c *Client) *trust { return &trust{c: c, blocked: map[string]bool{}} }

func (t *trust) configure(cfg map[string]any) {
	t.mu.Lock()
	t.salt, _ = cfg["salt"].(string)
	t.active = cfg["enabled"] == true && t.salt != ""
	t.sources = nil
	if src, ok := cfg["consumer_sources"].([]any); ok {
		for _, s := range src {
			if m, ok := s.(map[string]any); ok {
				t.sources = append(t.sources, m)
			}
		}
	}
	t.trustProxy = cfg["trust_proxy"] != false
	if t.c.opts.TrustProxy != nil {
		t.trustProxy = *t.c.opts.TrustProxy
	}
	t.blocking = cfg["blocking"] == true
	t.path, _ = cfg["verdicts_path"].(string)
	if t.path == "" {
		t.path = "/v1/trust/verdicts"
	}
	needFetch := t.blocking && t.fetchedAt.IsZero()
	if !t.blocking {
		t.blocked = map[string]bool{}
	}
	t.mu.Unlock()
	if needFetch {
		t.fetchVerdicts()
	}
}

func (t *trust) hash(raw string) string {
	m := hmac.New(sha256.New, []byte(t.salt))
	m.Write([]byte(raw))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

func (t *trust) consumer(r *http.Request) map[string]any {
	if res := t.c.opts.APITrustResolver; res != nil {
		if raw := res(r); raw != "" {
			return map[string]any{"id_hash": t.hash(raw), "auth_type": "custom", "source": "resolver"}
		}
	}
	for _, s := range t.sources {
		typ, _ := s["type"].(string)
		var raw, auth, label = "", "api_key", typ
		switch typ {
		case "header":
			name, _ := s["name"].(string)
			raw = r.Header.Get(name)
			if scheme, _ := s["scheme"].(string); scheme != "" && raw != "" {
				if strings.HasPrefix(strings.ToLower(raw), strings.ToLower(scheme)+" ") {
					raw = raw[len(scheme)+1:]
				} else {
					raw = ""
				}
			}
			label = "header:" + name
		case "query":
			name, _ := s["name"].(string)
			raw = r.URL.Query().Get(name)
			label = "query:" + name
		case "jwt":
			header, _ := s["header"].(string)
			if header == "" {
				header = "Authorization"
			}
			claims := []string{"sub"}
			if cl, ok := s["claims"].([]any); ok {
				claims = claims[:0]
				for _, c := range cl {
					if cs, ok := c.(string); ok {
						claims = append(claims, cs)
					}
				}
			}
			raw, auth, label = jwtClaim(r.Header.Get(header), claims), "jwt", "jwt"
		case "mtls":
			header, _ := s["header"].(string)
			raw, auth, label = r.Header.Get(header), "mtls", "mtls:"+header
			if raw == "" && r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
				sum := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
				raw = hex.EncodeToString(sum[:])
			}
		}
		if raw = strings.TrimSpace(raw); raw != "" {
			if len(label) > 80 {
				label = label[:80]
			}
			return map[string]any{"id_hash": t.hash(raw), "auth_type": auth, "source": label}
		}
	}
	return nil
}

func jwtClaim(header string, claims []string) string {
	token := header
	if strings.HasPrefix(strings.ToLower(header), "bearer ") {
		token = header[7:]
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var payload map[string]any
	if json.Unmarshal(b, &payload) != nil {
		return ""
	}
	for _, c := range claims {
		if v, ok := payload[c]; ok && v != nil && v != "" {
			if s, ok := v.(string); ok {
				return s
			}
			out, _ := json.Marshal(v)
			return string(out)
		}
	}
	return ""
}

func (t *trust) clientIP(r *http.Request) string {
	if t.trustProxy {
		if f := r.Header.Get("X-Forwarded-For"); f != "" {
			return strings.TrimSpace(strings.Split(f, ",")[0])
		}
		if f := r.Header.Get("X-Real-Ip"); f != "" {
			return strings.TrimSpace(f)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// inspect returns extra payload fields for this request and whether it must be blocked.
func (t *trust) inspect(r *http.Request) (map[string]any, bool) {
	t.mu.RLock()
	active := t.active
	t.mu.RUnlock()
	if !active {
		return nil, false
	}
	extra := map[string]any{}
	consumer := t.consumer(r)
	if consumer != nil {
		extra["consumer"] = consumer
	}
	client := map[string]any{}
	add := func(k, v string, max int) {
		if v != "" {
			if len(v) > max {
				v = v[:max]
			}
			client[k] = v
		}
	}
	add("ip", t.clientIP(r), 64)
	add("user_agent", r.UserAgent(), 300)
	add("origin", r.Header.Get("Origin"), 300)
	add("referer", r.Referer(), 300)
	add("requested_with", r.Header.Get("X-Requested-With"), 200)
	if len(client) > 0 {
		extra["client"] = client
	}
	return extra, consumer != nil && t.isBlocked(consumer["id_hash"].(string))
}

func (t *trust) isBlocked(hash string) bool {
	t.mu.Lock()
	if !t.blocking {
		t.mu.Unlock()
		return false
	}
	stale := time.Since(t.fetchedAt) > verdictRefresh && !t.refreshing
	if stale {
		t.refreshing = true
	}
	blocked := t.blocked[hash]
	t.mu.Unlock()
	if stale {
		go t.fetchVerdicts()
	}
	return blocked
}

func (t *trust) fetchVerdicts() {
	t.mu.Lock()
	t.fetchedAt = time.Now()
	t.refreshing = true
	etag, path := t.etag, t.path
	t.mu.Unlock()
	defer func() { t.mu.Lock(); t.refreshing = false; t.mu.Unlock() }()
	req, err := t.c.newRequest("GET", t.c.opts.Endpoint+path, nil)
	if err != nil {
		return
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	res, err := t.c.http.Do(req)
	if err != nil {
		return // fail open
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return
	}
	var body struct {
		Blocked []string `json:"blocked"`
	}
	if json.NewDecoder(res.Body).Decode(&body) != nil {
		return
	}
	set := map[string]bool{}
	for _, h := range body.Blocked {
		set[h] = true
	}
	t.mu.Lock()
	t.blocked, t.etag = set, res.Header.Get("ETag")
	t.mu.Unlock()
}
