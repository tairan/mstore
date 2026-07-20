package naming

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	got, err := Normalize("Qwen/Qwen3_TTS 12Hz--1.7B.CustomVoice")
	if err != nil {
		t.Fatal(err)
	}
	if want := "qwen3-tts-12hz-1-7b-customvoice"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNormalizeRejectsLongAndUnicode(t *testing.T) {
	if _, err := Normalize(strings.Repeat("a", 65)); err == nil {
		t.Fatal("expected long name error")
	}
	if _, err := Normalize("模型"); err == nil {
		t.Fatal("expected non-ASCII error")
	}
}
