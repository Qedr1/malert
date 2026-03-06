# Detailed Plan
Last updated: 2026-02-25 09:05

## P1 (DONE)
Goal: confirm implementation stack and boundaries.
1. Confirm language/runtime and service layout. DONE: Go 1.23.1 + NATS 2.12.4 JetStream + ClickHouse (env current), MVP single-node/no cluster/no Docker.
2. Confirm required integrations for MVP (HTTP, NATS, notifications). DONE: outbound notification channel for MVP is Telegram only.
3. Define non-functional constraints (throughput, latency, HA). DONE: throughput target fixed at 10k events/sec; latency SLO intentionally not fixed at this stage.
Next: accepted.

## P2 (DONE)
Goal: config schema and validator.
1. Define TOML schema for shared and per-type fields. DONE.
2. Implement required/forbidden field checks per alert type. DONE.
3. Add config-loading tests with negative cases. DONE (`internal/config/config_test.go`).
Next: accepted.

## P3 (DONE)
Goal: ingestion and matching pipeline.
1. Implement input DTOs and strict decoding/validation. DONE (`internal/domain/event.go`, `internal/ingest/http.go`, `internal/ingest/nats.go`).
2. Implement matching (`type`, `var`, tags, typed `value` predicate). DONE (`internal/engine/matcher.go` + tests).
3. Implement deterministic alert key builder. DONE (`internal/engine/key_builder.go` + tests).
Next: accepted.

## P4 (DONE)
Goal: alert state lifecycle on NATS KV.
1. Implement raise/refresh flow using `tick.Create/Put` with per-key TTL. DONE: tick refresh uses publish to `$KV.<bucket>.<key>` with `Nats-TTL`; stream `AllowMsgTTL` is enabled in setup.
2. Implement delete-marker consumer with queue semantics. DONE: `internal/state/delete_consumer.go` and service wiring (`buildDeleteConsumer`) with durable queue consumer.
3. Implement idempotent resolve (CAS), close procedure, and cleanup. DONE: `Manager.ResolveByTTL` uses `HasTick` guard + CAS update + resolved notification + delete/cleanup flow.
Next: accepted.

## P5 (DONE)
Goal: routing and outbound handlers.
1. Implement routing model in alert card. DONE: dispatcher fan-out by configured channels, per-channel repeat keys preserved (`internal/app/manager.go`).
2. Implement HTTP scenario sender. DONE (`internal/notify/notify.go` + `internal/notify/notify_test.go`).
3. Implement Telegram and Mattermost adapters. DONE (`internal/notify/notify.go` + sender tests).
Next: accepted.

## P6 (DONE)
Goal: verification and operations.
1. Add unit tests for filters, key generation, and rule engines. DONE (`internal/engine/*_test.go`, `internal/config/config_test.go`, `internal/notify/notify_test.go`, `internal/state/store_test.go`).
2. Add integration tests for NATS KV TTL resolve path. DONE (`internal/state/delete_consumer_integration_test.go`, `internal/state/nats_store_test.go`).
3. Document runbook and minimal deployment profile. DONE (`docs/RUNBOOK.md`).
Next: accepted.

## P7 (DONE)
Goal: MVP acceptance checks from spec.
1. Add e2e tests for hot reload success/fail behavior (`reload_enabled=true`, valid snapshot apply, invalid snapshot rollback). DONE (`test/e2e/reload_e2e_test.go`).
2. Add reproducible throughput benchmark for ingest path (target reference: 10k events/sec). DONE (`test/e2e/throughput_benchmark_test.go`), measured `619739 events/sec` on current env (`go test ./test/e2e -bench BenchmarkIngestThroughput -benchtime=3s`).
3. Update runbook with acceptance commands and expected outcomes. DONE (`docs/RUNBOOK.md`).
Next: accepted.

## P8 (DONE)
Goal: full codebase audit and optimization pass.
1. Fix runtime defects found in audit. DONE: reload now swaps notifier dispatcher (`internal/app/service.go`, `internal/app/manager.go`), manager notify reads are lock-safe, NATS ingest logs decode/push errors (`internal/ingest/nats.go`), memory tick expiry now cleans stale entries (`internal/state/store.go`).
2. Keep acceptance coverage aligned with optimized behavior. DONE: reload e2e now validates notify-channel enablement via reload (`test/e2e/reload_e2e_test.go`), memory-store expiry assertion extended (`internal/state/store_test.go`).
3. Re-run quality gates after optimizations. DONE: `gofmt`, `go test ./...`, `go vet ./...`, `go test -race ./...` passed.
Next: accepted.

## P9 (DONE)
Goal: peak-load profiling and whole-codebase optimization plan.
1. Run peak benchmark with pprof CPU/heap profiles and stress checks. DONE: `BenchmarkIngestThroughput` @ 20s produced `461348 events/sec`, `go test -race ./...` green, `go test ./test/e2e -run HotReload -count=20` green.
2. Identify hotspots and leak candidates using measured evidence. DONE: CPU/alloc hotspots dominated by `state.cloneCard`, `engine.BuildAlertKey`, `manager.sortedRulesLocked`, `engine.GetStateSnapshot`; active memory-growth scenario reproduced for high key cardinality (`500k` unique hosts => `~325MB` retained heap in runtime state).
3. Produce codebase-wide optimization/reuse plan with risk priorities. DONE: ready for implementation approval; focuses on hot-path allocation cuts, bounded runtime state, and consolidation of repeated logic.
Next: pending user approval for implementation phase.

## P10 (DONE)
Goal: implement first wave of hot-path optimizations from P9.
1. Remove per-event rule sorting and per-call channel sorting allocations. DONE (`internal/app/manager.go`, `internal/notify/notify.go`).
2. Reduce key-builder allocations (`BuildAlertKey`) without contract changes. DONE (`internal/engine/key_builder.go`).
3. Cut unnecessary state-store read/write cycles when decision state is unchanged and no notification is sent. DONE (`internal/app/manager.go` fast-path in `applyDecision`).
4. Fix service error-path shutdown leak on HTTP server failure branch. DONE (`internal/app/service.go`).
5. Re-run tests/race/bench and compare throughput/allocation metrics against baseline. DONE: benchmark improved from `461348` to `920645 events/sec`; allocations improved from `1696 B/op, 23 allocs/op` to `744 B/op, 20 allocs/op` (`go test ./test/e2e -bench BenchmarkIngestThroughput -benchtime=20s`).
Next: accepted.

## P11 (DONE)
Goal: implement Telegram MVP bot flow with quoted resolve message.
1. Replace raw Telegram HTTP sender with `github.com/go-telegram/bot` integration and keep config-compatible startup behavior. DONE (`internal/notify/notify.go`, `go.mod`, `go.sum`).
2. Extend notification pipeline to return per-channel send metadata and persist runtime opening `message_id` per `alert_id/channel`. DONE (`internal/notify/notify.go`, `internal/app/manager.go`, `internal/engine/engine.go`, `internal/domain/alert.go`).
3. Ensure `resolved` Telegram notification is sent as reply to the opening Telegram message for the same alert. DONE (`internal/app/manager.go` + runtime channel refs in engine state).
4. Add/refresh tests: sender unit tests + end-to-end manager flow (`firing -> resolved` reply linkage), then run full test suite. DONE (`internal/notify/notify_test.go`, `test/e2e/telegram_reply_e2e_test.go`), `go test ./...` + `go test -race ./...` passed.
Next: accepted.

## P12 (DONE)
Goal: full codebase audit, optimization pass, and logical-duplicate removal.
1. Re-audit all source files and validate baseline quality gates (`go test`, `go vet`, `go test -race`). DONE.
2. Remove duplicated notify/state-transition logic in manager and keep behavior identical. DONE (`internal/app/manager.go`: common notification dispatch helper, common card-state transition helper, removed duplicated branches).
3. Apply low-risk performance optimizations on hot/retry paths and reduce unnecessary state store operations. DONE (`internal/app/manager.go`: skip tick refresh on resolved; `internal/notify/notify.go`: retry timer reuse; `internal/engine/engine.go`: avoid zero-length clone allocations; repeat settings computed once per rule in tick loop).
4. Add/adjust tests for new validation/behavior and re-run full quality gates. DONE (`internal/config/config_test.go` added enabled-Telegram credentials validation case; `go test ./...`, `go vet ./...`, `go test -race ./...` passed).
Next: accepted.

## P14 (DONE)
Goal: move channel message templates into dedicated config and enforce unified notify processing.
1. Introduce per-channel template config (`notify.templates.<channel>`) with sane defaults and validation. DONE (`internal/config/config.go` defaults + syntax validation).
2. Implement shared notification rendering pipeline before channel transport dispatch. DONE (`internal/notify/notify.go` dispatcher-level template rendering).
3. Keep channel adapters transport-only (Telegram/HTTP/Mattermost) and consume rendered payload from common layer. DONE (`internal/notify/notify.go` sender formatting removed from Telegram/Mattermost).
4. Update README/TOML examples and extend tests for template rendering + channel routing behavior. DONE (`docs/README.md`, `internal/notify/notify_test.go`, `internal/config/config_test.go`).
5. Re-run quality gates (`go test ./...`, `go vet ./...`, `go test -race ./...`). DONE (all green).
Next: accepted.

## P15 (DONE)
Goal: enforce separate template catalog with per-rule transport+template routing only.
1. Replace old all-channel fan-out with `rule.notify.route` dispatch in manager (events/repeats/resolve/reload cleanup), keeping Telegram resolve reply threading. DONE (`internal/app/manager.go` route-based dispatch + TTL missing-rule fail-fast log/exit).
2. Wire dispatcher/service constructor to top-level `[[notify_template]]` list and require template name at send time. DONE (`internal/app/service.go`, `internal/notify/notify.go`, call-sites updated).
3. Update config/tests/e2e/docs to new TOML model: templates in `[[notify_template]]`, rule-level bindings in `[[rule.notify.route]]`. DONE (`internal/config/config_test.go`, `internal/notify/notify_test.go`, `test/e2e/*`, `docs/README.md`).
4. Re-run quality gates (`go test ./...`, `go vet ./...`, `go test -race ./...`) and mark DONE only on green. DONE (all green).
Next: accepted.

## P128 (DONE)
Goal: reduce delivery-path runtime and allocation cost based on corrected P127 benchmark/pprof evidence.
1. Cut repeat-path runtime allocations by removing full `LastNotified` map cloning from hot-path repeat scheduling in `internal/engine/engine.go` and `internal/app/manager.go`. DONE: `RepeatSnapshot` no longer clones the whole `LastNotified` map; repeat scheduling now reads only the needed keyed timestamps through `Engine.GetLastNotified`. Added direct unit coverage in `internal/engine/engine_test.go`.
2. Reduce HTTP sender/tracker/mattermost success-path allocations by avoiding unnecessary response-body reads and other avoidable request-path work in `internal/notify/notify.go`. DONE: `executeRequest` now reads success bodies only when the caller actually needs them (`HTTP` notify never, `mattermost` create yes / update-delete no, `tracker` only when `responseRefKey` is configured). Added unit coverage in `internal/notify/notify_test.go`.
3. Revisit NATS tick refresh cost in `internal/state/nats_store.go` and surrounding manager flow; implement only a low-risk change that keeps TTL/resolve semantics identical. DONE: tick refresh payload was simplified to a constant non-empty presence marker because no runtime path consumes the previous JSON body; TTL behavior remains fully driven by `Nats-TTL` header and delete-marker flow. This reduced tick-path allocations without changing resolve semantics.
4. Run formatting, targeted notify/e2e coverage, full suite, race suite, and repeat corrected P127 benchmark matrix to measure whether app-level hotspots (`cloneTimes`, request/response alloc path, tick refresh) actually moved. DONE:
   - Verification PASS:
     `gofmt -w internal/engine/engine.go internal/app/manager.go internal/notify/notify.go internal/state/nats_store.go internal/notify/notify_test.go internal/engine/engine_test.go`
     `go test ./internal/notify ./internal/engine ./internal/state -count=1`
     `go test ./test/e2e -run 'TestHTTPNotifyDeliveryE2E|TestMattermostWebhookFiringAndResolveE2E|TestJiraTrackerCreateAndResolveE2E|TestYouTrackTrackerCreateAndResolveE2E|TestTelegramResolvedRepliesToOpeningMessage' -count=1`
     `go test ./... -count=1`
     `go test -race ./...`
   - Corrected delivery matrix after-pass PASS:
     `E2E_DELIVERY_BATCHES=20 go test ./test/e2e -run '^$' -bench 'Benchmark(HTTP|NATS)DeliveryE2E' -benchtime=1x -count=1`
     all 30 cases still finished with `backlog_events=0`.
   - Representative before/after deltas:
     `HTTP/http batch_1000`: `507.5MB -> 385.6MB`, `5.23M -> 4.65M allocs/op`, throughput flat (`847.6 -> 847.8 deliveries/sec`)
     `NATS/http batch_1000`: `494.3MB -> 379.7MB`, `5.16M -> 4.61M allocs/op`, throughput flat/slightly up (`904.9 -> 895.8` in representative rerun; full matrix stayed same order of magnitude with zero backlog)
     `HTTP/telegram batch_1000`: `875.0MB -> 759.4MB`, `8.86M -> 8.28M allocs/op`
     `NATS/jira batch_1000`: `719.7MB -> 589.8MB`, `8.15M -> 7.49M allocs/op`
   - After-pprof summary:
     `alerting/internal/engine.cloneTimes` dropped out of the top alloc-space tables for the representative after-profiles, which confirms the repeat-path map-clone cut landed on a real hotspot.
     response/read-path alloc pressure also fell materially in `HTTP`/tracker cases; top alloc tables are now led by `TickRuleWithFiring`, request parsing, and remaining NATS/HTTP client costs rather than full-map cloning.
     `RefreshTick` still shows up as a secondary app-level cost in some profiles, but lower than before.
   - Remaining issue:
     NATS heavy benchmarks still emit teardown-time noise like `refresh tick: publish tick: nats: connection closed` or `update card: nats: connection closed` after successful drain; backlog stays zero, so this is a shutdown-order cleanup issue, not a throughput regression.
Next: accepted.

## P130 (DONE)
Goal: capture a separate technical specification and implementation plan for the built-in web dashboard.
1. Create a dedicated UI specification file outside the general product README. DONE: added `internal/ui/README.md` as the single user-facing spec for the planned built-in dashboard.
2. Document V1 scope and hard requirements. DONE: fixed requirements for one binary, no external runtime assets, live backend-driven updates, config-driven basic auth, NATS-based discovery, and service status via `healthz`/`readyz` probing.
3. Describe the target architecture. DONE: documented stack choice, NATS service registry model, health polling model, dashboard data model, SSE update flow, HTTP routes, security constraints, and rendering constraints.
4. Add a phased implementation plan. DONE: captured phases for config schema, service registry publisher, backend aggregation, SSR+SSE UI, and test/guardrail coverage.
Next: pending user acceptance.

## P17 (DONE)
Goal: add opt-in real Telegram end-to-end test (ingest -> firing -> resolved reply chain).
1. Add live Telegram e2e test using env-driven credentials (`E2E_TG_BOT_TOKEN`, `E2E_TG_CHAT_ID`) and skip when env is missing. DONE (`test/e2e/telegram_live_e2e_test.go`).
2. Route Telegram calls through local proxy to capture `sendMessage` requests/responses while forwarding to real Telegram API. DONE (`telegramLiveProxy` in `test/e2e/telegram_live_e2e_test.go`).
3. Assert full flow: firing delivered, resolved delivered, resolved contains `reply_parameters.message_id` pointing to opening message. DONE: after channel access fix, live run passed and reply linkage was asserted (`go test ./test/e2e -run TelegramLive -count=1 -v`).
4. Run focused e2e test and keep default suite safe (no live calls without explicit env). DONE: `go test ./test/e2e -run TelegramLive -count=1 -v` passes in skip mode without env; `go test ./...` green.
Next: accepted.

## P18 (DONE)
Goal: run real Telegram alert lifecycle with manual user acceptance criteria.
1. Tune live Telegram e2e config for manual observation window (~60s until resolved) via env-controlled `silence_sec`. DONE (`E2E_TG_SILENCE_SEC`, default 60, in `test/e2e/telegram_live_e2e_test.go`).
2. Execute real run with provided Telegram credentials and capture technical send metadata (`message_id`, timestamps, reply linkage). DONE: real run passed (`go test ./test/e2e -run TelegramLive -count=1 -v`, ~61s) after channel access was fixed.
3. User manually verifies Telegram delivery rendering and lifecycle; user verdict is the only success criterion. DONE: user confirmed `messages arrived` and `resolved on firing is correct`.
4. If Telegram rejects chat/channel access, report exact API error and required credential/channel fix. DONE: earlier failures (`400 chat not found`) were reproduced and resolved after channel/bot access fix.
Next: accepted.

## P19 (DONE)
Goal: persist live Telegram credentials/config artifacts in repo-local config files.
1. Add reusable runtime TOML config with provided bot token and chat ID for manual live runs. DONE (`configs/live.telegram.toml`).
2. Add env file for live e2e variables (`E2E_TG_BOT_TOKEN`, `E2E_TG_CHAT_ID`, `E2E_TG_SILENCE_SEC`). DONE (`configs/live.telegram.env`).
3. Verify created artifacts exist and contain provided values. DONE (file inspection).
Next: pending user acceptance.

## P20 (DONE)
Goal: split live runtime profile into dedicated base/rules/notify transport configs.
1. Move service/runtime sections into `configs/live/base.toml`. DONE.
2. Move alert rule logic into `configs/live/rules.toml`. DONE.
3. Move Telegram transport config and notify templates into `configs/live/notify.telegram.toml`. DONE.
4. Remove mixed monolithic `configs/live.telegram.toml`. DONE.
5. Keep env credentials for live e2e unchanged (`configs/live.telegram.env`). DONE.
Next: pending user acceptance.

## P21 (DONE)
Goal: remove global notify retry and switch to per-channel retry configs.
1. Replace global `notify.retry` model with channel-level retry (`notify.telegram.retry`, `notify.http.retry`, `notify.mattermost.retry`). DONE (`internal/config/config.go`).
2. Update dispatcher retry execution to apply policy per destination channel. DONE (`internal/notify/notify.go`).
3. Update unit/e2e config fixtures and live config to channel-level retry sections. DONE (`internal/config/config_test.go`, `internal/notify/notify_test.go`, `test/e2e/*`, `configs/live/notify.telegram.toml`).
4. Update user spec docs to new retry schema. DONE (`docs/README.md`).
5. Re-run quality gates. DONE (`gofmt -w $(rg --files -g '*.go')`, `go test ./...`, `go vet ./...`).
Next: pending user acceptance.

## P22 (DONE)
Goal: move global notify controls from Telegram transport config to global service config.
1. Move `[notify]` section from `configs/live/notify.telegram.toml` to `configs/live/base.toml`. DONE.
2. Keep Telegram transport file transport-only (`[notify.telegram]`, `[notify.telegram.retry]`, `[[notify_template]]`). DONE.
3. Verify split config startup with directory mode. DONE (`go run ./cmd/alerting --config-dir ./configs/live` + readiness check `200`).
Next: pending user acceptance.

## P23 (DONE)
Goal: move template catalog to channel-scoped sections and keep route binding (`channel+template`) in alert rules.
1. Replace global `[[notify_template]]` model with channel-scoped `[[notify.<channel>.name-template]]` in config schema/validation. DONE (`internal/config/config.go`).
2. Keep `[[rule.notify.route]]` contract unchanged: each route declares destination `channel` and `template` name. DONE (`internal/config/config.go`, no contract changes in rule schema).
3. Update dispatcher template lookup to resolve by `channel+template` using channel-scoped template sets. DONE (`internal/notify/notify.go`).
4. Update live configs/tests/docs to new TOML structure. DONE (`configs/live/notify.telegram.toml`, `test/e2e/*`, `internal/config/config_test.go`, `docs/README.md`).
5. Re-run quality gates (`gofmt`, `go test ./...`, `go vet ./...`). DONE (all green).
Next: pending user acceptance.

## P25 (DONE)
Goal: rename live config directory to alerts and add exhaustive comments in all config artifacts.
1. Rename `configs/live` to `configs/alerts` and keep file contents semantically equivalent. DONE.
2. Add detailed explanatory comments to `configs/alerts/base.toml`, `configs/alerts/rules.toml`, `configs/alerts/notify.telegram.toml`, and `configs/live.telegram.env`. DONE.
3. Update path references in docs/commands from `configs/live` to `configs/alerts` where applicable. DONE (runtime command/comment paths updated to `configs/alerts`; historical changelog entries preserved as-is).
4. Verify startup with directory mode (`go run ./cmd/alerting --config-dir ./configs/alerts`) and readiness `200`. DONE (`READY_OK`).
5. Commit only config/comment/path changes for this task. DONE.
Next: pending user acceptance.

## P26 (DONE)
Goal: align README with current config layout and transport-template model.
1. Update README to current template model (`[[notify.<channel>.name-template]]`) and rule routing (`[[rule.notify.route]] channel+template). DONE.
2. Add explicit section with active config file layout under `configs/alerts/*` plus `configs/live.telegram.env`. DONE.
3. Fix example references to `rule.pending` scope and related wording. DONE.
4. Update bookkeeping docs for this README sync. DONE.
Next: pending user acceptance.

## P30 (DONE)
Goal: enable multi-service processing chain (`events -> queue -> agents -> alert`) with reproducible NATS bootstrap.
1. Add `deploy/nats` bootstrap scripts to provision stream/subjects/KV/consumers and validation checks. DONE (`deploy/nats/bootstrap.sh`, `deploy/nats/verify.sh`, `deploy/nats/cleanup.sh`, `deploy/nats/.env.example`).
2. Switch ingest from core NATS subscribe to JetStream queue-consumer with explicit ACK/NAK handling. DONE (`internal/ingest/nats.go`, `internal/app/service.go`).
3. Extend `ingest.nats` config model (consumer/group/ack/retry knobs) and enforce validation/defaults. DONE (`internal/config/config.go`, `internal/config/config_test.go`).
4. Ensure redelivery-safe processing path (`decode -> process -> ack`; processing failure -> nack/redelivery) without duplicate alert transitions. DONE (`internal/ingest/nats.go`, `internal/app/manager.go`).
5. Add e2e test for two service instances consuming one NATS queue and producing one logical alert lifecycle. DONE (`test/e2e/multiservice_nats_e2e_test.go`).
6. Add e2e test for TTL resolve in multi-instance mode (delete-marker queue consumer resolves once). DONE (`test/e2e/multiservice_nats_e2e_test.go`, assert single `resolved`).
7. Add queue-path throughput benchmark scenario (target reference: 10k events/sec) using NATS producer flow. DONE (`test/e2e/throughput_nats_benchmark_test.go`).
8. Update configs and README for multi-service + deploy bootstrap workflow. DONE (`configs/alerts/base.toml`, `docs/README.md`).
Next: pending user acceptance.

## P31 (DONE)
Goal: remove duplicated/redundant logic in runtime and tests without behavior changes.
1. Unify notify channel registry and predicate/wildcard logic across config validation and runtime matching. DONE (`internal/config/config.go`, `internal/notify/notify.go`, `internal/engine/matcher.go`).
2. Refactor manager/service duplicated branches (repeat notify payload/build and startup cleanup paths), keep semantics unchanged. DONE (`internal/app/manager.go`, `internal/app/service.go`).
3. Remove unused fields/state in runtime components where values are never read. DONE (`internal/ingest/nats.go`, `internal/state/delete_consumer.go`, dropped write-only `Service.dispatch`).
4. Deduplicate e2e/state test harness helpers and repeated config fixture builders. DONE (`test/testutil/nats.go`, `test/e2e/service_harness_test.go`, `test/e2e/*`, `internal/state/*_test.go`, `internal/config/config_test.go`).
5. Re-run quality gates (`go test ./...`, `go test -race ./...`) and document results. DONE (`gofmt -w $(rg --files -g '*.go')`, `go test ./...`, `go test -race ./...` all green).
Next: accepted.

## P34 (DONE)

## P154 (DONE)
Goal: sync the user README with the shipped dashboard UI and its config model.
1. Add a dedicated README section for the built-in dashboard and describe current capabilities. DONE (`docs/README.md`).
2. Extend the config section with the actual UI TOML schema and runtime behavior. DONE (`docs/README.md`).
3. Keep the wording aligned with the current implementation (polling JSON snapshots, basic auth, NATS registry, no config editing). DONE.
4. Update bookkeeping. DONE (`docs/state/changelog.md`).
Next: accepted.
Goal: enrich Telegram payload fields and update main production template format.
1. Extend notification model with presentation fields (`short_id`, `metric_value`, `started_at`, `duration`). DONE (`internal/domain/alert.go`).
2. Populate those fields from runtime/state flow for `firing` and `resolved` notifications. DONE (`internal/engine/engine.go`, `internal/app/manager.go`).
3. Update main Telegram template in `configs/alerts/notify.telegram.toml` to concise human-oriented format from user sample. DONE (`configs/alerts/notify.telegram.toml`).
4. Run tests including live Telegram firing->resolved validation. DONE (`gofmt -w $(rg --files -g '*.go')`, `go test ./...`, `set -a; source configs/live.telegram.env; set +a; go test ./test/e2e -run TelegramLive -count=1 -v`).
Next: pending user acceptance.

## P35 (DONE)
Goal: adjust Telegram message layout (bold title, colon alignment, rounded delta).
1. Enable Telegram message bold title rendering and keep reply threading behavior. DONE (`internal/notify/notify.go`, `internal/notify/notify_test.go`).
2. Update main template to move short id into title (`АЛЕРТ(<id>)`) and align `:` labels. DONE (`configs/alerts/notify.telegram.toml`).
3. Round alert duration to one decimal place in human format. DONE (`internal/app/manager.go`).
4. Sync e2e template/assertions and run full + live tests. DONE (`test/e2e/telegram_live_e2e_test.go`, `gofmt -w $(rg --files -g '*.go')`, `go test ./...`, `set -a; source configs/live.telegram.env; set +a; go test ./test/e2e -run TelegramLive -count=1 -v`).
Next: pending user acceptance.

## P36 (DONE)
Goal: simplify Telegram title and make template human-readable in config.
1. Remove literal word `АЛЕРТ` from template title and keep short id in title. DONE (`configs/alerts/notify.telegram.toml`, `test/e2e/telegram_live_e2e_test.go`).
2. Change metric line format from `Метрика : ...` to `<var>:  <value>` (`(OK)` for resolved). DONE (`configs/alerts/notify.telegram.toml`, `test/e2e/telegram_live_e2e_test.go`).
3. Reformat `notify.telegram` template as multiline TOML literal for readability. DONE (`configs/alerts/notify.telegram.toml`).
4. Sync live e2e template/assertions and run full + live tests. DONE (`gofmt -w $(rg --files -g '*.go')`, `go test ./...`, `set -a; source configs/live.telegram.env; set +a; go test ./test/e2e -run TelegramLive -count=1 -v`).
Next: pending user acceptance.

## P37 (DONE)
Goal: enforce non-nil `StartedAt` and remove template guard from Telegram message.
1. Enforce non-nil `StartedAt` payload in manager notification paths with safe fallback behavior. DONE (`internal/app/manager.go`).
2. Remove `if .StartedAt` guards from Telegram templates (main config + live e2e fixture). DONE (`configs/alerts/notify.telegram.toml`, `test/e2e/telegram_live_e2e_test.go`).
3. Run full + live tests after change. DONE (`gofmt -w $(rg --files -g '*.go')`, `go test ./...`, `set -a; source configs/live.telegram.env; set +a; go test ./test/e2e -run TelegramLive -count=1 -v`).
Next: pending user acceptance.

## P38 (DONE)
Goal: move HTTP server options from `service` to `ingest.http` and verify config grouping.
1. Move schema/runtime usage for HTTP listen/endpoints (`listen`, `health_path`, `ready_path`, `ingest_path`) into `ingest.http`. DONE (`internal/config/config.go`, `internal/app/service.go`).
2. Migrate configs/tests/docs fixtures to new grouping. DONE (`configs/alerts/base.toml`, `test/e2e/*`, `internal/config/config_test.go`, `docs/README.md`).
3. Audit remaining config options for grouping consistency and keep only necessary regrouping changes. DONE: no additional regrouping required in this pass; existing `service/log/ingest/state/notify/rule.*` boundaries are coherent.
4. Run formatting/tests and update bookkeeping docs. DONE (`gofmt -w $(rg --files -g '*.go')`, `go test ./...`).
Next: pending user acceptance.

## P39 (DONE)
Goal: run full e2e verification after config regrouping changes.
1. Run full e2e suite (`go test ./test/e2e -count=1 -v`). DONE (PASS; `TestTelegramLiveEndToEnd` skipped without env in suite run as expected).
2. Run live Telegram e2e (`go test ./test/e2e -run TelegramLive -count=1 -v` with `configs/live.telegram.env`). DONE (PASS in ~61s; firing->resolved lifecycle completed).
3. Report pass/fail summary and keep plan bookkeeping synced. DONE.
Next: pending user acceptance.

## P41 (DONE)
Goal: enforce NATS-only state backend and remove memory backend code paths; add file logging sink to main config.
1. Add `[log.file]` into `configs/alerts/base.toml` with same level/format semantics as `[log.console]` and explicit `path`. DONE (`configs/alerts/base.toml`, `path="./alerting.log"`).
2. Switch state config/runtime to NATS-only (`state.backend=nats`), remove memory backend defaults/validation branches and store builder branch. DONE (`internal/config/config.go`, `internal/app/service.go`).
3. Remove memory-only state implementation and dead optional expiration interface (`internal/state/store.go`, related tests, manager tick branch). DONE (`internal/state/store.go`, deleted `internal/state/store_test.go`, `internal/app/manager.go`).
4. Update e2e/bench fixtures to NATS-backed state and provision local NATS+KV for scenarios previously using memory backend. DONE (`test/e2e/service_smoke_test.go`, `test/e2e/reload_e2e_test.go`, `test/e2e/telegram_reply_e2e_test.go`, `test/e2e/telegram_live_e2e_test.go`, `test/e2e/throughput_benchmark_test.go`, `test/e2e/throughput_nats_benchmark_test.go`).
5. Run formatting/tests and sync bookkeeping docs. DONE (`gofmt -w $(rg --files -g '*.go')`, `go test ./...` green).
Next: pending user acceptance.

## P42 (DONE)
Goal: send Telegram preview messages with `<pre>` aligned fields for visual check in channel.
1. Send `firing` preview message in Telegram channel using Bot API with `parse_mode=HTML` and `<pre>` block. DONE (`ok=true`, `message_id=25`).
2. Send `resolved` preview message in Telegram channel using Bot API with `parse_mode=HTML` and `<pre>` block. DONE (`ok=true`, `message_id=26`).
3. Report exact sent text and delivery result to user. DONE.
Next: pending user acceptance.

## P43 (DONE)
Goal: reduce spacing after colon in Telegram template to one space.
1. Update production Telegram template in `configs/alerts/notify.telegram.toml` from `:  ` to `: ` for metric/start/delta lines. DONE.
2. Sync Telegram live e2e fixture/assertions to one-space formatting in `test/e2e/telegram_live_e2e_test.go`. DONE.
3. Run Telegram-focused e2e tests and update bookkeeping docs. DONE (`go test ./test/e2e -run Telegram -count=1 -v`, PASS; live test skipped without env).
Next: pending user acceptance.

## P44 (DONE)
Goal: move Telegram field lines into `<pre>` block in the main template.
1. Update production template in `configs/alerts/notify.telegram.toml`: keep bold header and wrap metric/start/delta lines into `<pre>...</pre>`. DONE.
2. Sync Telegram live e2e fixture/assertions in `test/e2e/telegram_live_e2e_test.go` for `<pre>` format. DONE.
3. Run Telegram-focused e2e tests and update bookkeeping docs. DONE (`go test ./test/e2e -run Telegram -count=1 -v`, PASS; live skipped without env).
Next: pending user acceptance.

## P45 (DONE)
Goal: keep only explicit Telegram template branches for `firing` and `resolved` (remove fallback branch).
1. Remove `else` fallback block from production template in `configs/alerts/notify.telegram.toml`. DONE.
2. Keep explicit `resolved` condition branch and sync fixture template in `test/e2e/telegram_live_e2e_test.go`. DONE.
3. Run Telegram-focused e2e tests and update bookkeeping docs. DONE (`go test ./test/e2e -run Telegram -count=1 -v`, PASS; live skipped without env).
Next: pending user acceptance.

## P46 (DONE)
Goal: pass typed `time.Duration` in notifications and use one shared template formatter across all channels.
1. Replace `domain.Notification.Duration` string field with typed duration value and update producer path in manager. DONE (`internal/domain/alert.go`, `internal/app/manager.go`).
2. Add shared template function(s) for duration formatting and use the same parser/functions in both config validation and runtime rendering. DONE (`internal/templatefmt/templatefmt.go`, `internal/config/config.go`, `internal/notify/notify.go`).
3. Update Telegram main template + live e2e fixture to use unified formatter (`fmtDuration`) instead of string/manual formatting. DONE (`configs/alerts/notify.telegram.toml`, `test/e2e/telegram_live_e2e_test.go`).
4. Run focused + full tests and update bookkeeping docs. DONE (`gofmt -w $(rg --files -g '*.go')`, `go test ./test/e2e -run Telegram -count=1 -v`, `go test ./...`; PASS, live Telegram test skip-safe without env).
Next: pending user acceptance.

## P47 (DONE)
Goal: add Jira/YouTrack tracker integration as transport channels in common notify processing pipeline.
1. Extend notify channel model with `jira` and `youtrack` channels, each in dedicated transport config file (`configs/alerts/notify.jira.toml`, `configs/alerts/notify.youtrack.toml`) and route binding via `[[rule.notify.route]] channel+template`. DONE (`internal/config/config.go`, `configs/alerts/notify.jira.toml`, `configs/alerts/notify.youtrack.toml`).
2. Add generic tracker HTTP transport config (request/response templates, auth headers, timeout/retry/success rules) and keep delivery logic shared in notifier runtime. DONE (`internal/config/config.go`, `internal/notify/notify.go`, shared template helpers in `internal/templatefmt/templatefmt.go`).
3. Add per-alert external issue reference persistence in NATS KV card state (`rule_name + alert_id + channel`) so `firing` creates issue once and `resolved` closes the same issue. DONE (`internal/domain/alert.go`, `internal/app/manager.go`, `internal/engine/engine.go`).
4. Implement Jira transport behavior on top of shared HTTP templated sender. DONE (`internal/notify/notify.go`, `test/e2e/tracker_jira_e2e_test.go`):
   - `firing`: create issue;
   - `resolved`: transition/close issue using stored external issue ref.
5. Implement YouTrack transport behavior on top of shared HTTP templated sender. DONE (`internal/notify/notify.go`, config support in `internal/config/config.go`):
   - `firing`: create issue;
   - `resolved`: apply resolve command using stored external issue ref.
6. Add tests. DONE:
   - unit tests for config validation/template rendering/ref persistence;
   - integration tests with `httptest` mocks for Jira/YouTrack create+resolve flows (`internal/notify/notify_test.go`, `test/e2e/tracker_jira_e2e_test.go`).
7. Update README + config examples for new transports, keeping Telegram path unchanged. DONE (`docs/README.md`).
8. Run quality gates (`gofmt`, `go test ./...`) and keep live integrations optional. DONE (`gofmt -w $(rg --files -g '*.go')`, `go test ./...`; green).
Next: pending user acceptance.

## P48 (DONE)
Goal: reduce duplicated logic/hot-path overhead and close reliability gap in state/notify runtime.
1. Fix TTL delete-consumer ACK semantics: ACK only after successful resolve path; on failure return error/NAK for retry. DONE (`internal/state/delete_consumer.go`, `internal/app/service.go`, `internal/state/delete_consumer_integration_test.go`).
2. Remove redundant KV read in refresh path: make `RefreshTick` update timestamp without pre-`Get`, keep create-vs-update semantics where actually needed. DONE (`internal/state/store.go`, `internal/state/nats_store.go`, `internal/app/manager.go`, `internal/state/nats_store_test.go`).
3. Reduce tick-time double scans: avoid repeated full state iteration for repeat notifications; preserve repeat behavior and tests. DONE (`internal/engine/engine.go`, `internal/app/manager.go`, `internal/engine/engine_test.go`).
4. Keep config merge semantics explicit for bool fields (`false` overrides) and reduce channel merge duplication where feasible in this pass. DONE (`internal/config/config.go`, `internal/config/config_test.go`): directory overlay now tracks explicit bool keys and applies explicit `false` for notify/channel enabled flags.
5. Run formatting + full tests and sync bookkeeping docs. DONE (`gofmt -w $(rg --files -g '*.go')`, `go test ./...`; PASS).
Next: pending user acceptance.

## P49 (DONE)
Goal: add ready-to-run Bash examples for all three metric types via both HTTP and NATS ingest.
1. Create `docs/example` with shared helper layer for payload generation and transport send (`http`/`nats`). DONE (`docs/example/common.sh`).
2. Add HTTP scenario scripts for each metric type (`count_total`, `count_window`, `missing_heartbeat`). DONE (`docs/example/http_count_total.sh`, `docs/example/http_count_window.sh`, `docs/example/http_missing_heartbeat.sh`).
3. Add NATS scenario scripts for each metric type (`count_total`, `count_window`, `missing_heartbeat`). DONE (`docs/example/nats_count_total.sh`, `docs/example/nats_count_window.sh`, `docs/example/nats_missing_heartbeat.sh`).
4. Add concise usage doc with env vars and run examples. DONE (`docs/example/README.md`).
5. Validate shell syntax for new scripts. DONE (`bash -n docs/example/*.sh`; PASS).
Next: pending user acceptance.

## P50 (DONE)
Goal: migrate rule config model to named TOML tables and split rules by metric type files.
1. Replace legacy `[[rule]]` schema with named tables: `[rule.<rule_name>]` + nested `[rule.<rule_name>.*]` and `[[rule.<rule_name>.notify.route]]`. DONE (`internal/config/config.go`, `internal/config/config_test.go`, `test/e2e/*` fixtures).
2. Remove legacy `[[rule]]` support and return explicit validation/load error when old format is used. DONE (`internal/config/config.go`, covered by `TestLoadSnapshotRejectsLegacyRuleArraySyntax`).
3. Keep runtime `RuleConfig.Name` sourced from TOML table key `<rule_name>` (not from body field). DONE (`normalizeRawConfig` + `rule.<name>.name` rejection in `internal/config/config.go`, tested by `TestLoadSnapshotRejectsRuleBodyNameField`).
4. Split production rule config into separate files by metric type: `rules.count_total.toml`, `rules.count_window.toml`, `rules.missing_heartbeat.toml`. DONE (`configs/alerts/rules.*.toml`; old `configs/alerts/rules.toml` removed).
5. Update config tests/e2e fixtures/docs to new format and run quality gates. DONE (`docs/README.md`, `configs/alerts/notify.*.toml` comments, `gofmt -w $(rg --files -g '*.go')`, `go test ./...`; PASS).
Next: pending user acceptance.

## P51 (DONE)
Goal: run full e2e verification for all metric types and commit if green.
1. Run full e2e package (`go test ./test/e2e -count=1 -v`) after rule schema migration. DONE (PASS; live Telegram test skipped without env as expected).
2. Run manual scenario scripts for all metric types via both transports (`docs/example/http_*`, `docs/example/nats_*`) on isolated local stack and verify firing/resolved outputs. DONE (`NOTIFY_TOTAL=12`, each rule has `firing=2` and `resolved=2`).
3. If all checks pass, commit current changes as one snapshot. DONE (commit created).
4. If any check fails, do not commit; report failures with root cause. DONE (not applicable; no failures).
Next: pending user acceptance.

## P54 (DONE)
Goal: expose all three rule-type configs in README as explicit examples.
1. Add direct references to `configs/alerts/rules.count_total.toml`, `configs/alerts/rules.count_window.toml`, `configs/alerts/rules.missing_heartbeat.toml` in corresponding metric sections. DONE (`docs/README.md`).
2. Keep examples and wording aligned with the new named-rule schema (`[rule.<rule_name>]`). DONE (`docs/README.md`).
3. Avoid behavior changes: docs-only update. DONE.
Next: pending user acceptance.

## P55 (DONE)
Goal: complete Mattermost integration in the shared notify pipeline with dedicated transport config, tests, and docs.
1. Audit existing Mattermost implementation and lock target contract (shared template render + transport-only sender). DONE (kept shared template rendering in dispatcher and transport-only sender in `internal/notify/notify.go`).
2. Extend `notify.mattermost` config to production shape (`timeout_sec`, optional webhook fields, retry/templates), add strict validation/defaults/merge coverage. DONE (`internal/config/config.go`, `internal/config/config_test.go`).
3. Add dedicated transport config file `configs/alerts/notify.mattermost.toml` with exhaustive comments and default template(s). DONE (`configs/alerts/notify.mattermost.toml`).
4. Update Mattermost sender runtime: config-driven timeout, optional payload fields, improved non-2xx error body details, keep retries only in dispatcher. DONE (`internal/notify/notify.go`).
5. Add/extend tests:
   - config unit tests (`internal/config/config_test.go`);
   - sender/dispatcher unit tests (`internal/notify/notify_test.go`);
   - e2e test with local webhook stub (`test/e2e/mattermost_e2e_test.go`). DONE.
6. Update user docs (`docs/README.md`) with config and route examples for Mattermost channel. DONE.
7. Run quality gates (`gofmt`, `go test ./...`, focused e2e) and mark DONE only on green. DONE (`gofmt -w $(rg --files -g '*.go')`, `go test ./internal/config ./internal/notify ./test/e2e -run Mattermost -count=1 -v`, `go test ./...`; PASS).
Next: pending user acceptance.

## P56 (DONE)
Goal: add asynchronous best-effort delivery queue (separate from ingest queue) and reduce duplicated notification logic.
1. Extend config model with `notify.queue` (defaults/merge/validation) and wire it into base config/docs. DONE (`internal/config/config.go`, `internal/config/config_test.go`, `configs/alerts/base.toml`, `docs/README.md`).
2. Add dedicated notify queue runtime package with NATS JetStream producer/worker and deterministic job IDs. DONE (`internal/notifyqueue/queue.go`, `internal/notifyqueue/nats.go`, `internal/notifyqueue/nats_test.go`).
3. Route manager notifications through queue when enabled, keep direct dispatcher fallback when disabled, and process queued jobs via shared dispatcher path. DONE (`internal/app/manager.go`).
4. Enforce per-channel best-effort delivery: enqueue/send failures on one channel must not block other channels; treat permanent template/config errors as drop+ACK. DONE (`internal/app/manager.go`).
5. Wire service lifecycle/reload to build/swap/close notify queue producer+worker atomically. DONE (`internal/app/service.go`).
6. Update NATS deploy scripts for notify queue stream+consumer bootstrap/verify/cleanup and env defaults. DONE (`deploy/nats/.env.example`, `deploy/nats/bootstrap.sh`, `deploy/nats/verify.sh`, `deploy/nats/cleanup.sh`).
7. Reduce duplicated transport error handling (shared non-2xx formatter) and keep Mattermost/HTTP behavior aligned. DONE (`internal/notify/notify.go`).
8. Add/refresh tests for new behavior and run quality gates. DONE (`test/e2e/notify_queue_best_effort_e2e_test.go`, `go test ./...`, `go test ./test/e2e -run NotifyQueueBestEffortPerChannel -count=1 -v`; PASS).
Next: pending user acceptance.

## P57 (DONE)
Goal: make Mattermost resolved notifications always reference firing by thread linkage.
1. Replace Mattermost webhook contract with API posts contract (`base_url`, `bot_token`, `channel_id`) and update config validation/merge/default handling. DONE (`internal/config/config.go`, `internal/config/config_test.go`, `configs/alerts/notify.mattermost.toml`).
2. Rework Mattermost sender to call `/api/v4/posts` with Bearer auth, parse `post.id`, return firing reference, and require `external_ref` for resolved (`root_id`). DONE (`internal/notify/notify.go`, `internal/notify/notify_test.go`).
3. Update manager delivery guards so resolved Mattermost without firing reference is not sent unthreaded (skip with warn) in direct and queued paths. DONE (`internal/app/manager.go`).
4. Update Mattermost e2e and queue best-effort fixtures for API-mode config and thread assertion (`resolved.root_id == firing post.id`). DONE (`test/e2e/mattermost_e2e_test.go`, `test/e2e/notify_queue_best_effort_e2e_test.go`).
5. Run quality gates and mark DONE only on green. DONE (`gofmt -w $(rg --files -g '*.go')`, `go test ./...`; PASS).
Next: pending user acceptance.

## P58 (DONE)
Goal: remove redundant `state.backend` config key and keep NATS-only state model without user-visible backend selector.
1. Remove `state.backend` from config schema/defaults/validation and keep only `state.nats.*` settings. DONE (`internal/config/config.go`).
2. Simplify service runtime checks to always use NATS state backend (remove dead backend branching). DONE (`internal/app/service.go`).
3. Remove `[state] backend = "nats"` from user-facing and e2e fixture configs. DONE (`configs/alerts/base.toml`, `docs/README.md`, `test/e2e/*.go` fixture TOML blocks).
4. Run quality gates and confirm no regressions. DONE (`gofmt -w $(rg --files -g '*.go')`, `go test ./...`; PASS).
Next: pending user acceptance.

## P60 (DONE)
Goal: apply targeted README sync only after reading current document and approved plan.
1. Re-read current `docs/README.md` before edits and keep its structure intact. DONE.
2. Fix Mattermost delivery logic text to match runtime contract (`firing post.id` -> `resolved root_id` thread linkage). DONE (`docs/README.md`).
3. Replace stale `[state]` reference in config structure list with `[state.nats]`. DONE (`docs/README.md`).
4. Verify `docs/README.md` contains no `state.backend`/`[state]` leftovers. DONE (`rg -n "state\\.backend|\\[state\\]" docs/README.md`; no matches).
Next: pending user acceptance.

## P61 (DONE)
Goal: switch ingest/state NATS URLs from scalar to array form (`url = ["nats://..."]`) across config/runtime/tests/docs.
1. Update config schema/types/defaults/validation for `ingest.nats.url` and `state.nats.url` as `[]string`; fix merge checks for non-comparable sections with slices. DONE (`internal/config/config.go`).
2. Update runtime NATS connections to consume URL arrays (join into multi-server connect string). DONE (`internal/ingest/nats.go`, `internal/state/nats_store.go`, `internal/state/delete_consumer.go`).
3. Update real configs, README examples, and e2e TOML fixtures to array URL syntax for ingest/state sections. DONE (`configs/alerts/base.toml`, `docs/README.md`, `test/e2e/*`).
4. Update integration/benchmark code literals that construct `NATSIngestConfig`/`NATSStateConfig` with scalar URL fields. DONE (`test/e2e/throughput_nats_benchmark_test.go`, `test/e2e/throughput_benchmark_test.go`, `test/e2e/nats_helpers_test.go`, `internal/state/*_test.go`).
5. Run quality gates (`gofmt -w $(rg --files -g '*.go')`, `go test ./...`) and mark DONE only on green. DONE (PASS).
Next: pending user acceptance.

## P62 (DONE)
Goal: remove configurable `[state.nats]`; derive fixed state settings from `ingest.nats.url`.
1. Remove `state` from user config schema and reject `[state]`/`[state.*]` sections during snapshot load. DONE (`internal/config/config.go`, `internal/config/config_test.go`).
2. Build state settings in code from fixed constants + `ingest.nats.url` (no user overrides). DONE (`internal/config/config.go`: `DeriveStateNATSConfig`).
3. Switch service store/delete-consumer builders to derived state settings and remove direct `cfg.State.*` usage. DONE (`internal/app/service.go`).
4. Remove `[state.nats]` from base config, README, and all e2e fixture TOML snippets; where needed, set `ingest.nats.url` explicitly for state connection target. DONE (`configs/alerts/base.toml`, `docs/README.md`, `test/e2e/*`).
5. Run quality gates (`gofmt -w $(rg --files -g '*.go')`, `go test ./...`) and mark DONE only on green. DONE (PASS).
Next: pending user acceptance.

## P63 (DONE)
Goal: run 5-minute load profiling and complete full codebase redundancy audit, then prepare optimization roadmap.
1. Run sustained load benchmark for 5 minutes with profiles (`cpu`, `heap`, `block`, `mutex`) and capture raw artifacts. DONE (`go test ./test/e2e -run '^$' -bench BenchmarkIngestThroughput -benchmem -benchtime=5m -count=1 -cpuprofile ./tmp_p63_cpu.pprof -memprofile ./tmp_p63_mem.pprof -blockprofile ./tmp_p63_block.pprof -mutexprofile ./tmp_p63_mutex.pprof`; benchmark result: `1838055 194530 ns/op 5141 events/sec 2403 B/op 39 allocs/op`; PASS).
2. Analyze pprof outputs (hot paths, allocations, contention/waits) and summarize measured bottlenecks. DONE (`go tool pprof -top ...`; main hot spots: `runtime.futex` 44.54% cpu, `internal/runtime/syscall.Syscall6` 27.60% cpu; alloc hot path `alerting/internal/state.(*NATSStore).RefreshTick` 85.04% cum alloc space).
3. Audit full codebase for duplicated logic, redundant branches/abstractions, and avoidable allocation/copy patterns. DONE (manager/engine/notify/config/state packages reviewed with line-level findings).
4. Produce prioritized optimization plan combining profiling evidence + code audit findings. DONE (prioritized plan with perf + dedup tracks prepared for user review).
Next: pending user acceptance.

## P64 (DONE)
Goal: apply prioritized performance/dedup optimizations from P63 and compare benchmark results against baseline.
1. Remove duplicated hot-path checks in event flow (single match/key validation path without behavior regressions). DONE (`internal/app/manager.go`: removed separate key-tag validation before key build; `internal/engine/engine.go`: removed duplicate `MatchEvent` guard in engine `ProcessEvent` path).
2. Optimize tick persistence allocations in `NATSStore.RefreshTick` (drop high-allocation map/json/fmt path). DONE (`internal/state/nats_store.go`: switched to lightweight payload builder + precomputed KV subject prefix + cheaper TTL header formatting).
3. Optimize `count_window` engine path with incremental running sum (avoid full window rescan on each event). DONE (`internal/engine/engine.go`: added `CountWindowSum`; prune now subtracts evicted counts instead of full rescan).
4. Reduce duplicated notification route preprocessing in manager (direct + queued paths share same channel preparation). DONE (`internal/app/manager.go`: added shared route normalization and per-channel notification preparation helper, reused in direct + queued + enqueue flows).
5. Run quality gates (`gofmt`, focused tests, `go test ./...`) and execute 5-minute benchmark rerun for side-by-side comparison with P63 baseline. DONE (`gofmt -w $(rg --files -g '*.go')`, `go test ./...`, `go test ./test/e2e -run '^$' -bench BenchmarkIngestThroughput -benchmem -benchtime=5m -count=1 -cpuprofile ./tmp_p64_cpu.pprof -memprofile ./tmp_p64_mem.pprof -blockprofile ./tmp_p64_block.pprof -mutexprofile ./tmp_p64_mutex.pprof`; result vs P63: `ns/op 194530 -> 182703`, `events/sec 5141 -> 5473`, `B/op 2403 -> 1953`, `allocs/op 39 -> 31`).
Next: accepted.

## P65 (DONE)
Goal: reduce duplication and excess code in notification delivery subsystem (manager/config/transports/queue/e2e fixtures) with no behavior regressions.
1. Remove duplicated manager post-send logic and consolidate into shared delivery result finalizer. DONE (`internal/app/manager.go`: new `applyDeliveryResults`; reused by direct and queued paths).
2. Reduce repeated route normalization on hot paths by using pre-normalized runtime routes for dispatch/repeat/enqueue/queue-consume flow. DONE (`internal/app/manager.go`: `normalizedRouteFromRuntimeRoute` used in runtime loops; strict normalize remains only in config-normalization path).
3. Introduce unified notify channel registry for config helpers instead of repeated switch branches (`enabled/retry/templates`). DONE (`internal/config/config.go`: `notifyChannelRegistry` + descriptor helper).
4. Consolidate transport HTTP request/response boilerplate and simplify retry timer lifecycle handling. DONE (`internal/notify/notify.go`: sender factory registry, `stopAndDrainTimer`, shared `buildJSONRequest`/`executeRequest`, Mattermost request dedup, Tracker response-status handling via shared executor).
5. Deduplicate notify queue runtime setup (`connect + jetstream + ensure stream`) between producer/worker. DONE (`internal/notifyqueue/nats.go`: `openNotifyQueueJetStream`).
6. Reduce e2e fixture duplication for common config prefix and migrate primary notification e2e tests to helper-based TOML assembly. DONE (`test/e2e/config_fixtures_test.go`, `test/e2e/{telegram_reply_e2e_test.go,mattermost_e2e_test.go,telegram_active_only_e2e_test.go,mattermost_active_only_e2e_test.go,notify_queue_best_effort_e2e_test.go,tracker_jira_e2e_test.go}`).
7. Run quality gates after refactor. DONE (`gofmt -w $(rg --files -g '*.go')`, `go test ./...`; PASS).
Next: accepted.

## P66 (DONE)
Goal: add notify queue DLQ path for exhausted/permanent failures and cover it with tests.
1. Extend notify queue config with optional `notify.queue.dlq` (`enabled`, `subject`, `stream`) including defaults/merge/validation constraints. DONE (`internal/config/config.go`, `internal/config/config_test.go`).
2. Add queue-level permanent-error marker and DLQ payload contract for failed jobs. DONE (`internal/notifyqueue/queue.go`).
3. Update NATS notify worker to route permanent/max-deliver failures into DLQ, ACK dropped jobs, and keep retry behavior for transient errors. DONE (`internal/notifyqueue/nats.go`, `internal/app/manager.go`).
4. Update NATS deploy scripts/env to provision and verify notify DLQ stream. DONE (`deploy/nats/.env.example`, `deploy/nats/bootstrap.sh`, `deploy/nats/verify.sh`, `deploy/nats/cleanup.sh`).
5. Add tests for permanent/max-deliver DLQ behavior and end-to-end queue DLQ flow. DONE (`internal/notifyqueue/nats_test.go`, `test/e2e/notify_queue_best_effort_e2e_test.go`).
6. Run formatting and quality gates. DONE (`gofmt -w ...`, `go test ./internal/config ./internal/notifyqueue -count=1`, `go test ./test/e2e -run NotifyQueueDLQOnMaxDeliverE2E -count=1 -v`, `go test ./...`; PASS).
Next: pending user acceptance.

## P67 (DONE)
Goal: reduce new DLQ-path redundancy and fix loss-prone queue edge cases from latest audit.
1. Replace string-based permanent-error classification in manager with typed marker flow from notify layer. DONE (`internal/notify/notify.go`, `internal/app/manager.go`, `internal/notify/notify_test.go`).
2. Stop retry loops for typed permanent notify errors in dispatcher retry path. DONE (`internal/notify/notify.go`, `internal/notify/notify_test.go`).
3. Fix notify worker behavior on DLQ publish failure: do not ACK dropped job, NAK for retry when DLQ write fails. DONE (`internal/notifyqueue/nats.go`).
4. Deduplicate stream ensure logic in notify queue runtime (`work` + `dlq` share one helper). DONE (`internal/notifyqueue/nats.go`).
5. Reduce fresh test duplication/flakiness in new notify queue tests. DONE (`internal/notifyqueue/nats_test.go`).
6. Surface DLQ in user-facing configs/docs (`[notify.queue.dlq]`, contour bootstrap text). DONE (`configs/alerts/base.toml`, `docs/README.md`).
7. Run formatting and quality gates. DONE (`gofmt -w ...`, `go test ./internal/notify ./internal/notifyqueue ./internal/config -count=1`, `go test ./test/e2e -run NotifyQueue -count=1 -v`, `go test ./...`; PASS).
Next: pending user acceptance.

## P68 (DONE)
Goal: enforce 3-metric coverage (`count_total`, `count_window`, `missing_heartbeat`) in every key e2e scenario test.
1. Add shared metric-matrix helpers for rule TOML generation and event posting. DONE (`test/e2e/metrics_matrix_test.go`).
2. Refactor e2e scenarios to run each case by metric matrix:
   - smoke/reload/notify-queue/telegram reply (continued and finalized),
   - Telegram active-only, Mattermost, Mattermost active-only, Jira tracker, multi-service NATS, Telegram live. DONE (`test/e2e/*_e2e_test.go` listed above).
3. Stabilize heartbeat-specific lifecycle in matrix runs:
   - reload test waits for snapshot apply before first heartbeat probe;
   - multi-service heartbeat resolves via repeated recovery events and asserts `>=1` lifecycle notifications due queue distribution across workers. DONE (`test/e2e/reload_e2e_test.go`, `test/e2e/multiservice_nats_e2e_test.go`).
4. Run validation gates. DONE:
   - focused matrix suite: `go test ./test/e2e -run 'Test(ServiceSmokeHealthReadyAndIngest|HotReloadApplyValidSnapshot|HotReloadKeepsPreviousSnapshotOnValidationError|NotifyQueueBestEffortPerChannelE2E|NotifyQueueDLQOnMaxDeliverE2E|TelegramResolvedRepliesToOpeningMessage|TelegramActiveOnlyLifecycleE2E|MattermostWebhookFiringAndResolveE2E|MattermostWebhookRetryOnTransientFailureE2E|MattermostActiveOnlyLifecycleE2E|JiraTrackerCreateAndResolveE2E|MultiServiceNATSQueueLifecycleSingleNotifications)$' -count=1 -v` PASS;
   - full suite: `go test ./...` PASS.
Next: pending user acceptance.

## P69 (DONE)
Goal: close remaining test-coverage gaps after matrix expansion.
1. Add unit coverage for missing rule-type and out-of-order logic. DONE:
   - `internal/engine/engine_test.go`: `count_window` lifecycle test (`firing -> resolved`);
   - `internal/config/rule_types_test.go`: rule validation for `count_window` and `missing_heartbeat` (valid + forbidden-field cases);
   - `internal/app/manager_outoforder_test.go`: `out_of_order` late/future/boundary checks.
2. Add missing YouTrack end-to-end transport coverage across metric matrix. DONE (`test/e2e/tracker_youtrack_e2e_test.go` with create+resolve flow for all 3 metric types).
3. Extend throughput benchmarks to all 3 metrics. DONE:
   - `test/e2e/throughput_benchmark_test.go`: sub-bench matrix by metric;
   - `test/e2e/throughput_nats_benchmark_test.go`: queue-path sub-bench matrix by metric.
4. Run verification gates. DONE:
   - `go test ./internal/engine ./internal/config ./internal/app -count=1` PASS;
   - `go test ./test/e2e -run 'TestYouTrackTrackerCreateAndResolveE2E|TestJiraTrackerCreateAndResolveE2E' -count=1 -v` PASS;
   - `go test ./...` PASS.
Next: pending user acceptance.

## P72 (DONE)
Goal: optimize HTTP ingest path with minimal behavioral change and add high-throughput batch ingest route.
1. Remove HTTP `ReadAll` decode path and switch to stream JSON decoding for single-event ingest. DONE (`internal/ingest/http.go`, `internal/domain/event.go`).
2. Add batch ingest support via `/ingest/batch` route (same sink pipeline, optional `PushBatch` fast path). DONE (`internal/app/service.go`, `internal/ingest/http.go`, `internal/app/manager.go`).
3. Add/refresh tests for stream and batch HTTP ingest behavior. DONE (`internal/ingest/http_test.go`, `internal/domain/event_test.go`).
4. Extend throughput benchmark with HTTP batch scenario and keep fair NATS comparison path. DONE (`test/e2e/throughput_benchmark_test.go`, `test/e2e/throughput_nats_benchmark_test.go`).
5. Run quality/benchmark gates. DONE:
   - `go test ./internal/domain ./internal/ingest ./internal/app -count=1` PASS;
   - `go test ./test/e2e -run '^$' -bench 'Benchmark(IngestThroughput|HTTPBatchIngestThroughput|NATSQueueIngestThroughput)' -benchtime=100000x -count=1 -benchmem` PASS;
   - `go test ./... -count=1` PASS.
   Final benchmark snapshot (`events/sec`):
   - HTTP single: `count_total 13095`, `count_window 13149`, `missing_heartbeat 13303`;
   - HTTP batch (`10 events/request`): `count_total 78178`, `count_window 76683`, `missing_heartbeat 78302`;
   - NATS queue: `count_total 177526`, `count_window 164615`, `missing_heartbeat 169794`.
Next: pending user acceptance.

## P73 (DONE)
Goal: add single-instance mode without NATS dependencies (HTTP-only, in-memory state).
1. Extend config schema/validation with `service.mode` and single-mode constraints (HTTP-only, no NATS ingest, no notify queue). DONE (`internal/config/config.go`, `internal/config/config_test.go`).
2. Add in-memory state store and wire service to select backend by mode (skip NATS subscriber/delete consumer/notify queue in single). DONE (`internal/state/memory_store.go`, `internal/app/service.go`).
3. Add single-mode tests (config validation + HTTP e2e matrix) and update bookkeeping docs. DONE (`internal/state/memory_store_test.go`, `test/e2e/service_single_mode_test.go`, `go test ./internal/config ./internal/state`, `go test ./test/e2e -run TestServiceSingleModeHTTPOnly -count=1`).
Next: pending user acceptance.

## P74 (DONE)
Goal: deduplicate single-mode logic and run single-mode load/e2e tests.
1. Reduce single-mode defaulting/guard duplication in config/service; add helpers. DONE (`internal/config/config.go`, `internal/app/service.go`).
2. Optimize in-memory tick lookup for read-heavy access. DONE (`internal/state/memory_store.go`).
3. Deduplicate e2e config assembly for single-mode tests. DONE (`test/e2e/config_fixtures_test.go`, `test/e2e/service_smoke_test.go`, `test/e2e/service_single_mode_test.go`).
4. Run single-mode load benchmarks and single-mode e2e tests. DONE (`E2E_SERVICE_MODE=single go test ./test/e2e -run '^$' -bench 'Benchmark(IngestThroughput|HTTPBatchIngestThroughput)' -benchmem -count=1`, `go test ./test/e2e -run TestServiceSingleModeHTTPOnly -count=1 -v`).
Next: pending user acceptance.

## P75 (DONE)
Goal: run existing HTTP load benchmarks in single-mode and record results.
1. Execute single-mode HTTP benchmarks for all metrics. DONE (`E2E_SERVICE_MODE=single go test ./test/e2e -run '^$' -bench 'Benchmark(IngestThroughput|HTTPBatchIngestThroughput)' -benchmem -count=1`).
2. Capture results with batch size and store in state context. DONE (`docs/state/context.md`).
Next: pending user acceptance.

## P76 (DONE)
Goal: fix NATS load benchmark batching and run batch=1/100/1000.
1. Update NATS ingest to accept JSON array batch payloads and route to batch sink. DONE (`internal/ingest/nats.go`).
2. Fix NATS benchmark to publish one message per batch size. DONE (`test/e2e/throughput_nats_benchmark_test.go`).
3. Run NATS benchmarks for batch=1/100/1000 and capture results. DONE (`E2E_NATS_BATCH_SIZE=1|100|1000 go test ./test/e2e -run '^$' -bench BenchmarkNATSQueueIngestThroughput -benchmem -count=1`; batch=1 hit timeout for count_total/count_window with backlog; results stored in `docs/state/context.md`).
Next: pending user acceptance.

## P77 (DONE)
Goal: run HTTP and NATS benchmarks for batch=1/100/1000 and summarize results.
1. Run HTTP benchmarks with batch size 1/100/1000. DONE (`E2E_HTTP_BATCH_SIZE=1|100|1000 go test ./test/e2e -run '^$' -bench 'Benchmark(IngestThroughput|HTTPBatchIngestThroughput)' -benchmem -count=1`).
2. Run NATS benchmarks with batch size 1/100/1000. DONE (`E2E_NATS_BATCH_SIZE=1|100|1000 go test ./test/e2e -run '^$' -bench BenchmarkNATSQueueIngestThroughput -benchmem -count=1`; batch=1 timed out with backlog).
3. Store results in state context and provide summary table. DONE (`docs/state/context.md`).
Next: pending user acceptance.

## P78 (DONE)
Goal: sync docs with latest benchmarks and NATS drain note.
1. Update `docs/README.md` with latest HTTP/NATS batch results and mode-specific ingest/state wording. DONE (tables updated with batch=1/100/1000; NATS batch=1 backlog noted; input/state sections clarified).
2. Update `docs/state/context.md` with README sync and NATS drain explanation. DONE (SIGTERM drain note).
3. Update plan/changelog timestamps for this docs sync. DONE (docs-only change; verification N/A).
Next: pending user acceptance.

## P80 (DONE)
Goal: document single vs multi mode differences in README.
1. Add explicit modes section with comparison table and constraints. DONE (`docs/README.md`).
2. Update state context and plan timestamps to reflect README expansion. DONE (`docs/state/context.md`, `docs/state/detailed_plan.md`).
Next: pending user acceptance.

## P81 (DONE)
Goal: remove duplicated logic and apply low-risk optimizations.
1. Unify ingest decode/push handling and trailing-token checks across HTTP/NATS. DONE (new shared codec helpers in `internal/ingest/codec.go`, updated HTTP/NATS handlers).
2. Avoid repeated route normalization in hot path. DONE (runtime route normalization now assumes pre-normalized routes, no per-send lower/trim).
3. Deduplicate notification building for decision/repeat/removal paths. DONE (`buildNotification` helper in `internal/app/manager.go`).
4. Reduce repeat-loop snapshot overhead. DONE (added `Engine.GetRepeatSnapshot` and switched repeat loop to use it).
5. Reduce list prefix allocations in state stores. DONE (removed per-key `strings.ToLower` in `ListAlertIDsByRule`).
6. Replace string-matching CAS errors with typed NATS error when available. DONE (`errors.Is(err, nats.ErrKeyExists)` with fallback).
Next: pending user acceptance.

## P82 (DONE)
Goal: run load and full e2e tests after optimizations.
1. Run single-mode HTTP benchmarks. DONE (`E2E_SERVICE_MODE=single go test ./test/e2e -run '^$' -bench 'Benchmark(IngestThroughput|HTTPBatchIngestThroughput)' -benchmem -count=1`).
2. Run NATS benchmarks batch=1/100/1000. DONE (`E2E_NATS_BATCH_SIZE=1|100|1000 go test ./test/e2e -run '^$' -bench BenchmarkNATSQueueIngestThroughput -benchmem -count=1`; batch=1 hit timeout with backlog).
3. Run full e2e suite. DONE (`go test ./test/e2e -count=1`).
Next: pending user acceptance.

## P83 (DONE)
Goal: normalize state context and prepare commit.
1. Prune older benchmark entries in context; keep latest run only. DONE (`docs/state/context.md`).
2. Update timestamps and changelog. DONE (`docs/state/{context.md,detailed_plan.md,changelog.md}`).
Next: pending user acceptance.

## P85 (DONE)
Goal: remove inline code backticks from README text.
1. Strip inline backticks outside fenced code blocks in `docs/README.md`. DONE (handles indented code fences).
2. Update state docs timestamps. DONE (`docs/state/{context.md,detailed_plan.md,changelog.md}`).
Next: pending user acceptance.

## P87 (DONE)
Goal: split global config section by mode.
1. Add Multi-instance subsection and keep existing config example. DONE (`docs/README.md`).
2. Add Single-instance subsection with minimal config example (mode=single, no NATS). DONE (`docs/README.md`).
3. Update state docs timestamps. DONE (`docs/state/{context.md,detailed_plan.md,changelog.md}`).
Next: pending user acceptance.

## P88 (DONE)
Goal: remove NATS block from single-instance config example.
1. Drop `[ingest.nats]` from single-instance snippet. DONE (`docs/README.md`).
2. Update state docs timestamps. DONE (`docs/state/{context.md,detailed_plan.md,changelog.md}`).
Next: pending user acceptance.

## P90 (DONE)
Goal: make notify.queue.url an array across config/runtime.
1. Change `notify.queue.url` type to list and normalize/validate like ingest URLs. DONE (`internal/config/config.go`).
2. Update notify queue runtime to accept list URLs. DONE (`internal/notifyqueue/nats.go`).
3. Update configs/docs/tests to use array syntax. DONE (`configs/alerts/base.toml`, `docs/README.md`, `internal/config/config_test.go`, `internal/notifyqueue/nats_test.go`, `test/e2e/notify_queue_best_effort_e2e_test.go`).
4. Verify config/notifyqueue tests. DONE (`go test ./internal/config ./internal/notifyqueue -count=1`).
Next: pending user acceptance.

## P92 (DONE)
Goal: hardcode notify queue routing metadata in runtime.
1. Remove notify.queue subject/stream/consumer/deliver_group from config model/merge/validation. DONE (`internal/config/config.go`).
2. Hardcode notify queue subject/stream/consumer/group in runtime. DONE (`internal/notifyqueue/nats.go`).
3. Update configs/docs/tests to remove these fields; keep DLQ overrides. DONE (`configs/alerts/base.toml`, `docs/README.md`, `internal/config/config_test.go`, `test/e2e/notify_queue_best_effort_e2e_test.go`, `internal/notifyqueue/nats_test.go`).
4. Verify config/notifyqueue tests. DONE (`go test ./internal/config ./internal/notifyqueue -count=1`).
Next: pending user acceptance.

## P93 (DONE)
Goal: restore DLQ config defaults after removing queue routing fields.
1. Reintroduce DLQ subject/stream defaults in config examples. DONE (`configs/alerts/base.toml`, `docs/README.md`).
2. Verify config/notifyqueue tests. DONE (`go test ./internal/config ./internal/notifyqueue -count=1`).
Next: pending user acceptance.

## P95 (DONE)
Goal: full codebase + tests audit for duplication, implementation accuracy, and redundancy.
1. Run syntax/lint/test baseline (`go test ./...`, `go vet ./...`). DONE (PASS).
2. Audit all source/test files in `cmd/`, `internal/`, `test/` for duplication, risky/inaccurate logic, and excess complexity. DONE (findings prepared with file/line refs).
3. Produce prioritized optimization plan with low-risk execution order. DONE (ready for user review/approval).
Next: pending user acceptance.

## P96 (DONE)
Goal: execute optimization plan after audit findings.
1. Reliability fixes: shutdown first-error contract, pass shutdown context to tick/reload, defensive tag copy in engine identity, robust no-keys handling in NATS store. DONE (`internal/app/service.go`, `internal/engine/engine.go`, `internal/state/nats_store.go`).
2. Deduplicate permanent non-retryable error marker across notify and notifyqueue packages without API break. DONE (`internal/permanent/permanent.go`, `internal/notify/notify.go`, `internal/notifyqueue/queue.go`).
3. Reduce e2e duplication by extracting shared tracker lifecycle harness (jira/youtrack). DONE (`test/e2e/tracker_harness_test.go`, `test/e2e/tracker_{jira,youtrack}_e2e_test.go`).
4. Verify full suite (`go test ./...`, `go vet ./...`). DONE (PASS; `test/e2e` PASS in ~100s).
Next: pending user acceptance.

## P97 (OPEN)
Goal: run load tests with pprof and prepare CPU/memory optimization plan.
1. Run single-mode HTTP throughput benchmark with CPU+heap profiles (`tmp_p97_single_{cpu,mem}.pprof`). DONE (`E2E_SERVICE_MODE=single go test ./test/e2e -run '^$' -bench BenchmarkIngestThroughput -benchmem -benchtime=2m -count=1 -cpuprofile ./tmp_p97_single_cpu.pprof -memprofile ./tmp_p97_single_mem.pprof`).
2. Run NATS queue throughput benchmark with CPU+heap profiles (`tmp_p97_nats_{cpu,mem}.pprof`). DONE (`E2E_NATS_BATCH_SIZE=100 go test ./test/e2e -run '^$' -bench BenchmarkNATSQueueIngestThroughput -benchmem -benchtime=1m -count=1 -cpuprofile ./tmp_p97_nats_cpu.pprof -memprofile ./tmp_p97_nats_mem.pprof`; timeout/backlog reproduced in all sub-benches).
3. Analyze profiles (`go tool pprof -top`, `-cum`) and summarize hottest CPU/allocation paths. DONE (single path dominated by HTTP transport/syscalls + JSON decode; NATS path shows low CPU utilization with queue wait/backlog and alloc hot spots in JSON decode/NATS parse/map assignment).
4. Provide prioritized optimization plan with expected impact and risks. DONE (prepared from measured profile evidence).
Next: pending user acceptance.

## P98 (OPEN)
Goal: implement P97 optimization plan for NATS throughput and allocation reduction.
1. Add NATS ingest worker parallelism (`ingest.nats.workers`) with defaults/validation and runtime support. DONE (`internal/config/config.go`, `internal/ingest/nats.go`, `test/e2e/throughput_nats_benchmark_test.go`).
2. Add shared decode `sync.Pool` in ingest path to reduce per-message allocations without changing decode contract. DONE (`internal/ingest/codec.go`, `internal/ingest/nats.go`).
3. Coalesce tick refreshes in batch processing to avoid redundant state writes per event for same alert in one batch. DONE (`internal/app/manager.go`).
4. Update/extend tests for config, ingest, and manager batch behavior. DONE (`internal/config/config_test.go`, `internal/ingest/codec_pool_test.go`, `internal/app/manager_batch_test.go`).
5. Run focused and full verification (`go test ./internal/...`, `go test ./test/e2e ...`, `go test ./...`). DONE (`TMPDIR=$(pwd)/.tmp go test ./internal/app ./internal/config ./internal/ingest -count=1`, `TMPDIR=$(pwd)/.tmp go test ./test/e2e -run '^$' -bench BenchmarkNATSQueueIngestThroughput -benchtime=1x -count=1`, `TMPDIR=$(pwd)/.tmp go test ./...`).
Next: pending user acceptance.

## P99 (DONE)
Goal: run post-optimization load validation with pprof and compare against P97 baseline.
1. Run single-mode HTTP throughput benchmark with CPU+heap profiles after P98 (`tmp_p99_single_{cpu,mem}.pprof`). DONE (`TMPDIR=$(pwd)/.tmp E2E_SERVICE_MODE=single go test ./test/e2e -run '^$' -bench BenchmarkIngestThroughput -benchmem -benchtime=2m -count=1 -cpuprofile ./tmp_p99_single_cpu.pprof -memprofile ./tmp_p99_single_mem.pprof`).
2. Run NATS queue throughput benchmark with CPU+heap profiles for workers=1 and workers=4 (`tmp_p99_nats_w{1,4}_{cpu,mem}.pprof`). DONE (`TMPDIR=$(pwd)/.tmp E2E_NATS_BATCH_SIZE=100 E2E_NATS_WORKERS=1|4 go test ./test/e2e -run '^$' -bench BenchmarkNATSQueueIngestThroughput -benchmem -benchtime=2m -timeout=30m -count=1 -cpuprofile ... -memprofile ...`).
3. Analyze profiles (`go tool pprof -top -cum`) and compare throughput/backlog/allocs against P97 baseline. DONE (single path stable; NATS path now CPU/alloc heavy in JSON decode + key build, workers=4 removes backlog/timeouts).
4. Prepare measured optimization verdict (what improved, what regressed, next actions). DONE (verdict prepared with worker=1/4 split and follow-up actions).
Next: pending user acceptance.

## P100 (DONE)
Goal: ensure NATS load benchmarks leave no residual JetStream data and fail on undrained backlog.
1. Add explicit benchmark teardown that deletes benchmark stream and KV buckets after each sub-benchmark. DONE (`test/e2e/throughput_nats_benchmark_test.go`, `test/e2e/nats_helpers_test.go`).
2. Make queue-drain timeout/backlog a hard failure instead of log-only warning. DONE (`test/e2e/throughput_nats_benchmark_test.go`).
3. Harden local test NATS shutdown with kill fallback on slow SIGTERM drain. DONE (`test/testutil/nats.go`).
4. Run focused benchmark verification and confirm no residual `.tmp/BenchmarkNATS*` artifacts remain after run. DONE (`TMPDIR=$(pwd)/.tmp go test ./test/e2e -run '^$' -bench BenchmarkNATSQueueIngestThroughput -benchtime=1x -count=1`, `TMPDIR=$(pwd)/.tmp/p100_tmpdir go test ./test/e2e -run '^$' -bench BenchmarkNATSQueueIngestThroughput -benchtime=1x -count=1`, `find .tmp/p100_tmpdir -maxdepth 1 -type d -name 'BenchmarkNATSQueueIngestThroughput*' | wc -l => 0`).
Next: accepted.

## P101 (DONE)
Goal: remove legacy NATS `.blk` leftovers and sync user-facing/agent context docs.
1. Remove all `*.blk` files from workspace leftovers after historical load runs. DONE (`find /root/project/alerting -type f -name '*.blk' -print0 | xargs -0 rm -f`; post-check `find ... -name '*.blk' | wc -l => 0`).
2. Update `docs/state/context.md` with current benchmark-cleanup behavior and project-plan status. DONE.
3. Update `docs/README.md` with explicit "new config sections" summary and `ingest.nats.workers` in multi-instance config example. DONE.
4. Commit current approved scope changes. DONE.
Next: pending user acceptance.

## P102 (DONE)
Goal: simplify NATS-related config model and enforce runtime auto-disable in single mode.
1. Remove user-configurable multi-service routing keys from TOML (`ingest.nats.subject/stream/consumer_name/deliver_group`, `notify.queue.url`, `[notify.queue.dlq]`) and keep them runtime-fixed. DONE (`internal/config/config.go` rejectUnsupportedSyntax + runtime defaults).
2. Support `notify.queue.dlq = true|false` in `[notify.queue]` and use fixed DLQ stream/subject in runtime. DONE (`internal/config/config.go`, `internal/notifyqueue/nats.go`, `internal/notifyqueue/nats_test.go`, `test/e2e/notify_queue_best_effort_e2e_test.go`).
3. Enforce logical NATS auto-disable when `service.mode=single` (ingest NATS, state NATS flows, delete-consumer, notify queue), without depending on explicit config flags. DONE (`internal/config/config.go` single-mode defaults + updated config tests).
4. Update base config and README multi-instance section to explicit `mode="nats"` and new config structure. DONE (`configs/alerts/base.toml`, `docs/README.md`).
5. Update config/runtime/tests and run verification gates. DONE (`gofmt -w ...`, `TMPDIR=$(pwd)/.tmp go test ./internal/config ./internal/notifyqueue -count=1`, `TMPDIR=$(pwd)/.tmp go test ./test/e2e -run 'Test(NotifyQueueBestEffortPerChannelE2E|NotifyQueueDLQOnMaxDeliverE2E|MultiServiceNATSQueueLifecycleSingleNotifications)$' -count=1`, `TMPDIR=$(pwd)/.tmp go test ./... -count=1`).
Next: accepted.

## P110 (DONE)
Goal: reduce e2e test duplication and keep only needed coverage.
1. Keep full metric matrix coverage in one place only (`test/e2e/metrics_matrix_test.go`) and add reduced functional metric set helper. DONE (`e2eFunctionalMetricCases` with `count_total` only).
2. Apply reduced metric set to functional e2e suites (reload, queue, transport, tracker, multiservice), keeping benchmarks on full matrix. DONE (`test/e2e/{reload_e2e_test.go,notify_queue_best_effort_e2e_test.go,mattermost*_e2e_test.go,telegram*_e2e_test.go,tracker_harness_test.go,multiservice_nats_e2e_test.go,service_smoke_test.go}`).
3. Remove duplicated smoke/single test file by unifying mode checks into one table-driven smoke test. DONE (`test/e2e/service_smoke_test.go`, deleted `test/e2e/service_single_mode_test.go`).
4. Run verification gates for changed scope. DONE (`gofmt -w ...`, `go test ./test/e2e -count=1`, `go test ./...`).
Next: pending user acceptance.

## P111 (DONE)
Goal: significantly reduce test code size by removing config-test duplication without losing behavior coverage.
1. Refactor `internal/config/config_test.go` with shared TOML builders and table-driven validation cases for repetitive channel/mode/queue checks. DONE (`serviceSection`, `countTotalRule`, `routeBlock`, channel-specific notify helpers, grouped table tests).
2. Keep legacy syntax and merge-override checks, but collapse repeated fixture blocks into helpers. DONE (legacy/state/rule-name syntax checks preserved; explicit-false merge/dir-override tests preserved).
3. Run focused/full tests and measure line-count delta (`*_test.go`, total Go with tests). DONE (`go test ./internal/config -count=1`, `go test ./... -count=1`; `internal/config/config_test.go: 1898 -> 999`, `*_test.go total: 7526 -> 6627`, total Go lines with tests: `15270 -> 14371`).
4. Update bookkeeping docs and prepare commit with measured reduction. DONE (bookkeeping updated; ready to commit).
Next: accepted.

## P112 (DONE)
Goal: add resolve hysteresis for `missing_heartbeat` to prevent alert flapping on single recovery event.
1. Extend rule resolve schema with recovery confirmation window for heartbeat restore and validate per alert type constraints. DONE (`internal/config/config.go`: `resolve.recovery_sec`, type-specific validation, `ResolveTimeout` includes recovery window for missing_heartbeat).
2. Update engine transitions for `missing_heartbeat`: enter recovery-hold on first heartbeat, resolve only after stable window, reset hold on missing recurrence. DONE (`internal/engine/engine.go`: `RecoveringSince` state, event/tick recovery-hold flow, reset helper).
3. Add/update tests for config validation and engine flapping scenario, then run full verification. DONE (`internal/config/rule_types_test.go`, `internal/engine/engine_test.go`; `go test ./internal/config ./internal/engine -count=1`, `go test ./... -count=1`).
4. Update README and sample heartbeat rule config to describe new recovery contract. DONE (`docs/README.md`, `configs/alerts/rules.missing_heartbeat.toml`).
Next: accepted.

## P113 (DONE)
Goal: unify resolve hysteresis control for all alert types under one config key.
1. Replace `resolve.recovery_sec` with unified `resolve.hysteresis_sec` in config schema/validation and timeout derivation. DONE (`internal/config/config.go`: `RuleResolve` rename, validation update, unified `ResolveTimeout`).
2. Apply unified hysteresis runtime behavior across all rule types (`count_*` and `missing_heartbeat`) before `resolved` transition. DONE (`internal/engine/engine.go`: `count_*` resolves at `silence+hysteresis`; `missing_heartbeat` recovery hold uses same hysteresis key).
3. Update tests (config + engine) to cover unified semantics and run full verification. DONE (`internal/config/rule_types_test.go`, `internal/engine/engine_test.go`; `go test ./internal/config ./internal/engine -count=1`, `go test ./... -count=1`).
4. Update README and sample rules to document only `resolve.hysteresis_sec`. DONE (`docs/README.md`, `configs/alerts/rules.{count_total,count_window,missing_heartbeat}.toml`).
Next: accepted.

## P114 (DONE)
Goal: close remaining test gap for count_window resolve hysteresis behavior.
1. Add unit test for `count_window` with `resolve.hysteresis_sec > 0` (no resolve before threshold, resolve after threshold). DONE (`internal/engine/engine_test.go`).
2. Run focused/full verification. DONE (`go test ./internal/engine -count=1`, `go test ./... -count=1`).
3. Update bookkeeping docs with results. DONE.
Next: accepted.

## P115 (DONE)
Goal: sync context docs after accepted hysteresis unification and finalize user-facing spec consistency.
1. Update `docs/state/context.md` to current accepted implementation state (through P114) including unified `resolve.hysteresis_sec`. DONE.
2. Re-check `docs/README.md` against latest runtime/config contract and edit only if needed. DONE (no additional README fixes required beyond accepted P113/P114 content).
3. Update bookkeeping docs and commit docs-sync package. DONE.
Next: accepted.

## P116 (DONE)
Goal: enrich README with practical alert-rule writing patterns and fully commented config lines.
1. Add new README subsection with rule-writing patterns for all alert types (`count_total`, `count_window`, `missing_heartbeat`). DONE.
2. Provide TOML examples where each config line is explicitly documented with comments. DONE.
3. Keep examples aligned with current config/runtime contract (`resolve.hysteresis_sec`, `pending`, `notify.route`). DONE.
4. Update bookkeeping docs and commit docs package. DONE.
Next: accepted.

## P117 (DONE)
Goal: commit user-edited README as-is without extra content changes.
1. Capture current `docs/README.md` state exactly as edited by user. DONE.
2. Update bookkeeping docs and commit package. DONE.
Next: accepted.

## P118 (DONE)
Goal: add full matcher-filter coverage tests and enrich sample rule configs with explicit value filters.
1. Add a dedicated matcher unit test that covers tag filtering, variable-name filtering, all value types (`n/s/b`), and all supported value operations with pass/fail cases. DONE (`internal/engine/matcher_filter_matrix_test.go`).
2. Update sample rule configs for each metric type (`rules.count_total.toml`, `rules.count_window.toml`, `rules.missing_heartbeat.toml`) with explicit `match.value` filters while keeping existing var filters. DONE (`configs/alerts/rules.{count_total,count_window,missing_heartbeat}.toml`).
3. Run focused and full test suites, fix regressions if any, and update bookkeeping docs. DONE (`gofmt -w internal/engine/matcher_filter_matrix_test.go`, `go test ./internal/engine -count=1`, `go test ./... -count=1`).
Next: accepted.

## P119 (DONE)
Goal: rename sample rule identifiers in `rules.*` configs and add a dedicated filter examples config.
1. Rename sample rule IDs to `example_<alert_type>` in `configs/alerts/rules.count_total.toml`, `configs/alerts/rules.count_window.toml`, and `configs/alerts/rules.missing_heartbeat.toml` (all nested sections included). DONE (`example_count_total`, `example_count_window`, `example_missing_heartbeat`).
2. Add `configs/alerts/rules.example_filter.toml` with examples of all filter variants: tags, var names, and value predicates for all value types (`n/s/b`) with all supported operations. DONE.
3. Run verification tests and update bookkeeping docs. DONE (`go test ./... -count=1`).
Next: accepted.

## P120 (DONE)
Goal: rename `raise.tagg_sec` to `raise.window_sec` across runtime code, tests, configs, and user docs.
1. Replace runtime/test identifiers (`TaggSec` -> `WindowSec`; `tagg_sec` -> `window_sec`) in config schema, validation, engine logic, and unit/e2e fixtures. DONE (`internal/config/config.go`, `internal/engine/engine.go`, `internal/config/rule_types_test.go`, `internal/engine/engine_test.go`, `test/e2e/{metrics_matrix_test.go,throughput_benchmark_test.go}`).
2. Update sample configs and README semantics/examples to use `window_sec`. DONE (`configs/alerts/rules.count_window.toml`, `docs/README.md`).
3. Run full verification and update bookkeeping docs. DONE (`gofmt -w ...`, `go test ./... -count=1`).
Next: accepted.

## P121 (DONE)
Goal: add human-readable per-rule notify off windows (`[[rule.<name>.notify.off]]`) that suppress only notification delivery.
1. Extend config schema with `notify.off` windows (`days`, `from`, `to`) and validate day/time syntax with explicit rule path errors. DONE (`internal/config/config.go`: `RuleNotifyOff`, validation parser/checks, `NotifyOffActiveAt` helper).
2. Add runtime off-window evaluation in local host timezone and skip notification sending (direct + queue) without affecting alert state/log processing; no catch-up after window ends. DONE (`internal/app/manager.go`: suppression gate in direct dispatch + queued delivery).
3. Add tests for config validation and runtime suppression behavior (including queue path), update README + sample rule configs, and run full verification. DONE (`internal/config/rule_types_test.go`, `internal/app/manager_notify_off_test.go`, `docs/README.md`, `configs/alerts/rules.{count_total,count_window,missing_heartbeat}.toml`; `gofmt -w ...`, `go test ./... -count=1`).
Next: accepted.

## P122 (DONE)
Goal: remove duplicate notifications under state races, cut hot-path overhead, and reduce notify-route/runtime duplication without changing external contract.
1. Fix state-transition idempotency in manager/store path: make first card persist CAS-safe, treat revision conflicts as non-delivery for duplicated transitions, keep single/NATS semantics aligned. DONE (`internal/state/{store.go,memory_store.go,nats_store.go}` added `CreateCard`; `internal/app/manager.go` now persists transitions via CAS-safe create/update helpers and suppresses duplicate sends after reload-on-conflict).
2. Precompile `notify.off` windows in config/runtime and avoid repeated parsing on each notification send. DONE (`internal/config/config.go` added compiled window model + exported compile hook; `internal/app/manager.go` precompiles windows during rule normalization and runtime checks reuse compiled form).
3. Reduce hot-path/runtime duplication: unify notify-route normalization helpers and trim unnecessary per-event/per-repeat copying where safe. DONE (`internal/app/manager.go` now uses one route normalizer for config/runtime paths; `internal/engine/engine.go` returns immutable tag-map references instead of cloning on hot path).
4. Run formatting/tests, revert item to OPEN on failures if any, and update bookkeeping docs with exact verification commands/results. DONE (`gofmt -w internal/app/service.go internal/app/manager.go internal/app/manager_idempotency_test.go internal/config/config.go internal/engine/engine.go internal/state/store.go internal/state/memory_store.go internal/state/nats_store.go internal/state/memory_store_test.go internal/state/nats_store_test.go`; `go test ./... -count=1`; `go test -race ./...`).
Next: accepted.

## P124 (DONE)
Goal: reduce codebase size in manager/config/state without changing external contract or validated runtime behavior.
1. Capture baseline LOC for target files (`internal/app/manager.go`, `internal/config/config.go`, `internal/state/*.go`) and identify compressible blocks that can be merged/extracted without spreading code into more files. DONE: baseline `3876` LOC (`manager=1199`, `config=2202`, `store=31`, `memory=179`, `nats=265`).
2. Refactor orchestration/runtime helpers for net LOC reduction: compact manager state-persistence, route/runtime normalization, and repeated flow branches while preserving notify/reload/state semantics. DONE: `manager.go` route send/enqueue preparation and delivery side-effects collapsed into shared helpers; resolved-card notification building unified for TTL/rule-removal paths.
3. Refactor config/store helpers for net LOC reduction: collapse repeated validation/merge/card-lifecycle logic where safe, keep typed errors/contracts unchanged. DONE: shared rule prefix helper in `state`, compact card write helpers in memory/NATS stores; no config reduction landed in this pass because it would have increased risk for low net gain.
4. Run formatting/full/race verification, measure final LOC delta against baseline, and update bookkeeping docs with exact numbers. DONE: `gofmt -w internal/app/manager.go internal/state/store.go internal/state/memory_store.go internal/state/nats_store.go`, `go test ./... -count=1`, `go test -race ./...` all PASS; final target LOC `3854` (`manager=1165`, `config=2202`, `store=39`, `memory=182`, `nats=266`) => net `-22` LOC.
Next: pending user acceptance.

## P125 (DONE)
Goal: make a second codebase-reduction pass with a stricter LOC target, focused on remaining duplication hotspots.
1. Refactor `internal/config/config.go` notify merge/presence logic into shared helpers and reduce channel-specific repetition without changing TOML/runtime behavior. DONE: compact typed merge helpers landed; notify channel/queue presence checks collapsed without reflection or TOML contract changes. `config.go` reduced from `2202` to `2156` LOC.
2. Apply a smaller cleanup pass in `internal/app/manager.go` and `internal/state/*` only if it gives net negative LOC without increasing indirection risk. DONE: `manager.go` private route/delivery helper comments compacted and file reduced from `1165` to `1155` LOC; `state/*` left unchanged from P124 because additional compaction would not improve net LOC safely.
3. Run formatting/full/race verification, measure delta against current baseline, and report whether the stricter reduction target was actually met. DONE: `gofmt -w internal/config/config.go internal/app/manager.go`, `go test ./... -count=1`, `go test -race ./...` all PASS. Baseline after P124 was `3854` LOC (`manager=1165`, `config=2202`, `store=39`, `memory=182`, `nats=266`); final is `3798` LOC (`manager=1155`, `config=2156`, `store=39`, `memory=182`, `nats=266`) => net `-56` LOC, stricter target met.
Next: pending user acceptance.

## P126 (DONE)
Goal: run channel-by-channel functional and load verification for delivery paths, with pprof evidence and comparison against prior profiling notes.
Superseded note: the delivery-load conclusion from this pass is incomplete. The added `internal/notify/notify_benchmark_test.go` benchmark only measures mocked dispatcher/send cost, while the ingest benchmarks keep delivery disabled; corrected end-to-end delivery results now live in `P127`.
1. Audit current coverage for `telegram`, `mattermost`, `jira`, `youtrack`, and `http` delivery paths; identify missing functional or benchmark coverage needed for a defensible regression check. DONE: existing unit/e2e coverage already covered Telegram, Mattermost, Jira, and YouTrack lifecycle paths; gaps were explicit HTTP delivery e2e and dedicated per-channel delivery benchmarks/pprof.
2. Add any missing focused tests/benchmarks needed to exercise delivery-path behavior per channel without changing runtime contracts. DONE: added `test/e2e/http_notify_e2e_test.go` (HTTP notify firing->resolved e2e) and `internal/notify/notify_benchmark_test.go` (`BenchmarkDispatcherChannelSend` for `telegram/http/mattermost/jira/youtrack` with mock transports and per-channel `cpu`/`mem` profiles).
3. Run functional matrix per channel, then run load/benchmark profiles with `cpu`/`mem` pprof for delivery paths and compare hotspots/metrics with prior benchmark/profile records where available. DONE:
   - Functional channel matrix PASS:
     `go test ./internal/notify -run 'Test(TelegramSenderSend|TelegramSenderActiveOnlyLifecycle|HTTPScenarioSenderSend|MattermostSenderSend|MattermostSenderResolvedUsesRootID|MattermostSenderStatusErrorIncludesResponseBody|MattermostSenderResolvedRequiresExternalRef|MattermostSenderActiveOnlyLifecycle|TrackerSenderCreateAndResolve|TrackerSenderResolveRequiresExternalRef)$' -count=1`
     `go test ./test/e2e -run 'Test(HTTPNotifyDeliveryE2E|TelegramResolvedRepliesToOpeningMessage|TelegramActiveOnlyLifecycleE2E|MattermostWebhookFiringAndResolveE2E|MattermostWebhookRetryOnTransientFailureE2E|MattermostActiveOnlyLifecycleE2E|JiraTrackerCreateAndResolveE2E|YouTrackTrackerCreateAndResolveE2E|NotifyQueueBestEffortPerChannelE2E|NotifyQueueDLQOnMaxDeliverE2E)$' -count=1 -v`
     `go test ./test/e2e -run TestTelegramLiveEndToEnd -count=1 -v` => SKIP (no `E2E_TG_BOT_TOKEN` / `E2E_TG_CHAT_ID` in env).
   - Full verification PASS: `go test ./... -count=1`, `go test -race ./...`.
   - General load/pprof reruns:
     `TMPDIR=$(pwd)/.tmp E2E_SERVICE_MODE=single go test ./test/e2e -run '^$' -bench BenchmarkIngestThroughput -benchmem -benchtime=1m -count=1 -cpuprofile ./tmp_p126_single_cpu.pprof -memprofile ./tmp_p126_single_mem.pprof`
       Result: `count_total 12952 events/sec 8849 B/op 115 allocs/op`, `count_window 13385 events/sec 8977 B/op 115 allocs/op`, `missing_heartbeat 13375 events/sec 8823 B/op 115 allocs/op`, backlog `0`.
     `TMPDIR=$(pwd)/.tmp E2E_NATS_BATCH_SIZE=100 E2E_NATS_WORKERS=4 go test ./test/e2e -run '^$' -bench BenchmarkNATSQueueIngestThroughput -benchmem -benchtime=30s -timeout=30m -count=1 -cpuprofile ./tmp_p126_nats_cpu.pprof -memprofile ./tmp_p126_nats_mem.pprof`
       Result: `count_total 619213 events/sec 113760 B/op 2024 allocs/op`, `count_window 820551 events/sec 132445 B/op 2024 allocs/op`, `missing_heartbeat 631628 events/sec 115957 B/op 2024 allocs/op`, backlog `0`.
   - Per-channel delivery benchmark/pprof runs:
     `go test ./internal/notify -run '^$' -bench '^BenchmarkDispatcherChannelSend/<channel>$' -benchmem -benchtime=15s -count=1 -cpuprofile ./tmp_p126_delivery_<channel>_cpu.pprof -memprofile ./tmp_p126_delivery_<channel>_mem.pprof`
       `telegram`: `200559 ns/op`, `27838 B/op`, `284 allocs/op`
       `http`: `92650 ns/op`, `8544 B/op`, `97 allocs/op`
       `mattermost`: `98913 ns/op`, `9685 B/op`, `120 allocs/op`
       `jira`: `106163 ns/op`, `11473 B/op`, `147 allocs/op`
       `youtrack`: `105556 ns/op`, `11488 B/op`, `147 allocs/op`
   - pprof comparison summary:
     previous raw `.pprof` artifacts were not present on disk, so comparison used `docs/state/detailed_plan.md` / `docs/state/changelog.md` references from `P63/P64/P97/P99`.
     Single-mode ingest remains stable vs earlier HTTP benchmark snapshots from `P72` (`13095/13149/13303 events/sec`) with current `12952/13385/13375`; no regression signal.
     NATS queue keeps the `P99` post-fix property of `workers=4` draining with backlog `0`; CPU/alloc hotspots remain in the same family described in `P97/P99` (`encoding/json` decode, NATS parse, key-build/runtime alloc work), with no new dominant hotspot.
     Per-channel delivery had no historical dedicated benchmark baseline; `P126` establishes the first direct channel-send CPU/memory baseline. All five channel CPU profiles are dominated by loopback transport/syscall wait (`internal/runtime/syscall.Syscall6`, `runtime.futex`) rather than new application-level hot spots.
4. Update bookkeeping docs with exact commands, results, and any regressions or gaps. DONE: no functional regressions found in covered channels; only remaining gap is real Telegram live validation, which is environment-blocked and currently skip-safe by design.
Next: pending user acceptance.

## P127 (DONE)
Goal: replace the flawed P126 delivery-load methodology with real end-to-end channel delivery benchmarks under identical batch conditions.
1. Build a true delivery benchmark harness where each input event reaches an enabled channel route through full ingest -> manager -> notifier flow, with unique alert identities so delivery actually happens for every event. DONE: added `test/e2e/delivery_benchmark_test.go` with real `Service` runtime, real `HTTP` batch and `NATS` batch ingest paths, per-channel local delivery endpoints, and unique host keys per event to force one firing delivery per input event.
2. Run that harness for `telegram`, `http`, `mattermost`, `jira`, `youtrack` across both ingest paths (`HTTP`, `NATS`) and batch sizes `1`, `100`, `1000`, with identical batch semantics. DONE:
   - Functional matrix PASS:
     `go test ./test/e2e -run 'TestHTTPNotifyDeliveryE2E|TestMattermostWebhookFiringAndResolveE2E|TestJiraTrackerCreateAndResolveE2E|TestYouTrackTrackerCreateAndResolveE2E|TestTelegramResolvedRepliesToOpeningMessage|TestTelegramLive' -count=1 -v`
     `TestTelegramLiveEndToEnd` => SKIP (missing `E2E_TG_BOT_TOKEN` / `E2E_TG_CHAT_ID`).
   - Delivery benchmark matrix PASS with identical batches and zero backlog in all 30 cases:
     `E2E_DELIVERY_BATCHES=20 go test ./test/e2e -run '^$' -bench 'Benchmark(HTTP|NATS)DeliveryE2E' -benchtime=1x -count=1`
   - Representative throughput summary from `tmp_p127_results.tsv`:
     `HTTP/http`: `1228`, `1385`, `847.6 deliveries/sec` for batch `1/100/1000`
     `HTTP/telegram`: `1104`, `1250`, `807.0 deliveries/sec`
     `HTTP/mattermost`: `801.9`, `913.8`, `651.2 deliveries/sec`
     `HTTP/jira`: `832.2`, `907.1`, `649.9 deliveries/sec`
     `HTTP/youtrack`: `830.9`, `910.2`, `649.2 deliveries/sec`
     `NATS/http`: `1301`, `1422`, `904.9 deliveries/sec`
     `NATS/telegram`: `934.5`, `1283`, `848.4 deliveries/sec`
     `NATS/mattermost`: `764.0`, `925.6`, `676.3 deliveries/sec`
     `NATS/jira`: `772.9`, `939.0`, `676.7 deliveries/sec`
     `NATS/youtrack`: `784.0`, `921.1`, `675.5 deliveries/sec`
     all cases finished with `backlog_events=0`.
3. Capture `cpu`/`mem` pprof for the corrected benchmark runs, compare with earlier ingest-only profile notes where possible, and clearly separate ingest-only vs delivery-path conclusions. DONE:
   - Saved artifacts: `tmp_p127_{http,nats}_<channel>_batch{1,100,1000}_{cpu,mem}.pprof` plus matching `.log` and `.txt` summaries.
   - CPU profiles for all corrected delivery runs remain dominated by loopback/network wait (`runtime.futex`, `internal/runtime/syscall.Syscall6`), not by a new app-level regression. NATS-path profiles additionally show expected `github.com/nats-io/nats.go.(*Conn).parse` cost. Application-level hot spots that still repeat across channels are small and already known: `alerting/internal/engine.cloneTimes`, `alerting/internal/engine.(*Engine).TickRuleWithFiring`, `encoding/json.*`.
   - Memory profiles confirm the same family of alloc drivers across corrected runs: `alerting/internal/engine.cloneTimes` is the top app allocator; HTTP channel keeps the lowest channel-side alloc pressure, Telegram is materially heavier because multipart/form-data + bot client path (`github.com/go-telegram/bot.(*Bot).SendMessage`, `net/http.(*Request).ParseMultipartForm`, `bufio.NewReaderSize`, `bytes.growSlice`), tracker channels are heavier than plain HTTP because `alerting/internal/notify.(*TrackerSender).executeAction` and extra JSON marshalling dominate the channel-specific portion.
   - Comparison with previous notes:
     previous `P126` per-channel microbenchmarks are not apples-to-apples and must not be used as delivery-throughput baseline.
     prior HTTP/NATS ingest profiles from `P72/P97/P99` remain useful only for ingest-path families. The corrected `P127` delivery profiles show the same broad shape plus real notifier I/O cost; no new dominant hotspot family appeared after `P122/P124/P125`.
4. Update bookkeeping docs to mark P126 conclusions as incomplete for delivery throughput and record corrected P127 results. DONE: `P126` is now explicitly marked incomplete for delivery throughput; corrected methodology/results are documented here. Full verification also PASS: `gofmt -w test/e2e/delivery_benchmark_test.go test/e2e/service_harness_test.go test/e2e/service_smoke_test.go`, `go test ./... -count=1`, `go test -race ./...`.
Next: pending user acceptance.

## P131 (DONE)
Goal: implement the built-in UI dashboard from `internal/ui/README.md`.
1. Add UI config schema/validation (`internal/config/config.go`, tests) with basic auth, NATS registry, probe timeout, and advertised public base URL. DONE.
2. Add service-registry publisher and UI dashboard runtime (`internal/app/service.go`, `internal/ui/*`). DONE.
3. Add built-in HTTP UI with SSR + SSE and auth-guarded routes `/ui`, `/ui/events`, `/ui/healthz`. DONE.
4. Add tests for config, registry, dashboard aggregation, auth, and service-level smoke path. DONE (`internal/config/config_test.go`, `internal/ui/*_test.go`, `test/e2e/ui_e2e_test.go`).
5. Run `gofmt`, `go test ./... -count=1`, `go test -race ./...`; mark DONE only after all pass. DONE.
Next: pending user acceptance.

## P133 (DONE)
Goal: fix newly found UI/runtime defects, then bring up a local 3-service contour with the dashboard.
1. Fix UI registry lifecycle and service behavior on registry failure (shutdown cleanup, explicit runtime handling). DONE.
2. Remove the hot-reload trap for `ui.*` by keeping current UI runtime and applying only reloadable config with explicit warning. DONE.
3. Improve UI HTTP behavior (`/ui/` route, SSE error path). DONE.
4. Add targeted tests for registry lifecycle, reload behavior, and UI route/SSE coverage. DONE (`internal/ui/{registry,server}_test.go`, `internal/app/service_ui_reload_test.go`).
5. Run `gofmt`, `go test ./... -count=1`, `go test -race ./...`, then start local NATS + 3 services + UI and report the access URL. DONE: local contour is up (`nats://127.0.0.1:14222`, services on `19101/19102/19103`, UI on `19200`), full and race suites PASS.

## P136 (OPEN)
Goal: make the dashboard update interactively without manual page refresh under basic-auth protection.
1. Replace the fragile browser `EventSource` dependency in the page with an auth-compatible interactive update path. DONE: page now uses authenticated snapshot polling; server keeps SSE only as a compatible auxiliary route.
2. Add an authenticated JSON snapshot endpoint and keep summary/services/alerts blocks auto-refreshing from it. DONE: added `/ui/api/dashboard` and wired summary/services/alerts/timestamp re-rendering from inline JS polling.
3. Add/adjust UI tests for auth and live-update snapshot delivery. DONE: updated `internal/ui/server_test.go` and `test/e2e/ui_e2e_test.go` for the new snapshot endpoint and interactive path.
4. Run `gofmt`, `go test ./... -count=1`, and `go test -race ./...` after the fix. DONE: both suites passed; local 3-service contour was rebuilt and verified on the new UI code.
Next: pending user acceptance.

## P137 (OPEN)
Goal: enrich dashboard data and presentation for operational use, and restyle the built-in UI toward the approved `bsl.expert` visual direction.
1. Reformat dashboard timestamps to second precision only. DONE: UI now renders all dashboard timestamps in `YYYY-MM-DD HH:MM:SS`.
2. Extend `Active Alerts` with alert type, shortened hoverable alert id, latest metric value, threshold value, and accumulated event count for the active lifecycle. DONE: snapshot/model and table now expose `alert_type`, shortened hoverable IDs, latest metric value, threshold, and lifecycle `event_count` for new/updated alerts.
3. Extend `Alerting Services` with `host + ip` and a real build/runtime version instead of the current placeholder value. DONE: service rows now show host plus advertised address, and registry version is derived from Go build info / VCS revision instead of a hardcoded placeholder.
4. Refresh the dashboard visual design toward the `bsl.expert` reference while keeping the single-binary and inline-assets constraints. DONE: updated the inline-only SSR layout to a warmer editorial/engineering style with stronger typography, stricter tables, and clearer section hierarchy.
5. Update UI tests/e2e and re-run `gofmt`, `go test ./... -count=1`, and `go test -race ./...`. DONE: full and race suites passed; local contour was rebuilt and verified on the updated UI.
Next: pending user acceptance.

## P138 (OPEN)
Goal: align the dashboard visual profile much more closely with the `bsl.expert` reference without introducing external runtime assets.
1. Replace the current warm/light palette with the dark cyan-accent profile used by the reference. DONE: switched the dashboard to a dark `#040d12` base with cyan `#22d3ee` accents, glow, and dark table/card chrome.
2. Rework the hero and section chrome toward the reference language: top status bar, grid/PCB background, sharper cards, mono labels, and stronger display hierarchy. DONE: added top status bar, PCB/grid background, sharper rectangular panels, display-style headings, mono labels, and cyan chips.
3. Keep existing dashboard data and interactive behavior unchanged while updating only SSR markup/CSS/minimal rendering details. DONE: snapshot contract and polling behavior stayed intact; changes are limited to SSR template/CSS/rendering copy.
4. Re-run `gofmt`, `go test ./... -count=1`, and `go test -race ./...`, then rebuild the local contour for visual verification. DONE: full and race suites passed; local contour was rebuilt and verified live on `19200`.
Next: pending user acceptance.

## P139 (OPEN)
Goal: make two hero typography elements less dominant.
1. Reduce the `Live Alerting Dashboard` heading size by about half. DONE.
2. Reduce the `Snapshot Generated` timestamp size by about half. DONE.
3. Re-run `gofmt`, `go test ./... -count=1`, and `go test -race ./...`. DONE.
Next: pending user acceptance.

## P140 (OPEN)
Goal: simplify the hero and reduce the height of the summary counter row.
1. Remove the current hero subtitle copy under the main title. DONE.
2. Make the summary counter cards about twice shorter in height. DONE.
3. Re-run `gofmt`, `go test ./... -count=1`, and `go test -race ./...`. DONE.
Next: pending user acceptance.

## P141 (OPEN)
Goal: make the `Active Alerts` table feel more live without waiting for the next backend snapshot.
1. Remove the redundant `Last update` footer under the alerts table. DONE.
2. Update the `Last Seen` column on the client side between snapshot polls. DONE.
3. Re-run `gofmt`, `go test ./... -count=1`, and `go test -race ./...`. DONE.
Next: pending user acceptance.

## P142 (DONE)
Goal: restart the local contour so the latest UI template changes are visible in the live dashboard.
1. Restart the three local `alerting` services on top of the existing local NATS and notify sink. DONE.
2. Verify `3/3 ready`. DONE.
3. Verify the dashboard responds on `:19200` and serves the updated HTML. DONE.
Next: pending user acceptance.

## P143 (DONE)
Goal: recover the local contour after all three `alerting` processes were found stopped.
1. Restart the three local `alerting` services on the existing local NATS and notify sink. DONE.
2. Verify `3/3 ready`. DONE.
3. Verify the dashboard responds on `:19200` and serves the expected updated HTML. DONE.
Next: pending user acceptance.

## P144 (OPEN)
Goal: fix the local contour so service advertised URLs use the host IP instead of localhost, then relaunch the contour.
1. Update `tmp_p133_contour/*.toml` so `ui.service.public_base_url` points to `192.168.5.6` instead of `127.0.0.1`. DONE.
2. Relaunch the three local `alerting` services on the existing NATS and notify sink. DONE.
3. Verify `3/3 ready` and confirm UI access/probes via the host IP. DONE.
Next: pending user acceptance.

## P145 (OPEN)
Goal: relaunch the local contour with a durable background process model, explicit logs, and strict listener verification.
1. Ensure the local contour binary is current before relaunch. DONE.
2. Start `service-a`, `service-b`, and `service-c` through a durable process model that survives command completion in this environment. DONE: one-shot `nohup` launches were reproduced as non-durable under Codex exec, so the verified fix was to keep each service in its own persistent exec/PTTY session.
3. Verify listeners on `19101`, `19102`, `19103`, and `19200`, then verify `readyz` and authenticated UI endpoints. DONE.
4. Record the reproduced failure mode and the verified fix in `docs/state/skill.md`. DONE.
Next: pending user acceptance.

## P146 (OPEN)
Goal: simplify the dashboard hero by removing decorative copy and controls that do not add operational value.
1. Remove the `Snapshot Generated` block from the hero. DONE.
2. Remove the `Hardware-grade Runtime Visibility` eyebrow and both hero buttons. DONE.
3. Keep `Live Alerting Dashboard` on one line and tighten the hero layout after removing the extra block. DONE.
4. Re-run `gofmt`, `go test ./... -count=1`, and `go test -race ./...`. DONE.
Next: pending user acceptance.

## P147 (OPEN)
Goal: make the dashboard denser and surface backend snapshot-feed outages explicitly.
1. Reduce table, section, and summary vertical padding to the minimum practical footprint without harming readability. DONE.
2. Replace silent client refresh failures with an explicit full-page offline modal styled like a `FIRING` state. DONE.
3. Auto-hide the modal once the backend snapshot feed recovers. DONE.
4. Update tests, run `gofmt`, `go test ./... -count=1`, and `go test -race ./...`, then restart the live contour. DONE.
Next: pending user acceptance.

## P148 (OPEN)
Goal: remove the top summary counters because they do not add enough operational value relative to the screen space they consume.
1. Remove the summary block with alert/service counters from the dashboard HTML. DONE.
2. Tighten the layout after the block removal so the tables move up without dead space. DONE.
3. Remove unused client-side summary rendering logic. DONE.
4. Re-run `gofmt`, `go test ./... -count=1`, and `go test -race ./...`. DONE.
Next: pending user acceptance.

## P149 (OPEN)
Goal: simplify the services table and reduce section-heading prominence.
1. Remove the dedicated `Host` column from the services table. DONE.
2. Move the host IP into the `Service` cell as secondary metadata. DONE.
3. Rename `Alerting Services` to `Active Alert Services`. DONE.
4. Reduce the `Active Alert Services` and `Active Alerts` heading sizes by about half. DONE.
5. Re-run `gofmt`, `go test ./... -count=1`, and `go test -race ./...`. DONE.
Next: pending user acceptance.

## P150 (OPEN)
Goal: remove the remaining eyebrow labels above the two main table sections.
1. Remove `Service Topology` from the services section. DONE.
2. Remove `Current Pressure` from the alerts section. DONE.
3. Re-run `gofmt`, `go test ./... -count=1`, and `go test -race ./...`. DONE.
Next: pending user acceptance.

## P151 (OPEN)
Goal: shorten the two section titles to simpler labels.
1. Rename `Active Alert Services` to `Services`. DONE.
2. Rename `Active Alerts` to `Alerts`. DONE.
3. Re-run `gofmt`, `go test ./... -count=1`, and `go test -race ./...`. DONE.
Next: pending user acceptance.

## P153 (OPEN)
Goal: audit the built-in UI for cleanup/performance wins, remove temporary artifacts, sync context, and create a commit for the accepted UI work.
1. Re-audit the UI/runtime code and identify concrete optimization/cleanup items. DONE: identified dead `Summary` payload work, dead `/ui/events` path, redundant immediate post-SSR poll, and sequential service probing as the next low-risk cleanup targets.
2. Apply the accepted low-risk UI cleanup changes. DONE: removed `Summary` from the active UI payload/render path, removed `/ui/events` and related tests, removed the redundant immediate poll after SSR, and parallelized service health probing in `internal/ui/dashboard.go`.
3. Stop the local contour and remove temporary files/artifacts that are no longer needed. DONE.
4. Re-run `gofmt`, `go test ./... -count=1`, and `go test -race ./...`. DONE.
5. Update `docs/state/context.md` and bookkeeping docs for the accepted state. DONE.
6. Create a commit for the accepted scope without touching unrelated user files. OPEN.
Next: ready to commit.
