# sleepy

Episode pack generator for sleep narration YouTube content.

Pipeline: `PENDING → SCRIPTED → VOICED → THUMBNAILED → RENDERED → PACKAGED → DONE`

## Prerequisites

- Go 1.23+
- PostgreSQL 16+ (Docker recommended)
- ffmpeg / ffprobe (with libx264, aac)
- Docker (for the dev Postgres container)

## Quick start

```bash
# 1. Start Postgres and run all migrations
bash scripts/dev-up.sh

# 2. Start the API server (serves UI + API)
export PG_DSN="postgres://sleepy:sleepy@localhost:5433/sleepy?sslmode=disable"

# Optional: seed API keys from env vars on first startup
export GROQ_API_KEY="gsk_..."           # test mode (Groq + Edge TTS)
export OPENAI_API_KEY="sk-..."          # prod mode
export ELEVENLABS_API_KEY="..."         # prod mode
export ELEVENLABS_VOICE_ID="..."        # prod mode

export ASSET_ROOT="./tmp/assets"        # where generated files are stored

go run ./cmd/api
```

3. Open **http://localhost:8080** in your browser
4. Go to the **Settings** tab — configure API keys and select mode (`test` or `prod`)
5. Go to **New Run** — fill in series, episode, style, duration
6. Click **Start Worker** to begin processing
7. Monitor progress on the run detail page; approve voice when prompted

> **API keys** can be provided either as environment variables (loaded into the DB on first startup if the DB field is empty) or entered directly in the Settings UI.

## Output

Each completed run produces under `$ASSET_ROOT/<run-id>/`:

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
| `PG_DSN` | yes | `postgres://localhost:5432/sleepy?sslmode=disable` | Postgres connection string |
| `ASSET_ROOT` | no | `tmp/assets` | Root dir for generated assets |
| `ADDR` | no | `:8080` | HTTP listen address |
| `GROQ_API_KEY` | no* | — | Groq API key (test mode) — seeded to DB on first start |
| `OPENAI_API_KEY` | no* | — | OpenAI API key (prod mode) — seeded to DB on first start |
| `OPENAI_BASE_URL` | no | `https://api.openai.com/v1` | API base URL (prod mode) |
| `OPENAI_MODEL` | no | `gpt-4o` | Chat model (prod mode) |
| `ELEVENLABS_API_KEY` | no* | — | ElevenLabs API key (prod mode) — seeded to DB on first start |
| `ELEVENLABS_VOICE_ID` | no* | — | ElevenLabs voice ID (prod mode) — seeded to DB on first start |

\* Required for the selected mode, but can be set via the Settings UI instead of env vars.

## Modes

| Mode | LLM | TTS |
|---|---|---|
| `test` | Groq (`llama-3.3-70b-versatile`) | Edge TTS (free, Microsoft) |
| `openai` | OpenAI (`gpt-4o`) | Edge TTS (free, Microsoft) |
| `prod` | OpenAI (`gpt-4o`) | ElevenLabs |

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
