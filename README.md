# sleepy

Episode pack generator for sleep narration YouTube content.

Pipeline: `PENDING → SCRIPTED → VOICED → THUMBNAILED → RENDERED → PACKAGED → DONE`

## Prerequisites

- Go 1.23+
- PostgreSQL 16+
- ffmpeg / ffprobe (with libx264, aac)
- OpenAI-compatible API key
- Python 3.9+ with `kokoro`, `soundfile`, `numpy` (`pip install kokoro soundfile numpy`) — for local Kokoro TTS (default TTS engine)
- ElevenLabs API key + voice ID — optional, only needed for legacy `prod` mode

## Quick start

```bash
# 1. Start Postgres and run migrations
bash scripts/dev-up.sh

# 2. Set env vars
export PG_DSN="postgres://sleepy:sleepy@localhost:5432/sleepy?sslmode=disable"
export OPENAI_API_KEY="sk-..."
export OPENAI_MODEL="gpt-4o"              # optional, default gpt-4o
export OPENAI_BASE_URL="https://api.openai.com/v1"  # optional
export ASSET_ROOT="./tmp/assets"
export FFMPEG_BIN="ffmpeg"                # optional
export FFPROBE_BIN="ffprobe"              # optional

# Only needed for legacy "prod" (ElevenLabs) mode — not required for kokoro mode
export ELEVENLABS_API_KEY="..."
export ELEVENLABS_VOICE_ID="..."

# Install local Kokoro TTS deps once (no API key required):
pip install kokoro soundfile numpy

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

# 4. In Settings (UI or API), set worker mode to `kokoro` (default TTS engine — free,
#    local, no API key). `test` (Edge TTS) and `prod` (ElevenLabs, legacy) are also
#    available. Kokoro voice/speed are configured there too (defaults: af_sky, 0.85).

# 5. Start worker
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
| `ASSET_ROOT` | yes | — | Root dir for generated assets |
| `FFMPEG_BIN` | no | `ffmpeg` | Path to ffmpeg binary |
| `FFPROBE_BIN` | no | `ffprobe` | Path to ffprobe binary |
| `ELEVENLABS_API_KEY` | only for `prod` mode | — | ElevenLabs API key (legacy TTS) |
| `ELEVENLABS_VOICE_ID` | only for `prod` mode | — | ElevenLabs voice ID (legacy TTS) |

Kokoro TTS (default, `kokoro` mode) needs no env var / API key — voice and speed are
set per-worker in Settings (`kokoro_voice`, `kokoro_speed`; defaults `af_sky`, `0.85`),
and it runs locally via `scripts/kokoro_tts.py` (requires `pip install kokoro soundfile numpy`).

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
