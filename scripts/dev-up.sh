#!/usr/bin/env bash
set -euo pipefail

echo "==> Starting Postgres..."
docker run -d --name sleepy-pg \
  -e POSTGRES_USER=sleepy \
  -e POSTGRES_PASSWORD=sleepy \
  -e POSTGRES_DB=sleepy \
  -p 5432:5432 \
  postgres:16-alpine 2>/dev/null || echo "    (container already exists)"

echo "==> Waiting for Postgres to be ready..."
until docker exec sleepy-pg pg_isready -U sleepy -q 2>/dev/null; do
  sleep 1
done
echo "    ready."

echo "==> Running migrations..."
docker exec -i sleepy-pg psql -U sleepy -d sleepy < internal/db/migrations/001_init.sql

echo ""
echo "==> Done. To create a test run:"
echo ""
echo '  docker exec -i sleepy-pg psql -U sleepy -d sleepy <<SQL'
echo "  INSERT INTO runs (series, episode, style, duration_min)"
echo "  VALUES ('Cosmos', 'Nebula Gardens', 'Cosmos', 5)"
echo "  RETURNING id;"
echo "  SQL"
echo ""
echo '  # Then enqueue it (replace <run-id>):'
echo '  docker exec -i sleepy-pg psql -U sleepy -d sleepy <<SQL'
echo "  INSERT INTO job_queue (run_id, job_type) VALUES ('<run-id>', 'RUN_PIPELINE');"
echo "  SQL"
echo ""
echo "  # Start the worker:"
echo '  export PG_DSN="postgres://sleepy:sleepy@localhost:5432/sleepy?sslmode=disable"'
echo '  export OPENAI_API_KEY="sk-..."'
echo '  export ELEVENLABS_API_KEY="..."'
echo '  export ELEVENLABS_VOICE_ID="..."'
echo '  export ASSET_ROOT="./tmp/assets"'
echo '  go run ./cmd/worker'
