package codeskopecho

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
	"github.com/labstack/echo/v4"
)

func TestEcho(t *testing.T) {
	ingest := mockingest.New()
	defer ingest.Close()
	c := codeskop.Init(codeskop.Options{APIKey: mockingest.Key, Endpoint: ingest.URL, FlushInterval: 50 * time.Millisecond, DisableOutgoing: true})
	c.Ready(2 * time.Second)
	defer codeskop.Close(time.Second)
	e := echo.New()
	e.Use(Middleware())
	e.GET("/items/:itemId", func(c echo.Context) error { return c.String(200, c.Param("itemId")) })
	e.GET("/err", func(c echo.Context) error { return errors.New("handler failed") })
	e.GET("/panic", func(c echo.Context) error { panic("echo exploded") })
	e.GET("/missing", func(c echo.Context) error { return echo.NewHTTPError(404) })
	for _, p := range []string{"/items/3", "/err", "/panic", "/missing"} {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
	}
	codeskop.Flush(3 * time.Second)
	var got []string
	for _, ev := range ingest.OfType("http_request") {
		got = append(got, fmt.Sprintf("%v %v", mockingest.Payload(ev)["route"], mockingest.Payload(ev)["status"]))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "/err 500,/items/{itemId} 200,/missing 404,/panic 500" {
		t.Fatalf("routes %v", got)
	}
	if n := len(ingest.OfType("exception")); n != 2 {
		t.Fatalf("want 2 exceptions (error + panic, not the 404), got %d", n)
	}
}
