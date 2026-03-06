# Project Context
Last updated: 2026-03-06 17:45

## Stack / Versions
- Language/runtime: Go 1.23.1.
- Core infra: NATS JetStream (streams + KV), ClickHouse.
- NATS target in env: 2.12.4.
- Topology: single node service; no cluster, no Docker requirement.
- Throughput target (reference): 10k events/sec.

## Architecture / Flow
1. Ingest:
   - HTTP (`ingest.http`) in all modes.
   - JetStream queue ingest (`ingest.nats`) only in `service.mode=nats`.
2. Match engine: `type`, `var`, allow-only `tags`, optional typed `value` predicate.
3. Deterministic key: `alert_id = rule/<rule>/<var>/<sha1(key.from_tags values)>`.
4. State machine: `pending -> firing -> resolved`.
5. Resolve logic:
   - `count_total`, `count_window`: resolve on `resolve.silence_sec + resolve.hysteresis_sec` inactivity.
   - `missing_heartbeat`: resolve on stable heartbeat recovery window `resolve.hysteresis_sec` after firing.
6. State backend:
   - `mode=nats`: NATS KV only (`tick` + `data`), fixed delete-consumer `alerting-resolve`.
   - `mode=single`: in-memory state, NATS paths force-disabled.
7. Notify pipeline:
   - route-driven dispatch by `[[rule.<name>.notify.route]]` (`channel + template`).
   - direct mode or async `notify.queue` mode (queue only in `mode=nats`).
8. Channel-specific lifecycle:
   - Telegram resolved replies to opening message.
   - Mattermost resolved posts into firing thread (`root_id`).
   - Jira/YouTrack firing creates issue, resolved closes by stored `external_ref`.
9. Built-in UI:
   - optional second HTTP server (`ui.listen`) in the same binary.
   - live update path is SSR initial snapshot + authenticated polling of `GET /ui/api/dashboard`.
   - service discovery comes from NATS KV registry; service status comes from live `readyz/healthz` probing.

## Data Contracts / Schemas
- Event input: `dt,type,var,tags,value{t,n|s|b},agg_cnt,win`.
- Alert card: identity, state, timestamps, metric snapshot, `external_refs` map.
- Notification payload: `alert_id,short_id,rule_name,state,var,tags,metric_value,started_at,duration,timestamp,external_ref,reply_to_message_id`.
- Correlation key: only `alert_id` (separate notification UUID is not used).
- UI snapshot payload: `generated_at`, `alerts[]`, `services[]` (summary counters were removed from the browser/runtime contract).

## Config Model + Defaults
- Active config directory: `configs/alerts`.
- File split:
  - `base.toml` (service/log/ingest/global notify/notify.queue)
  - `rules.count_total.toml`, `rules.count_window.toml`, `rules.missing_heartbeat.toml`, `rules.example_filter.toml`
  - `notify.telegram.toml`, `notify.mattermost.toml`, `notify.jira.toml`, `notify.youtrack.toml`
- Rule schema: only `[rule.<name>]` model; legacy `[[rule]]` rejected.
- Unified resolve hysteresis key: `resolve.hysteresis_sec >= 0` (all alert types).
- `missing_heartbeat` constraints:
  - required: `raise.missing_sec > 0`
  - forbidden: `raise.n`, `raise.window_sec`, `resolve.silence_sec`
- `count_total` / `count_window` keep `resolve.silence_sec` and support `resolve.hysteresis_sec`.
- `count_window` uses `raise.window_sec` (renamed from legacy `raise.tagg_sec`).
- Sample rule IDs in `configs/alerts/rules.*.toml`: `example_count_total`, `example_count_window`, `example_missing_heartbeat`.
- Templates are channel-scoped: `[[notify.<channel>.name-template]]`.
- Retry is channel-scoped: `[notify.<channel>.retry]`.
- Per-rule notification off windows: `[[rule.<name>.notify.off]]` with `days`, `from`, `to` (human-readable local-time schedule).
  - runtime uses precompiled windows after config normalization.
- `service.mode`:
  - `nats` default
  - `single` force-disables NATS ingest/state/notify.queue regardless of flags.
- `ingest.nats.subject/stream/consumer_name/deliver_group` are runtime-fixed; config keys rejected.
- `notify.queue.url` is not configurable; derived from `ingest.nats.url`.
- `notify.queue.dlq` is bool in `[notify.queue]`; DLQ stream/subject are runtime-fixed.
- `ui` config:
  - `[ui] enabled/listen/base_path/refresh_sec`
  - `[ui.auth.basic] username/password`
  - `[ui.nats] url/registry_bucket/registry_ttl_sec/stale_after_sec/offline_after_sec`
  - `[ui.health] timeout_sec`
  - `[ui.service] public_base_url`

## Runtime / Ops
- Run: `go run ./cmd/alerting --config-dir ./configs/alerts`.
- NATS bootstrap scripts: `deploy/nats/{bootstrap.sh,verify.sh,cleanup.sh}`.
- Queue benchmark cleanup hardening is active (explicit stream+KV teardown, backlog hard-fail).
- Delivery benchmark artifacts are local-only and ignored in git (`*.pprof`, `*.test`, `tmp_p12*` bench outputs).
- Built-in UI local contour artifacts (`.tmp/*`, `tmp_p133_contour/*`) are temporary only and were purged after validation; no local contour is intentionally left running.
- Latest validation state:
  - `go test ./internal/config ./internal/engine -count=1` PASS
  - `go test ./... -count=1` PASS
  - `go test -race ./...` PASS

## Invariants / Constraints
- Resolved alerts are removed from runtime and persistent card store.
- Reload is atomic: invalid snapshot is rejected with no partial apply.
- Alert transition persistence is CAS-safe for create/update paths; duplicate notifications are suppressed after storage conflicts.
- `started_at` is always present in outbound notification payload.
- Tag filter policy is allow-only.
- `key.from_tags` missing in incoming event context => event ignored + warning.
- Queue mode is best-effort per channel; direct mode is fail-fast per dispatch path.
- `notify.off` does not suppress alert generation/state transitions/logging; it suppresses only notification send attempts (direct and queue) while window is active.
- `notify.off` uses local host timezone only; no catch-up sends after off window ends.
- UI is read-only; no config editing or control actions are exposed through the browser.

## Decisions / Known Notes
- NATS state config is intentionally runtime-fixed to avoid lifecycle drift.
- Notify route contract is explicit (`channel + template`) in rules only.
- Unified hysteresis contract is now common across all alert types (`resolve.hysteresis_sec`).
- Notify runtime swap (`notify.queue` producer/worker) is lock-protected across reload and shutdown.
- Remaining old references to `resolve.recovery_sec` are deprecated history only in old plan entries.
- Delivery throughput conclusions before `P127` are not valid unless they came from the corrected end-to-end harness in `test/e2e/delivery_benchmark_test.go`.
- Heavy NATS delivery benchmarks still emit teardown-time `connection closed` noise after backlog drains to zero; treated as cleanup-order noise, not delivery correctness or throughput regression.
- The browser dashboard no longer uses SSE or summary counters; live refresh is polling-only via `/ui/api/dashboard`.
- UI code still has possible future cleanup/perf work: client-side row patching instead of full-`tbody` redraws, optional caching of service probe results across tight poll intervals, and further CSS/template compaction.

## Project Plan
- Accepted implementation packages through P153.
- Latest accepted technical packages:
  - P118: full matcher filter matrix test (tags/var/value types and all value operators).
  - P119: sample rule IDs renamed to `example_<alert_type>` + added `rules.example_filter.toml`.
  - P120: contract rename `raise.tagg_sec` -> `raise.window_sec` across runtime/tests/configs/docs.
  - P121: added `notify.off` day/time windows per rule; alert processing unchanged, notification delivery skipped in-window (local TZ, no catch-up).
  - P122: CAS-safe alert-card create/update flow with duplicate-send suppression on conflicts; precompiled `notify.off`; unified route normalization; reload/shutdown notify-runtime race fixed.
  - P127: corrected delivery verification now uses real end-to-end HTTP/NATS ingest -> manager -> notifier benchmarks for `telegram/http/mattermost/jira/youtrack` with identical batch sizes `1/100/1000`; all 30 cases drain with backlog `0`, and these runs are the only valid delivery throughput baseline.
  - P128: reduced delivery-path allocation cost by removing repeat-path full `LastNotified` map cloning, skipping unnecessary success-body reads in notify senders, and simplifying NATS tick presence payload; representative batch-1000 runs materially reduced `B/op` and `allocs/op` without changing throughput order or backlog guarantees.
  - P130-P151: built-in read-only dashboard implemented and iterated: config-driven basic auth, NATS registry publisher, active-alert/service aggregation, polling-based live UI, external-IP contour validation, UI compaction, and current simplified labels/layout.
  - P153: UI cleanup pass removed unused snapshot summary counters, removed unused `/ui/events` path and SSE tests, removed redundant immediate poll after SSR, parallelized service probes, purged temporary artifacts, and synced project context.
- Current state: no active open implementation scope; next work is user-driven.
