# Alerting MVP Runbook

## Prerequisites
- Go 1.23.1+
- NATS Server 2.12.4+ (JetStream enabled)
- Config file or config directory in TOML format

## Build
```bash
go build ./...
```

## Run (single file)
```bash
go run ./cmd/alerting --config-file ./config.toml
```

## Run (directory mode)
```bash
go run ./cmd/alerting --config-dir ./config.d
```

## Health checks
```bash
curl -i http://127.0.0.1:8080/healthz
curl -i http://127.0.0.1:8080/readyz
```

## Ingest test event
```bash
curl -i -X POST http://127.0.0.1:8080/ingest \
  -H 'content-type: application/json' \
  -d '{
    "dt": 1739876543210,
    "type": "event",
    "tags": {"dc": "dc1", "service": "api"},
    "var": "errors",
    "value": {"t": "n", "n": 1},
    "agg_cnt": 1,
    "win": 0
  }'
```

## Test suite
```bash
go test ./...
```

## Hot reload acceptance (success/fail)
```bash
go test ./test/e2e -run HotReload -v
```
Expected:
- valid config rewrite is applied (`rule_new` starts firing),
- invalid config rewrite is rejected (previous snapshot stays active).

## Throughput acceptance (10k events/sec reference)
```bash
go test ./test/e2e -run '^$' -bench BenchmarkIngestThroughput -benchtime=10s -count=1
```
Expected:
- reported `events/sec` should be >= `10000` on target MVP environment.

## Troubleshooting
- If startup fails, validate TOML syntax and duplicate `rule.name` values.
- If no alerts fire, verify `match` predicates and required `key.from_tags` in event tags.
- If notifications fail, check Telegram token/chat and retry logs.
