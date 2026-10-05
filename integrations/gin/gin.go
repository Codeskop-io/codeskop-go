// Package codeskopgin integrates Codeskop with Gin: router.Use(codeskopgin.Middleware()).
package codeskopgin

import (
	"net/http"

	codeskop "github.com/Codeskop-io/codeskop-go"
	"github.com/gin-gonic/gin"
)

// Middleware records each request by route (/users/{id}), reports panics (answering 500) and
// errors added with c.Error, and handles API Trust. Use it instead of gin.Recovery().
func Middleware() gin.HandlerFunc {
	codeskop.SetFramework("gin")
	return func(c *gin.Context) {
		rec, ctx := codeskop.StartRequest(c.Request)
		c.Request = c.Request.WithContext(ctx)
		if rec.Trust(c.Request) {
			c.Data(http.StatusForbidden, "application/json", []byte(codeskop.BlockedBody))
			c.Abort()
			rec.Finish(codeskop.NormalizePath(c.Request.URL.Path), http.StatusForbidden, c.Request.ContentLength, int64(len(codeskop.BlockedBody)))
			return
		}
		defer func() {
			route := routeOf(c)
			if p := recover(); p != nil {
				rec.Panic(ctx, p, route, "gin")
				c.AbortWithStatus(http.StatusInternalServerError)
			}
			for _, e := range c.Errors {
				rec.Error(ctx, e.Err, route, "gin")
			}
			rec.Finish(route, c.Writer.Status(), c.Request.ContentLength, int64(c.Writer.Size()))
		}()
		c.Next()
	}
}

func routeOf(c *gin.Context) string {
	if p := c.FullPath(); p != "" {
		return codeskop.TemplateRoute(p)
	}
	return codeskop.NormalizePath(c.Request.URL.Path)
}
