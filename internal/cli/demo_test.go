package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/MatheusGustav/mister/internal/canvas"
)

func TestDemo_NeedsATerminal(t *testing.T) {
	out, _, err := run(t, DefaultDeps(), "", "demo")
	if err == nil || !strings.Contains(err.Error(), "terminal") || out != "" {
		t.Errorf("demo = %q, %v", out, err)
	}
}

func TestDemo_StaysOutOfTheHelp(t *testing.T) {
	out, _, err := run(t, DefaultDeps(), "", "--help")
	if err != nil || strings.Contains(out, "demo") {
		t.Errorf("help = %q, %v", out, err)
	}
}

func TestDemoScene_DrawsAtAnySize(t *testing.T) {
	moves := regexp.MustCompile(`\x1b\[\d+;1H`)
	for _, size := range [][2]int{{0, 0}, {1, 1}, {3, 2}, {30, 10}, {120, 40}, {40, 120}} {
		scene := newDemoScene(size[0], size[1])
		if !scene.fits(size[0], size[1]) || scene.fits(size[0]+1, size[1]) {
			t.Errorf("%v: fits answers wrong", size)
		}
		for _, at := range []float64{0, 0.7, 3, 60} {
			rows := moves.Split(scene.frame(at, canvas.NoColor), -1)[1:]
			if len(rows) != size[1] {
				t.Fatalf("%v at %vs: %d rows", size, at, len(rows))
			}
			for _, row := range rows {
				if len([]rune(row)) != size[0] {
					t.Fatalf("%v at %vs: row %q", size, at, row)
				}
			}
		}
	}

	settled := newDemoScene(120, 40).frame(3, canvas.NoColor)
	for _, want := range []string{demoTitle, demoHint, "@", "|", "-", "/", "\\"} {
		if !strings.Contains(settled, want) {
			t.Errorf("the frame lacks %q", want)
		}
	}
	if scrambling := newDemoScene(120, 40).frame(0, canvas.NoColor); strings.Contains(scrambling, demoTitle) {
		t.Error("the title is settled from the first frame")
	}
}
