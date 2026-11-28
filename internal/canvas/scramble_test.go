package canvas

import (
	"strings"
	"testing"
)

func TestScramble(t *testing.T) {
	const text = "chave da api"

	if got := Scramble(text, 1, 7); got != text {
		t.Errorf("settled: %q", got)
	}
	if got := Scramble(text, 5, 7); got != text {
		t.Errorf("past the end: %q", got)
	}

	start := Scramble(text, 0, 7)
	if len([]rune(start)) != len([]rune(text)) {
		t.Errorf("at the start the length changed: %q", start)
	}
	for i, r := range start {
		if (r == ' ') != (text[i] == ' ') || strings.ContainsRune("chavedpi", r) {
			t.Errorf("at the start, position %d shows %q: %q", i, r, start)
		}
	}
	if start != Scramble(text, -1, 7) {
		t.Errorf("before the start is not the start: %q", Scramble(text, -1, 7))
	}

	half := Scramble(text, 0.5, 7)
	if !strings.HasPrefix(half, "chave ") || half == text {
		t.Errorf("halfway: %q", half)
	}

	if start != Scramble(text, 0, 7) {
		t.Error("the same tick gave another text")
	}
	if start == Scramble(text, 0, 8) {
		t.Errorf("the next tick gave the same text: %q", start)
	}
	if got := Scramble("", 0, 0); got != "" {
		t.Errorf("empty text: %q", got)
	}
	if got := Scramble("ação", 0.5, 3); !strings.HasPrefix(got, "aç") || len([]rune(got)) != 4 {
		t.Errorf("accented text: %q", got)
	}
}
