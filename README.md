<p align="center">
  <a href="https://www.codeskop.com/?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go">
    <img src="https://www.codeskop.com/media/product-tour-poster.jpg" alt="Codeskop dashboard: crash-free users, active users, open issues and API latency" width="720">
  </a>
</p>

# Codeskop for Go

**Codeskop** is crash, error and API monitoring for mobile, web and backend apps. [Get started free at www.codeskop.com](https://www.codeskop.com/?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go).

[![Go Reference](https://pkg.go.dev/badge/github.com/Codeskop-io/codeskop-go.svg)](https://pkg.go.dev/github.com/Codeskop-io/codeskop-go)
[![version](https://img.shields.io/github/v/tag/Codeskop-io/codeskop-go?sort=semver&label=version)](https://github.com/Codeskop-io/codeskop-go/tags)
[![Go version](https://img.shields.io/github/go-mod/go-version/Codeskop-io/codeskop-go)](https://github.com/Codeskop-io/codeskop-go/blob/main/go.mod)
[![license](https://img.shields.io/github/license/Codeskop-io/codeskop-go.svg)](https://github.com/Codeskop-io/codeskop-go/blob/main/LICENSE)

`codeskop-go` is the Go SDK for [Codeskop](https://www.codeskop.com/?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go). Add it to a Go service and Codeskop groups its errors and panics into issues with stack traces, records every request it handles by route, and shows which outgoing API calls fail or slow down, next to the crashes and network calls from your mobile and web apps.

- **Errors and panics:** `CaptureError` with the caller's stack and wrapped-error cause chains, panics in your handlers (answered with a 500) and in your own goroutines.
- **Incoming requests:** every request your service handles, by route pattern (`/orders/{id}`), with status, latency and size.
- **Outgoing calls:** requests to other APIs through `codeskop.Transport`, with status, latency and failures.
- **Light and safe:** the core package uses only the standard library. Events are batched, compressed and sent from a background goroutine, and every function is safe to call before `Init` and never panics.

Works with `net/http` (including `ServeMux` patterns), Gin and Echo. Go 1.23 and later.

## Get started in 2 minutes

**1. Create a free account** at **[dashboard.codeskop.com/signup](https://dashboard.codeskop.com/signup?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go)**. No credit card. Every project has a **Test** key (`cs_test_pk_…`) and a **Live** key (`cs_live_pk_…`); copy one from **API tokens**.

**2. Install:**

```bash
go get github.com/Codeskop-io/codeskop-go
```

**3. Set your key** (Test key locally, Live key in production):

```bash
export CODESKOP_API_KEY=cs_test_pk_…
```

**4. Initialize at the start of `main` and wrap your router:**

```go
package main

import (
	"net/http"
	"time"

	codeskop "github.com/Codeskop-io/codeskop-go"
)

func main() {
	codeskop.Init(codeskop.Options{})   // reads CODESKOP_API_KEY
	defer codeskop.Close(2 * time.Second) // send queued events before exiting

	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders/{id}", getOrder)

	http.ListenAndServe(":8080", codeskop.Middleware(mux))
}
```

Panic in a handler or call `codeskop.CaptureError` and the issue shows up in your dashboard a few seconds later.

## Frameworks

### net/http

Wrap your handler with `codeskop.Middleware`, as above. Requests are recorded by `ServeMux` pattern, and panics are reported and answered with a 500.

### Gin

```go
import codeskopgin "github.com/Codeskop-io/codeskop-go/integrations/gin"

r := gin.New()
r.Use(codeskopgin.Middleware()) // use instead of gin.Recovery()
```

Reports panics and errors added with `c.Error`. Routes like `/orders/:id` are recorded as `/orders/{id}`.

### Echo

```go
import codeskopecho "github.com/Codeskop-io/codeskop-go/integrations/echo"

e := echo.New()
e.Use(codeskopecho.Middleware())
```

Reports panics and errors returned by your handlers.

The Gin and Echo integrations are separate packages: the core `codeskop` package imports only the standard library.

## Common tasks

```go
// Report a handled error, with optional tags.
if err := charge(ctx, order); err != nil {
	codeskop.CaptureError(ctx, err, map[string]string{"provider": "stripe"})
}

codeskop.CaptureMessage(ctx, "Inventory sync skipped")

// Inside a wrapped handler: attach events to a user (your own ID, never an email).
codeskop.SetUser(r.Context(), user.ID)

// Report panics in your own goroutines.
go func() {
	defer codeskop.Recover(ctx)
	work()
}()

// Record outgoing calls.
client := &http.Client{Transport: codeskop.Transport(http.DefaultTransport)}
```

In short-lived programs (CLIs, jobs), call `codeskop.Flush(2 * time.Second)` or `codeskop.Close` before exiting so queued events are sent.

## What's captured automatically

With `Init` and the middleware in place:

- **Panics** in wrapped handlers, with the route they happened on (and errors from Gin and Echo handlers).
- **Every incoming request** by route, with method, status, latency and size. Find them on the Network page under *Requests your servers receive*. Health checks (`/health*`, `/healthz`, `/metrics`, `/favicon.ico`) aren't recorded.
- **Outgoing calls** made through `codeskop.Transport`, on the Network page under *APIs your apps call*.
- **Release**, detected from `CODESKOP_RELEASE` or the commit on Render, Heroku, Railway, Cloud Run and GitHub Actions.

Go has no global panic handler, so panics outside a wrapped handler need `defer codeskop.Recover(ctx)`.

## API keys and environments

Server SDKs use the project's **public** key, the same kind of key your mobile and web apps use. Public keys can only send events, never read your data.

| Key | Use it for |
|---|---|
| `cs_test_pk_…` (Test) | Local development, CI and staging, so test traffic never mixes with real users |
| `cs_live_pk_…` (Live) | Production |

Set it as `CODESKOP_API_KEY` in each environment and the SDK reads it on its own, or pass `Options{APIKey: …}` in code. Never put a secret key (`cs_…_sk_…`) in a service: the SDK refuses it and stays switched off, without throwing. A missing or malformed key does the same, so if nothing shows up, check the key first.

Create a separate Codeskop project for each backend service so its issues and routes stay apart. More: [Core concepts](https://www.codeskop.com/docs/concepts?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go) and [server SDK keys](https://www.codeskop.com/docs/server?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go#keys).

## Configuration

Zero values fall back to `CODESKOP_*` environment variables, then to the defaults below.

| Option | Type | Default | Description |
|---|---|---|---|
| `APIKey` | `string` | `CODESKOP_API_KEY` | The project's public key. Secret keys are refused and the SDK stays off. |
| `Environment` | `string` | `production` | Shown on events. Env var `CODESKOP_ENVIRONMENT`. |
| `Release` | `string` | auto | Your version or commit. Env var `CODESKOP_RELEASE`. |
| `DisableRequests` | `bool` | `false` | Stop recording incoming requests. |
| `DisableOutgoing` | `bool` | `false` | Stop recording outgoing calls. |
| `IgnoreRoutes` | `[]string` | health checks | Glob patterns never recorded. |
| `IgnoreErrors` | `[]string` | `nil` | Error type names never sent, for example `*url.Error`. |
| `BeforeSend` | `func(Event) Event` | `nil` | Edit or drop any event before it's sent. |
| `DisableUserID` | `bool` | `false` | Never attach user IDs. |
| `Debug` | `bool` | `false` | Log what the SDK does. |

All options: [Go docs](https://www.codeskop.com/docs/server/go?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go#options) and the [package reference](https://pkg.go.dev/github.com/Codeskop-io/codeskop-go#Options).

## Plans

| | Free | Starter | Growth | Scale |
|---|---|---|---|---|
| Errors | ✓ | ✓ | ✓ | ✓ |
| Incoming requests and outgoing API calls | | ✓ | ✓ | ✓ |
| Events per month | 10,000 | 100,000 | 1,000,000 | 10,000,000 |
| Data retention | 7 days | 30 days | 90 days | 365 days |

The SDK is the same on every plan; your plan decides what's kept. Request and API call monitoring follow the network monitoring on your plan, with the same sampling, and failed requests are always kept. See [pricing](https://www.codeskop.com/pricing?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go).

## Privacy

**Never captured:** request or response bodies, cookies, `Authorization` headers and query strings. Requests are recorded by route template, so IDs in the path don't become separate routes.

**What is sent:** the error type, message and stack trace; for each request, the method, route, status, latency and size; for outgoing calls, the method, host, path, status and latency. Each event also carries your environment and release, plus the host name, OS and runtime version of the server.

**You choose the user ID.** Pass your own ID, never an email address or phone number.

**Error messages are sent as written.** If yours can contain personal data, scrub or drop events with `BeforeSend`, or skip error types with `IgnoreErrors`.

Details: [Privacy & Security](https://www.codeskop.com/docs/privacy?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go).

## Documentation

- [Go guide](https://www.codeskop.com/docs/server/go?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go)
- [Server SDKs overview](https://www.codeskop.com/docs/server?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go)
- [Privacy & Security](https://www.codeskop.com/docs/privacy?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go)
- [Troubleshooting](https://www.codeskop.com/docs/troubleshooting?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go)
- [Package reference on pkg.go.dev](https://pkg.go.dev/github.com/Codeskop-io/codeskop-go)
- [Changelog](https://github.com/Codeskop-io/codeskop-go/blob/main/CHANGELOG.md)

## Other Codeskop SDKs

Monitor your mobile apps, web apps and backend services in one dashboard:

| Platform | Package | Links |
|---|---|---|
| Android | `com.codeskop.sdk:tracker-android` | [Docs](https://www.codeskop.com/docs/quickstart/android?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go) |
| Kotlin Multiplatform and iOS (beta) | `com.codeskop.sdk:tracker-core` | [Docs](https://www.codeskop.com/docs/kmp?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go) |
| Flutter | `codeskop_tracker` | [Docs](https://www.codeskop.com/docs/android/flutter?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go) |
| Web (JavaScript, React) | `@codeskop/tracker`, `@codeskop/tracker-react` | [Docs](https://www.codeskop.com/docs/web/installation?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go) · [GitHub](https://github.com/Codeskop-io/web-sdk) |
| **Go** (this SDK) | `github.com/Codeskop-io/codeskop-go` | [Docs](https://www.codeskop.com/docs/server/go?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go) · [GitHub](https://github.com/Codeskop-io/codeskop-go) |
| Python | `codeskop` | [Docs](https://www.codeskop.com/docs/server/python?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go) · [GitHub](https://github.com/Codeskop-io/codeskop-python) |
| Node.js | `@codeskop/node` | [Docs](https://www.codeskop.com/docs/server/node?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go) · [GitHub](https://github.com/Codeskop-io/codeskop-node) |
| Java and Kotlin (JVM) | `com.codeskop.sdk:codeskop-server` | [Docs](https://www.codeskop.com/docs/server/java?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go) · [GitHub](https://github.com/Codeskop-io/codeskop-jvm) |
| PHP (beta) | `codeskop/codeskop-php` | [Docs](https://www.codeskop.com/docs/server/php?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go) · [GitHub](https://github.com/Codeskop-io/codeskop-php) |
| Any other language | OpenTelemetry (OTLP) or HTTPS | [OpenTelemetry](https://www.codeskop.com/docs/server/opentelemetry?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go) · [Ingest API](https://www.codeskop.com/docs/reference/ingest-api?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go) |

## Help

Questions or a bug? [Contact us](https://www.codeskop.com/contact?utm_source=github&utm_medium=readme&utm_campaign=codeskop-go), email support@codeskop.com, or open an issue in this repository.

## Development

```bash
go vet ./...
go test -race ./...
```

Go modules are published by tagging: merge to `main`, update `CHANGELOG.md` and `Version` in `client.go`, then push a tag `vX.Y.Z`. To make pkg.go.dev pick it up straight away: `GOPROXY=https://proxy.golang.org go list -m github.com/Codeskop-io/codeskop-go@vX.Y.Z`.

## License

[MIT](https://github.com/Codeskop-io/codeskop-go/blob/main/LICENSE)
