package codeskopgin

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	codeskop "github.com/Codeskop-io/codeskop-go"
	"github.com/Codeskop-io/codeskop-go/internal/mockingest"
	"github.com/gin-gonic/gin"
)

func TestGin(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	ingest := mockingest.New()
	defer ingest.Close()
	c := codeskop.Init(codeskop.Options{APIKey: mockingest.Key, Endpoint: ingest.URL, FlushInterval: 50 * time.Millisecond, DisableOutgoing: true})
	c.Ready(2 * time.Second)
	defer codeskop.Close(time.Second)
	r := gin.New()
	r.Use(Middleware())
	r.GET("/users/:id", func(c *gin.Context) { codeskop.SetUser(c.Request.Context(), "u1"); c.String(200, c.Param("id")) })
	r.GET("/panic", func(c *gin.Context) { panic(errors.New("gin exploded")) })
	r.GET("/fail", func(c *gin.Context) { _ = c.Error(errors.New("db down")); c.Status(500) })
	for _, p := range []string{"/users/1", "/panic", "/fail"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
	}
	codeskop.Flush(3 * time.Second)
	var got []string
	for _, e := range ingest.OfType("http_request") {
		got = append(got, fmt.Sprintf("%v %v", mockingest.Payload(e)["route"], mockingest.Payload(e)["status"]))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "/fail 500,/panic 500,/users/{id} 200" {
		t.Fatalf("routes %v", got)
	}
	if n := len(ingest.OfType("exception")); n != 2 {
		t.Fatalf("want 2 exceptions (panic + c.Error), got %d", n)
	}
}
