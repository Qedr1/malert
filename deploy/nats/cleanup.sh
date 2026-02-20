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
: "${INGEST_CONSUMER:=alerting-ingest}"
: "${NOTIFY_STREAM:=ALERTING_NOTIFY}"
: "${NOTIFY_CONSUMER:=alerting-notify}"
: "${NOTIFY_DLQ_STREAM:=ALERTING_NOTIFY_DLQ}"
: "${TICK_BUCKET:=tick}"
: "${DATA_BUCKET:=data}"
: "${RESOLVE_CONSUMER:=alerting-resolve}"

if ! command -v nats >/dev/null 2>&1; then
  echo "nats CLI is required (https://github.com/nats-io/natscli)" >&2
  exit 1
fi

nats_cmd=(nats --server "$NATS_URL")

exists_stream() { "${nats_cmd[@]}" stream info "$1" >/dev/null 2>&1; }
exists_consumer() { "${nats_cmd[@]}" consumer info "$1" "$2" >/dev/null 2>&1; }
exists_kv() { "${nats_cmd[@]}" kv info "$1" >/dev/null 2>&1; }

echo "[ingest] remove consumer/stream (if present): $INGEST_CONSUMER, $EVENTS_STREAM"
if exists_consumer "$EVENTS_STREAM" "$INGEST_CONSUMER"; then
  "${nats_cmd[@]}" consumer rm "$EVENTS_STREAM" "$INGEST_CONSUMER" --force >/dev/null
fi
if exists_stream "$EVENTS_STREAM"; then
  "${nats_cmd[@]}" stream rm "$EVENTS_STREAM" --force >/dev/null
fi

echo "[notify_queue] remove consumer/stream (if present): $NOTIFY_CONSUMER, $NOTIFY_STREAM"
if exists_consumer "$NOTIFY_STREAM" "$NOTIFY_CONSUMER"; then
  "${nats_cmd[@]}" consumer rm "$NOTIFY_STREAM" "$NOTIFY_CONSUMER" --force >/dev/null
fi
if exists_stream "$NOTIFY_STREAM"; then
  "${nats_cmd[@]}" stream rm "$NOTIFY_STREAM" --force >/dev/null
fi
echo "[notify_queue] remove DLQ stream (if present): $NOTIFY_DLQ_STREAM"
if exists_stream "$NOTIFY_DLQ_STREAM"; then
  "${nats_cmd[@]}" stream rm "$NOTIFY_DLQ_STREAM" --force >/dev/null
fi

echo "[state] remove consumer/kv (if present): $RESOLVE_CONSUMER, $TICK_BUCKET, $DATA_BUCKET"
if exists_consumer "KV_${TICK_BUCKET}" "$RESOLVE_CONSUMER"; then
  "${nats_cmd[@]}" consumer rm "KV_${TICK_BUCKET}" "$RESOLVE_CONSUMER" --force >/dev/null
fi
if exists_kv "$TICK_BUCKET"; then
  "${nats_cmd[@]}" kv del "$TICK_BUCKET" --force >/dev/null
fi
if exists_kv "$DATA_BUCKET"; then
  "${nats_cmd[@]}" kv del "$DATA_BUCKET" --force >/dev/null
fi

echo "[done] NATS deploy cleanup completed for $NATS_URL"
