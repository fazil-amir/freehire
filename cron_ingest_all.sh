#!/usr/bin/env bash
set -uo pipefail
APP_CONTAINER="freehire-app-1"

for p in whatjobs-in keka zohorecruit greenhouse ashby; do
  docker exec "$APP_CONTAINER" /app/ingest "$p" || echo "[ingest failed for $p, continuing]"
done

docker exec "$APP_CONTAINER" /app/reindex
