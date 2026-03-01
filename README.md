# sleepy

Episode pack generator for sleep narration YouTube content.

Pipeline: `PENDING → SCRIPTED → VOICED → THUMBNAILED → RENDERED → PACKAGED → DONE`

## Prerequisites

- Go 1.23+
- PostgreSQL 16+
- ffmpeg / ffprobe (with libx264, aac)
- OpenAI-compatible API key
- ElevenLabs API key + voice ID

## Quick start

```bash
# 1. Start Postgres and run migrations
bash scripts/dev-up.sh

# 2. Set env vars
export PG_DSN="postgres://sleepy:sleepy@localhost:5432/sleepy?sslmode=disable"
export OPENAI_API_KEY="sk-..."
export OPENAI_MODEL="gpt-4o-mini"              # optional, default gpt-4o
export OPENAI_BASE_URL="https://api.openai.com/v1"  # optional
export ELEVENLABS_API_KEY="..."
export ELEVENLABS_VOICE_ID="..."
export ASSET_ROOT="./tmp/assets"
export FFMPEG_BIN="ffmpeg"                # optional
export FFPROBE_BIN="ffprobe"              # optional

# 3. Create a run + enqueue
docker exec -i sleepy-pg psql -U sleepy -d sleepy <<'SQL'
INSERT INTO runs (series, episode, style, duration_min)
VALUES ('Cosmos', 'Nebula Gardens', 'Cosmos', 5)
RETURNING id;
SQL

# Use the returned UUID:
docker exec -i sleepy-pg psql -U sleepy -d sleepy <<'SQL'
INSERT INTO job_queue (run_id, job_type)
VALUES ('<paste-uuid>', 'RUN_PIPELINE');
SQL

# 4. Start worker
go run ./cmd/worker
```

The worker picks up the job and produces under `$ASSET_ROOT/<run-id>/`:

```
script.md
script.ssml
narration.wav
thumbnail.png
video.mp4
metadata.json
episode_pack.zip
```

## Env vars reference

| Variable | Required | Default | Description |
|---|---|---|---|
| `PG_DSN` | yes | — | Postgres connection string |
| `OPENAI_API_KEY` | yes | — | OpenAI (or compatible) API key |
| `OPENAI_BASE_URL` | no | `https://api.openai.com/v1` | API base URL |
| `OPENAI_MODEL` | no | `gpt-4o` | Chat model to use |
| `ELEVENLABS_API_KEY` | yes | — | ElevenLabs API key |
| `ELEVENLABS_VOICE_ID` | yes | — | ElevenLabs voice ID |
| `ASSET_ROOT` | yes | — | Root dir for generated assets |
| `FFMPEG_BIN` | no | `ffmpeg` | Path to ffmpeg binary |
| `FFPROBE_BIN` | no | `ffprobe` | Path to ffprobe binary |

## Style presets

- **Cosmos** — deep space, nebulae, stars, cosmic silence
- **Earthside** — forests, lakes, rainfall, meadows at dusk
- **Myth** — enchanted gardens, sleeping libraries, mythical creatures

## QA gate

Scripts are automatically validated for sleep safety:
- Rejects high-tension words (suddenly, blood, scream, terror, panic, etc.)
- Rejects excessive exclamation marks (>2) or ALL CAPS words (>3)
- Rejects average sentence length above 25 words
- Rejects high repetition ratio (<0.30 unique/total)
- Auto-retries LLM generation up to 2 times with feedback on failure
