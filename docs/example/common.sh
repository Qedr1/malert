#!/usr/bin/env bash
set -euo pipefail

# Common helpers for manual alert scenario checks via HTTP/NATS ingest.

: "${BASE_URL:=http://127.0.0.1:8080}"
: "${INGEST_PATH:=/ingest}"
: "${NATS_URL:=nats://127.0.0.1:4222}"
: "${NATS_SUBJECT:=alerting.events}"

: "${TAG_DC:=dc1}"
: "${TAG_SERVICE:=api}"
: "${TAG_HOST:=demo-$(date +%s)}"

log() {
  printf '[example] %s\n' "$*"
}

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    printf 'required command is missing: %s\n' "$cmd" >&2
    exit 1
  fi
}

now_ms() {
  date +%s%3N
}

build_event_json() {
  local var="$1"
  local value_n="$2"
  local event_type="$3"
  local agg_cnt="$4"
  local win="$5"
  local dt
  dt="$(now_ms)"
  cat <<EOF
{"dt":$dt,"type":"$event_type","tags":{"dc":"$TAG_DC","service":"$TAG_SERVICE","host":"$TAG_HOST"},"var":"$var","value":{"t":"n","n":$value_n},"agg_cnt":$agg_cnt,"win":$win}
EOF
}

send_http_json() {
  require_cmd curl
  local payload="$1"
  local url="${BASE_URL}${INGEST_PATH}"
  local status
  status="$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$url" -H 'Content-Type: application/json' --data "$payload")"
  if [[ "$status" != "202" ]]; then
    printf 'http ingest failed: status=%s url=%s payload=%s\n' "$status" "$url" "$payload" >&2
    exit 1
  fi
}

send_nats_json() {
  require_cmd nats
  local payload="$1"
  nats --server "$NATS_URL" pub "$NATS_SUBJECT" "$payload" >/dev/null
}

send_event() {
  local transport="$1"
  local payload="$2"
  case "$transport" in
    http) send_http_json "$payload" ;;
    nats) send_nats_json "$payload" ;;
    *)
      printf 'unsupported transport: %s\n' "$transport" >&2
      exit 1
      ;;
  esac
}

scenario_count_total() {
  local transport="$1"
  local var="${VAR:-errors}"
  local value_n="${VALUE_N:-1}"
  local fire_events="${FIRE_EVENTS:-3}"
  local fire_interval_sec="${FIRE_INTERVAL_SEC:-1}"
  local silence_sec="${SILENCE_SEC:-65}"
  local i payload

  log "count_total/$transport start var=$var host=$TAG_HOST fire_events=$fire_events silence_sec=$silence_sec"
  for ((i = 1; i <= fire_events; i++)); do
    payload="$(build_event_json "$var" "$value_n" "event" "1" "0")"
    send_event "$transport" "$payload"
    sleep "$fire_interval_sec"
  done
  log "count_total/$transport events sent; wait resolve window"
  sleep "$silence_sec"
  log "count_total/$transport done (check notifications/logs for firing->resolved)"
}

scenario_count_window() {
  local transport="$1"
  local var="${VAR:-errors}"
  local value_n="${VALUE_N:-1}"
  local burst_events="${BURST_EVENTS:-5}"
  local burst_interval_sec="${BURST_INTERVAL_SEC:-0.2}"
  local event_type="${EVENT_TYPE:-event}"  # event|agg
  local agg_cnt win_ms silence_sec payload i
  silence_sec="${SILENCE_SEC:-65}"

  if [[ "$event_type" == "agg" ]]; then
    agg_cnt="${AGG_CNT:-2}"
    win_ms="${WIN_MS:-10000}"
  else
    agg_cnt="1"
    win_ms="0"
  fi

  log "count_window/$transport start var=$var host=$TAG_HOST burst_events=$burst_events event_type=$event_type silence_sec=$silence_sec"
  for ((i = 1; i <= burst_events; i++)); do
    payload="$(build_event_json "$var" "$value_n" "$event_type" "$agg_cnt" "$win_ms")"
    send_event "$transport" "$payload"
    sleep "$burst_interval_sec"
  done
  log "count_window/$transport burst sent; wait resolve window"
  sleep "$silence_sec"
  log "count_window/$transport done (check notifications/logs for firing->resolved)"
}

scenario_missing_heartbeat() {
  local transport="$1"
  local var="${VAR:-heartbeat}"
  local value_n="${VALUE_N:-1}"
  local missing_sec="${MISSING_SEC:-12}"
  local payload

  log "missing_heartbeat/$transport baseline var=$var host=$TAG_HOST"
  payload="$(build_event_json "$var" "$value_n" "event" "1" "0")"
  send_event "$transport" "$payload"

  log "missing_heartbeat/$transport wait for missing window: ${missing_sec}s"
  sleep "$missing_sec"

  log "missing_heartbeat/$transport recovery event"
  payload="$(build_event_json "$var" "$value_n" "event" "1" "0")"
  send_event "$transport" "$payload"
  log "missing_heartbeat/$transport done (check notifications/logs for firing->resolved)"
}
