# Skill Log
Problem: `NotifyConfig` merge detection compared `HTTPNotifier` struct containing `map`, causing build failure.
Symptoms: `invalid operation: cfg.HTTP != (HTTPNotifier{}) (struct containing map[string]string cannot be compared)`.
Ready fix(exact commands/files): edit `internal/config/config.go` (`hasNotifyConfig`) to field-by-field checks with `len(cfg.HTTP.Headers) > 0`.
Verify(exact checks): `gofmt -w $(rg --files -g '*.go') && go test ./...`.
Status: resolved.
Last verified: 2026-02-18 22:00

Problem: hot reload applied rules but did not swap notifier dispatcher; updated `notify.*` config had no runtime effect.
Symptoms: after successful config reload, channel enable/disable changes were ignored.
Ready fix(exact commands/files): update `internal/app/service.go` (`reloadConfig`) to build/swap dispatcher via `manager.SetDispatcher`; add lock-safe dispatcher accessors in `internal/app/manager.go`; verify reload path with `test/e2e/reload_e2e_test.go`.
Verify(exact checks): `gofmt -w $(rg --files -g '*.go') && go test ./... && go vet ./... && go test -race ./...`.
Status: resolved.
Last verified: 2026-02-18 22:50

Problem: runtime engine state grows without bounds for high-cardinality keys that never reach firing/resolved.
Symptoms: peak-load probe with unique hosts shows retained heap growth after GC (`500000` unique keys => `~325MB` retained), while baseline is `~0.19MB`.
Ready fix(exact commands/files): add bounded runtime-state lifecycle in `internal/engine/engine.go` + manager tick integration in `internal/app/manager.go` (TTL/idle-eviction for inactive states, plus tests in `internal/engine/engine_test.go` and/or e2e cardinality scenario).
Verify(exact checks): `go run ./tmp_leak_probe.go` (KeepAlive version) should show bounded post-GC heap after unique-key phase; then `go test ./... && go test -race ./...`.
Status: resolved (requires non-zero `service.runtime_state_max` and/or `service.runtime_state_idle_sec`).
Last verified: 2026-02-18 23:37

Problem: JetStream ingest consumer failed to start on `WorkQueuePolicy` stream when configured with `deliver=new`.
Symptoms: service init error `consumer must be deliver all on workqueue stream` during multi-instance NATS e2e.
Ready fix(exact commands/files): set ingest subscription policy to `nats.DeliverAll()` in `internal/ingest/nats.go`; align deploy bootstrap consumer creation to `--deliver all` in `deploy/nats/bootstrap.sh`.
Verify(exact checks): `go test ./test/e2e -run MultiServiceNATSQueueLifecycleSingleNotifications -count=1 -v && go test ./...`.
Status: resolved.
Last verified: 2026-02-19 10:40

Problem: `go test -race ./...` reported a data race between `Service.reloadConfig()` and `Service.shutdown()` while swapping/closing notify queue runtime.
Symptoms: race detector flagged concurrent read/write of `notifyQ`/`notifyPub` in `internal/app/service.go` during reload e2e (`TestHotReloadApplyValidSnapshot`).
Ready fix(exact commands/files): add locked notify-runtime swap helper in `internal/app/service.go`; route reload/build/shutdown through `swapNotifyRuntime`; verify with `gofmt -w internal/app/service.go internal/app/manager.go internal/app/manager_idempotency_test.go internal/config/config.go internal/engine/engine.go internal/state/store.go internal/state/memory_store.go internal/state/nats_store.go internal/state/memory_store_test.go internal/state/nats_store_test.go && go test ./... -count=1 && go test -race ./...`.
Verify(exact checks): `go test ./... -count=1 && go test -race ./...`.
Status: resolved.
Last verified: 2026-03-06 09:55

Problem: built-in dashboard only reflected alert/service changes after a manual browser refresh.
Symptoms: page loaded, but active alerts and tables stayed stale until full reload while backend snapshots were already changing.
Ready fix(exact commands/files): update `internal/ui/server.go` to expose `/ui/api/dashboard`, embed `basePath` as a valid JS string, and poll authenticated snapshots from inline JS; cover in `internal/ui/server_test.go` and `test/e2e/ui_e2e_test.go`.
Verify(exact checks): `gofmt -w internal/ui/server.go internal/ui/server_test.go test/e2e/ui_e2e_test.go && go test ./... -count=1 && go test -race ./...`; then rebuild local contour binary, start services from `tmp_p133_contour/*.toml`, and confirm `curl -su ops:secret http://127.0.0.1:19200/ui/api/dashboard` changes after ingest.
Status: resolved.
Last verified: 2026-03-06 15:16

Problem: local contour services disappeared after "successful" relaunches, leaving `:19101-:19103` and `:19200` closed.
Symptoms: one-shot `nohup`/background launches from Codex exec returned PID files but `pgrep`/`ss` were empty immediately after command completion; `.tmp/p133_logs/service-*.log` stayed empty, while the same binary/config stayed healthy when run in a persistent foreground session.
Ready fix(exact commands/files): rebuild `./.tmp/p145_bin/alerting` with `go build -o ./.tmp/p145_bin/alerting ./cmd/alerting`; then run each service in its own persistent exec/PTTY session instead of a one-shot background command:
`./.tmp/p145_bin/alerting --config-file ./tmp_p133_contour/service-a.toml`
`./.tmp/p145_bin/alerting --config-file ./tmp_p133_contour/service-b.toml`
`./.tmp/p145_bin/alerting --config-file ./tmp_p133_contour/service-c.toml`
Keep the sessions alive and verify through listeners/HTTP checks rather than trusting startup return codes.
Verify(exact checks): `ss -ltnp | rg ':(19200|19101|19102|19103)\\b'`; `curl -sf http://127.0.0.1:19101/readyz`; `curl -sf http://127.0.0.1:19102/readyz`; `curl -sf http://127.0.0.1:19103/readyz`; `curl -sfu ops:secret http://192.168.5.6:19200/ui/api/dashboard`.
Status: resolved.
Last verified: 2026-03-06 16:52
