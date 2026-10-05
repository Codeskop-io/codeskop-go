# Codeskop for Go

Errors, panics, incoming requests and outgoing HTTP calls from your Go services, in Codeskop. Go 1.23+, standard library only (Gin and Echo integrations are separate packages).

```bash
go get github.com/Codeskop-io/codeskop-go@v0.1.0   # or @latest
```

```go
codeskop.Init(codeskop.Options{APIKey: os.Getenv("CODESKOP_API_KEY")}) // cs_live_pk_… (public key)
defer codeskop.Close(2 * time.Second)
```

## net/http

```go
mux := http.NewServeMux()
mux.HandleFunc("GET /orders/{id}", getOrder)
http.ListenAndServe(":8080", codeskop.Middleware(mux)) // routes, panics (answered with 500), API Trust
```

## Gin and Echo

```go
r.Use(codeskopgin.Middleware())   // github.com/Codeskop-io/codeskop-go/integrations/gin (use instead of gin.Recovery())
e.Use(codeskopecho.Middleware())  // github.com/Codeskop-io/codeskop-go/integrations/echo
```

## Errors, users and outgoing calls

```go
codeskop.CaptureError(ctx, err, map[string]string{"provider": "stripe"})
codeskop.SetUser(r.Context(), user.ID)            // inside a wrapped handler
defer codeskop.Recover(ctx)                        // in your own goroutines
client := &http.Client{Transport: codeskop.Transport(http.DefaultTransport)}
```

## Options

`APIKey`, `Endpoint`, `Environment`, `Release` (auto-detected from common CI/host variables), `DisableRequests`, `DisableOutgoing`, `IgnoreRoutes`, `IgnoreErrors`, `BeforeSend`, `DisableUserID`, `Debug`. Environment variables: `CODESKOP_API_KEY`, `CODESKOP_ENDPOINT`, `CODESKOP_ENVIRONMENT`, `CODESKOP_RELEASE`.

Never captured: request or response bodies, cookies, `Authorization` headers, query strings.

## Releasing

Go modules are published by tagging: merge to `main`, update `CHANGELOG.md` and `Version` in `client.go`, then push a tag `vX.Y.Z`. The repository must be public for `proxy.golang.org` and pkg.go.dev to serve it; request indexing with `GOPROXY=https://proxy.golang.org go list -m github.com/Codeskop-io/codeskop-go@vX.Y.Z`.

## License

MIT
