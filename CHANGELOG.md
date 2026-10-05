# Changelog

## 0.1.0 (beta), 2026-10-05

First release.

- Errors: `CaptureError` with the caller's stack, wrapped-error cause chains, panics via the middleware or `defer codeskop.Recover(ctx)`.
- Incoming requests (`http_request`): `codeskop.Middleware` for `net/http` (ServeMux patterns), `codeskopgin.Middleware`, `codeskopecho.Middleware`.
- Outgoing calls: `codeskop.Transport` (`http.RoundTripper`).
- Background sending in gzip batches of up to 100 with Retry-After and backoff; remote config and sampling (failures and API Trust calls never sampled out); API Trust capture and opt-in blocking.
- Core package uses only the standard library; Go 1.23+.
