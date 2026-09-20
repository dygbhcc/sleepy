#!/usr/bin/env python3
"""
Kokoro TTS synthesis wrapper for the Sleepy pipeline.
Usage: python3 kokoro_tts.py <text_file> <voice> <out_wav> [speed]

Install deps: pip install kokoro soundfile numpy
"""

import sys


def main():
    if len(sys.argv) < 4:
        print("usage: kokoro_tts.py <text_file> <voice> <out_wav> [speed]", file=sys.stderr)
        sys.exit(1)

    text_file, voice, out_wav = sys.argv[1], sys.argv[2], sys.argv[3]
    speed = float(sys.argv[4]) if len(sys.argv) > 4 else 1.0

    try:
        import numpy as np
        from kokoro import KPipeline
        import soundfile as sf
    except ImportError as e:
        print(f"error: missing dependency — {e}\nRun: pip install kokoro soundfile numpy", file=sys.stderr)
        sys.exit(1)

    with open(text_file, "r", encoding="utf-8") as f:
        text = f.read().strip()

    if not text:
        print("error: empty text input", file=sys.stderr)
        sys.exit(1)

    # Infer Kokoro language code from voice name prefix.
    # Convention: af_sky → 'a' (American English), bf_emma → 'b' (British English), etc.
    lang_code = voice[0] if voice else "a"

    pipeline = KPipeline(lang_code=lang_code)

    segments = []
    for _, _, audio in pipeline(text, voice=voice, speed=speed):
        if audio is not None and len(audio) > 0:
            segments.append(audio)

    if not segments:
        print("error: kokoro produced no audio output", file=sys.stderr)
        sys.exit(1)

    combined = np.concatenate(segments)
    sf.write(out_wav, combined, 24000)
    print(f"wrote {len(combined)} samples ({len(combined)/24000:.1f}s) to {out_wav}", file=sys.stderr)


if __name__ == "__main__":
    main()
