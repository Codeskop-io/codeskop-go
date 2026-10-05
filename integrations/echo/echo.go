// Package codeskopecho integrates Codeskop with Echo: e.Use(codeskopecho.Middleware()).
package codeskopecho

import (
	"net/http"

	codeskop "github.com/Codeskop-io/codeskop-go"
	"github.com/labstack/echo/v4"
)

// Middleware records each request by route (/users/{id}), reports panics and returned errors,
// and handles API Trust.
func Middleware() echo.MiddlewareFunc {
	codeskop.SetFramework("echo")
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) (err error) {
			req := c.Request()
			rec, ctx := codeskop.StartRequest(req)
			c.SetRequest(req.WithContext(ctx))
			if rec.Trust(c.Request()) {
				rec.Finish(codeskop.NormalizePath(req.URL.Path), http.StatusForbidden, req.ContentLength, int64(len(codeskop.BlockedBody)))
				return c.Blob(http.StatusForbidden, "application/json", []byte(codeskop.BlockedBody))
			}
			defer func() {
				route := routeOf(c)
				panicked := false
				if p := recover(); p != nil {
					rec.Panic(ctx, p, route, "echo")
					err = echo.NewHTTPError(http.StatusInternalServerError)
					panicked = true
				}
				status := c.Response().Status
				if err != nil {
					status = http.StatusInternalServerError
					if he, ok := err.(*echo.HTTPError); ok {
						status = he.Code
					}
					if status >= 500 && !panicked {
						rec.Error(ctx, err, route, "echo")
					}
				}
				rec.Finish(route, status, req.ContentLength, c.Response().Size)
			}()
			return next(c)
		}
	}
}

func routeOf(c echo.Context) string {
	if p := c.Path(); p != "" {
		return codeskop.TemplateRoute(p)
	}
	return codeskop.NormalizePath(c.Request().URL.Path)
}
