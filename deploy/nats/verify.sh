#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${1:-$SCRIPT_DIR/.env}"

if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck source=/dev/null
  source "$ENV_FILE"
  set +a
fi

: "${NATS_URL:=nats://127.0.0.1:4222}"
: "${EVENTS_STREAM:=ALERTING_EVENTS}"
: "${EVENTS_SUBJECT:=alerting.events}"
: "${INGEST_CONSUMER:=alerting-ingest}"
: "${NOTIFY_STREAM:=ALERTING_NOTIFY}"
: "${NOTIFY_SUBJECT:=alerting.notify.jobs}"
: "${NOTIFY_CONSUMER:=alerting-notify}"
: "${TICK_BUCKET:=tick}"
: "${DATA_BUCKET:=data}"
: "${RESOLVE_CONSUMER:=alerting-resolve}"

if ! command -v nats >/dev/null 2>&1; then
  echo "nats CLI is required (https://github.com/nats-io/natscli)" >&2
  exit 1
fi

nats_cmd=(nats --server "$NATS_URL")

echo "[ingest] stream info: $EVENTS_STREAM"
"${nats_cmd[@]}" stream info "$EVENTS_STREAM" >/dev/null
echo "[ingest] consumer info: $EVENTS_STREAM/$INGEST_CONSUMER"
"${nats_cmd[@]}" consumer info "$EVENTS_STREAM" "$INGEST_CONSUMER" >/dev/null

echo "[notify_queue] stream info: $NOTIFY_STREAM"
"${nats_cmd[@]}" stream info "$NOTIFY_STREAM" >/dev/null
echo "[notify_queue] consumer info: $NOTIFY_STREAM/$NOTIFY_CONSUMER"
"${nats_cmd[@]}" consumer info "$NOTIFY_STREAM" "$NOTIFY_CONSUMER" >/dev/null

echo "[state] kv info: $TICK_BUCKET"
"${nats_cmd[@]}" kv info "$TICK_BUCKET" >/dev/null
echo "[state] kv info: $DATA_BUCKET"
"${nats_cmd[@]}" kv info "$DATA_BUCKET" >/dev/null
echo "[state] stream info: KV_${TICK_BUCKET}"
"${nats_cmd[@]}" stream info "KV_${TICK_BUCKET}" >/dev/null
echo "[state] consumer info: KV_${TICK_BUCKET}/$RESOLVE_CONSUMER"
"${nats_cmd[@]}" consumer info "KV_${TICK_BUCKET}" "$RESOLVE_CONSUMER" >/dev/null

echo "[done] NATS deploy verification OK (subjects: $EVENTS_SUBJECT, $NOTIFY_SUBJECT)"
