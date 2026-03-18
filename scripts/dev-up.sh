#!/usr/bin/env bash
set -euo pipefail

echo "==> Starting Postgres..."
docker run -d --name sleepy-pg \
  -e POSTGRES_USER=sleepy \
  -e POSTGRES_PASSWORD=sleepy \
  -e POSTGRES_DB=sleepy \
  -p 5433:5432 \
  postgres:16-alpine 2>/dev/null || echo "    (container already exists)"

echo "==> Waiting for Postgres to be ready..."
until docker exec sleepy-pg pg_isready -U sleepy -q 2>/dev/null; do
  sleep 1
done
echo "    ready."

echo "==> Running migrations..."
for f in $(ls internal/db/migrations/*.sql | sort); do
  echo "    $f"
  docker exec -i sleepy-pg psql -U sleepy -d sleepy < "$f"
done

echo ""
echo "==> Done. Start the server:"
echo ""
echo '  export PG_DSN="postgres://sleepy:sleepy@localhost:5432/sleepy?sslmode=disable"'
echo '  export GROQ_API_KEY="gsk_..."     # for test mode'
echo '  export ASSET_ROOT="./tmp/assets"'
echo '  go run ./cmd/api'
echo ""
echo "  Then open http://localhost:8080"
