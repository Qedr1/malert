# UI Technical Specification

## Goal
Provide a built-in web dashboard for the alerting service without introducing an external frontend toolchain or deploy artifact.

V1 is read-only and focused on operational visibility.

## Hard Requirements
1. No separate asset files at runtime.
   All UI code, templates, styles, and scripts must be compiled into the same Go binary.
2. Dashboard data must update dynamically from the backend.
3. Authentication must be configured from TOML and use basic auth with login/password.
4. UI must be able to discover alerting service instances through NATS.
5. Service status must be derived from HTTP health probing of discovered instances, not from synthetic alert data.

## V1 Scope
The first page is a single dashboard.

It must show:
1. Active alerts and their states.
2. Count of alerting service hosts/instances and their current status.

## Explicit Non-Goals For V1
1. No config editing from the browser.
2. No rule authoring UI.
3. No external JS/CSS/image assets.
4. No separate frontend repository or Node/Vite/Webpack build.
5. No user/role management beyond one configured basic-auth credential set.

## Recommended Stack
1. Go `net/http` for HTTP serving.
2. Server-side HTML rendering.
   Preferred option: `templ`.
   Acceptable fallback: standard `html/template`.
3. Authenticated JSON snapshot polling for live dashboard updates.
4. Small inline CSS and inline JavaScript only.

This stack satisfies the single-binary requirement and keeps the UI operationally simple.

## Runtime Architecture
The UI is a built-in subsystem of the alerting service.

Main flow:
1. UI backend reads active alert cards from the state backend.
2. UI backend reads a service registry from NATS.
3. UI backend probes discovered service instances through `healthz` / `readyz`.
4. UI backend builds an aggregated dashboard snapshot.
5. Browser receives initial HTML from SSR.
6. Browser receives periodic snapshot updates through authenticated dashboard snapshot polling.

## Service Discovery Model
The UI must not infer service status from active alerts.

Discovery source:
1. A dedicated registry in NATS.
2. Each alerting instance registers itself there.

The registry exists for discovery only.
It is not the final truth for health.

## Service Status Model
Actual service status must be determined by probing the registered service endpoints.

Expected logic:
1. `readyz == 200` => `online`
2. `healthz == 200`, but `readyz != 200` => `degraded`
3. probe timeout / connect error / stale registry record => `offline`

This separates:
1. discovery via NATS
2. actual health via HTTP

## UI Configuration
Proposed TOML structure:

```toml
[ui]
enabled = true
listen = "127.0.0.1:8090"
base_path = "/ui"
refresh_sec = 2

[ui.auth.basic]
enabled = true
username = "admin"
password = "admin"

[ui.nats]
url = ["nats://127.0.0.1:4222"]
registry_bucket = "ui_registry"
registry_ttl_sec = 30
stale_after_sec = 10
offline_after_sec = 30

[ui.health]
timeout_sec = 2
```

Notes:
1. `ui.auth.basic` is mandatory for V1 when UI is enabled.
2. `ui.nats.url` may later fall back to `ingest.nats.url`, but that fallback must be explicit in code and validation.
3. UI config must stay separate from alerting rule config.

## Service Registry Record
Each alerting instance should publish one record into the registry bucket.

Suggested key:
`alerting/<instance_id>`

Suggested value:

```json
{
  "instance_id": "host1:pid:started_at",
  "host": "host1",
  "service_name": "alerting",
  "http_addr": "10.0.0.5:8080",
  "health_url": "http://10.0.0.5:8080/healthz",
  "ready_url": "http://10.0.0.5:8080/readyz",
  "mode": "nats",
  "version": "dev",
  "last_seen": "2026-03-06T13:10:00Z"
}
```

Registry responsibilities:
1. Discovery of instances.
2. Storage of probe targets and metadata.
3. TTL-based cleanup of dead registrations.

## Dashboard Data Model
The dashboard snapshot should contain:
1. Active alert rows.
2. Service instance rows.

Suggested active alert row:
1. `alert_id`
2. `rule_name`
3. `state`
4. `host`
5. `var`
6. `metric_value`
7. `started_at`
8. `last_seen_at`

Suggested service row:
1. `instance_id`
2. `host`
3. `mode`
4. `version`
5. `health_url`
6. `ready_url`
7. `last_seen`
8. `status`

## HTTP Routes
V1 routes:
1. `GET /ui`
   Returns the dashboard page.
2. `GET /ui/api/dashboard`
   Returns the current dashboard snapshot as JSON. This is the primary live-update path used by the browser page.
3. `GET /ui/healthz`
   Returns health of the UI subsystem itself.

## Live Update Model
The browser receives:
1. initial snapshot from the HTML page render
2. subsequent updates from authenticated `GET /ui/api/dashboard` polling

V1 should send full snapshots, not diffs.

Reason:
1. simpler client logic
2. simpler backend semantics
3. better operational debuggability

Suggested payload:

```json
{
  "generated_at": "2026-03-06T13:00:00Z",
  "alerts": [],
  "services": []
}
```

## Security Constraints
1. Basic auth must guard all UI routes.
2. Credentials come only from config.
3. UI must not expose secrets from alerting config.
4. Dashboard must not render full notifier credentials, tokens, passwords, or raw auth headers.
5. If later proxy-aware deployment is needed, trusted-proxy behavior must be explicit and opt-in.

## Rendering Constraints
1. No external CSS files.
2. No external JS files.
3. No CDN dependencies.
4. No generated static asset bundle on disk at runtime.

Allowed:
1. Inline `<style>` and `<script>` in rendered HTML.
2. Templates embedded into the Go binary.

## V1 Dashboard Layout
Main section:
1. table of alerting services and their status
2. table of active alerts and their states

The first version must optimize for operational clarity, not visual complexity.

## Implementation Plan

### Phase P1: Config Schema And Validation
1. Add `ui` config model.
2. Add `ui.auth.basic` config model.
3. Add `ui.nats` config model.
4. Add `ui.health` config model.
5. Validate required fields when UI is enabled.

Done criteria:
1. config load/validation tests cover positive and negative cases
2. enabling UI without auth or NATS discovery config fails fast

### Phase P2: Service Registry Publisher
1. Add runtime instance identity generation.
2. Add NATS registry publisher for service records.
3. Refresh registration periodically.
4. Ensure stale entries expire through TTL or overwrite logic.

Done criteria:
1. service publishes its own registry record on startup
2. record refresh works during runtime
3. record cleanup behavior is deterministic on shutdown / TTL expiry

### Phase P3: UI Backend Aggregator
1. Read active alert cards from state backend.
2. Read service registry records from NATS.
3. Poll `healthz` / `readyz` for discovered services.
4. Build aggregated dashboard snapshot.

Done criteria:
1. snapshot includes alert rows + service rows
2. service status is derived from probing, not registry-only metadata

### Phase P4: SSR Dashboard And Live Updates
1. Add `internal/ui` HTTP handlers.
2. Add basic-auth middleware.
3. Render dashboard HTML from server-side templates.
4. Add `/ui/api/dashboard` JSON snapshot endpoint.
5. Add inline JS to refresh dashboard blocks from authenticated snapshot polling.

Done criteria:
1. `/ui` works from one binary with no external assets
2. dashboard updates dynamically without page refresh

### Phase P5: Tests And Operational Guardrails
1. Config tests.
2. Registry publisher tests.
3. Aggregation tests.
4. Basic auth tests.
5. Live-update handler tests (`/ui/api/dashboard`).
6. E2E smoke test for dashboard.

Done criteria:
1. full test suite remains green
2. no regressions in alert processing behavior
3. UI does not introduce new external runtime dependencies

## Acceptance Criteria
V1 is accepted when:
1. UI is served from the same Go binary.
2. No external asset files are required at runtime.
3. UI is protected with config-driven basic auth.
4. Active alerts are visible on the dashboard.
5. Alerting service instances are discovered through NATS.
6. Their status is computed through HTTP health probes.
7. Dashboard updates dynamically from backend snapshots.
