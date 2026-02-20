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
: "${EVENTS_STORAGE:=file}"
: "${NOTIFY_STREAM:=ALERTING_NOTIFY}"
: "${NOTIFY_SUBJECT:=alerting.notify.jobs}"
: "${NOTIFY_STORAGE:=file}"
: "${NOTIFY_DLQ_STREAM:=ALERTING_NOTIFY_DLQ}"
: "${NOTIFY_DLQ_SUBJECT:=alerting.notify.jobs.dlq}"
: "${NOTIFY_DLQ_STORAGE:=file}"
: "${INGEST_CONSUMER:=alerting-ingest}"
: "${INGEST_DELIVER_GROUP:=alerting-workers}"
: "${INGEST_ACK_WAIT:=30s}"
: "${INGEST_MAX_DELIVER:=-1}"
: "${INGEST_MAX_PENDING:=4096}"
: "${NOTIFY_CONSUMER:=alerting-notify}"
: "${NOTIFY_DELIVER_GROUP:=alerting-notify-workers}"
: "${NOTIFY_ACK_WAIT:=30s}"
: "${NOTIFY_MAX_DELIVER:=-1}"
: "${NOTIFY_MAX_PENDING:=4096}"
: "${TICK_BUCKET:=tick}"
: "${DATA_BUCKET:=data}"
: "${KV_STORAGE:=file}"
: "${RESOLVE_CONSUMER:=alerting-resolve}"
: "${RESOLVE_DELIVER_GROUP:=alerting-resolve}"
: "${RESOLVE_SUBJECT_WILDCARD:=\$KV.${TICK_BUCKET}.>}"

if ! command -v nats >/dev/null 2>&1; then
  echo "nats CLI is required (https://github.com/nats-io/natscli)" >&2
  exit 1
fi

nats_cmd=(nats --server "$NATS_URL")

has_stream() {
  "${nats_cmd[@]}" stream info "$1" >/dev/null 2>&1
}

has_consumer() {
  local stream="$1"
  local consumer="$2"
  "${nats_cmd[@]}" consumer info "$stream" "$consumer" >/dev/null 2>&1
}

has_kv() {
  "${nats_cmd[@]}" kv info "$1" >/dev/null 2>&1
}

echo "[ingest] ensure stream: $EVENTS_STREAM ($EVENTS_SUBJECT)"
if ! has_stream "$EVENTS_STREAM"; then
  "${nats_cmd[@]}" stream add "$EVENTS_STREAM" \
    --subjects "$EVENTS_SUBJECT" \
    --storage "$EVENTS_STORAGE" \
    --retention work \
    --ack \
    --max-age 24h \
    --defaults >/dev/null
fi

echo "[ingest] ensure consumer: $INGEST_CONSUMER"
if ! has_consumer "$EVENTS_STREAM" "$INGEST_CONSUMER"; then
  "${nats_cmd[@]}" consumer add "$EVENTS_STREAM" "$INGEST_CONSUMER" \
    --filter "$EVENTS_SUBJECT" \
    --deliver all \
    --deliver-group "$INGEST_DELIVER_GROUP" \
    --ack explicit \
    --wait "$INGEST_ACK_WAIT" \
    --max-deliver "$INGEST_MAX_DELIVER" \
    --max-pending "$INGEST_MAX_PENDING" \
    --defaults >/dev/null
fi

echo "[notify_queue] ensure stream: $NOTIFY_STREAM ($NOTIFY_SUBJECT)"
if ! has_stream "$NOTIFY_STREAM"; then
  "${nats_cmd[@]}" stream add "$NOTIFY_STREAM" \
    --subjects "$NOTIFY_SUBJECT" \
    --storage "$NOTIFY_STORAGE" \
    --retention work \
    --ack \
    --max-age 24h \
    --defaults >/dev/null
fi

echo "[notify_queue] ensure DLQ stream: $NOTIFY_DLQ_STREAM ($NOTIFY_DLQ_SUBJECT)"
if ! has_stream "$NOTIFY_DLQ_STREAM"; then
  "${nats_cmd[@]}" stream add "$NOTIFY_DLQ_STREAM" \
    --subjects "$NOTIFY_DLQ_SUBJECT" \
    --storage "$NOTIFY_DLQ_STORAGE" \
    --retention limits \
    --max-age 168h \
    --defaults >/dev/null
fi

echo "[notify_queue] ensure consumer: $NOTIFY_CONSUMER"
if ! has_consumer "$NOTIFY_STREAM" "$NOTIFY_CONSUMER"; then
  "${nats_cmd[@]}" consumer add "$NOTIFY_STREAM" "$NOTIFY_CONSUMER" \
    --filter "$NOTIFY_SUBJECT" \
    --deliver all \
    --deliver-group "$NOTIFY_DELIVER_GROUP" \
    --ack explicit \
    --wait "$NOTIFY_ACK_WAIT" \
    --max-deliver "$NOTIFY_MAX_DELIVER" \
    --max-pending "$NOTIFY_MAX_PENDING" \
    --defaults >/dev/null
fi

echo "[state] ensure KV buckets: $TICK_BUCKET, $DATA_BUCKET"
if ! has_kv "$TICK_BUCKET"; then
  "${nats_cmd[@]}" kv add "$TICK_BUCKET" --storage "$KV_STORAGE" >/dev/null
fi
if ! has_kv "$DATA_BUCKET"; then
  "${nats_cmd[@]}" kv add "$DATA_BUCKET" --storage "$KV_STORAGE" >/dev/null
fi

echo "[state] ensure resolve consumer on KV_${TICK_BUCKET}"
tick_stream="KV_${TICK_BUCKET}"
if ! has_stream "$tick_stream"; then
  echo "tick stream $tick_stream not found after KV bootstrap" >&2
  exit 1
fi
if ! has_consumer "$tick_stream" "$RESOLVE_CONSUMER"; then
  "${nats_cmd[@]}" consumer add "$tick_stream" "$RESOLVE_CONSUMER" \
    --filter "$RESOLVE_SUBJECT_WILDCARD" \
    --deliver new \
    --deliver-group "$RESOLVE_DELIVER_GROUP" \
    --ack explicit \
    --wait "$INGEST_ACK_WAIT" \
    --max-deliver "$INGEST_MAX_DELIVER" \
    --max-pending "$INGEST_MAX_PENDING" \
    --defaults >/dev/null
fi

echo "[done] NATS bootstrap completed for $NATS_URL"
