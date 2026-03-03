package ttsreliability

import "testing"

func TestIdempotencyKey_Stable(t *testing.T) {
	s := TTSSettings{Speed: 0.80, Stability: 0.75, ModelID: "eleven_monolingual_v1"}
	key1 := IdempotencyKey("run-abc", 0, 1, s)
	key2 := IdempotencyKey("run-abc", 0, 1, s)
	if key1 != key2 {
		t.Errorf("same input should produce same key: %q != %q", key1, key2)
	}
}

func TestIdempotencyKey_DifferentSettings(t *testing.T) {
	s1 := TTSSettings{Speed: 0.80}
	s2 := TTSSettings{Speed: 0.90}
	key1 := IdempotencyKey("run-abc", 0, 1, s1)
	key2 := IdempotencyKey("run-abc", 0, 1, s2)
	if key1 == key2 {
		t.Error("different settings should produce different keys")
	}
}

func TestIdempotencyKey_DifferentChunk(t *testing.T) {
	s := TTSSettings{Speed: 0.80}
	key1 := IdempotencyKey("run-abc", 0, 1, s)
	key2 := IdempotencyKey("run-abc", 1, 1, s)
	if key1 == key2 {
		t.Error("different chunk index should produce different keys")
	}
}

func TestIdempotencyKey_DifferentAttempt(t *testing.T) {
	s := TTSSettings{Speed: 0.80}
	key1 := IdempotencyKey("run-abc", 0, 1, s)
	key2 := IdempotencyKey("run-abc", 0, 2, s)
	if key1 == key2 {
		t.Error("different attempt number should produce different keys")
	}
}

func TestIdempotencyKey_DifferentRun(t *testing.T) {
	s := TTSSettings{}
	key1 := IdempotencyKey("run-abc", 0, 1, s)
	key2 := IdempotencyKey("run-def", 0, 1, s)
	if key1 == key2 {
		t.Error("different run IDs should produce different keys")
	}
}
