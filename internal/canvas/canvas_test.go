package canvas

import (
	"math"
	"regexp"
	"strings"
	"testing"
)

var (
	moveRe  = regexp.MustCompile(`\x1b\[\d+;1H`)
	colorRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	white   = Color{255, 255, 255}
	facing  = Vec3{Z: -1}
)

// rows returns what each row of the frame shows, without the colors.
func rows(c *Canvas) []string {
	return moveRe.Split(c.Frame(NoColor), -1)[1:]
}

// paint fills the cells that pick accepts with a surface at this depth.
func paint(c *Canvas, depth, light float64, pick func(col, row int) bool) {
	for row := range c.rows {
		for col := range c.cols {
			if pick(col, row) {
				c.cells[row*c.cols+col] = cell{kind: surface, color: white, depth: depth, light: light}
			}
		}
	}
}

func TestSurface_OriginFallsInTheMiddle(t *testing.T) {
	c := New(21, 11)
	c.Surface(Vec3{}, facing, white)
	got := rows(c)
	if got[5][10] == ' ' || strings.Count(strings.Join(got, ""), " ") != 21*11-1 {
		t.Errorf("frame = %q", got)
	}
}

func TestProject_KeepsTheShapeOnTallCells(t *testing.T) {
	c := New(80, 20)
	c.Dot(Vec3{X: 0.5}, 'x', white)
	c.Dot(Vec3{Y: 0.5}, 'y', white)
	got := rows(c)
	// Half a unit is 5 rows up and, with cells twice as tall as wide, 10 columns right.
	if got[10][50] != 'x' || got[5][40] != 'y' {
		t.Errorf("frame = %q", got)
	}
}

func TestDot_NearestWins(t *testing.T) {
	for _, order := range [][]float64{{1, -1}, {-1, 1}} {
		c := New(9, 5)
		for _, z := range order {
			ch := 'f'
			if z < 0 {
				ch = 'n'
			}
			c.Dot(Vec3{Z: z}, ch, white)
		}
		if got := rows(c); got[2][4] != 'n' {
			t.Errorf("drawn in the order %v: frame = %q", order, got)
		}
	}
}

func TestDot_BehindTheViewerIsLeftOut(t *testing.T) {
	c := New(9, 5)
	c.Dot(Vec3{Z: -c.Distance}, 'x', white)
	c.Dot(Vec3{X: 50}, 'x', white)
	if got := strings.Join(rows(c), ""); strings.TrimSpace(got) != "" {
		t.Errorf("frame = %q", got)
	}
}

func TestFrame_OutlineFollowsTheEdge(t *testing.T) {
	cases := []struct {
		name     string
		pick     func(col, row int) bool
		col, row int
		want     byte
	}{
		{"top of a block", func(col, row int) bool { return row >= 3 }, 10, 3, '-'},
		{"bottom of a block", func(col, row int) bool { return row <= 6 }, 10, 6, '-'},
		{"left of a block", func(col, row int) bool { return col >= 5 }, 5, 5, '|'},
		{"right of a block", func(col, row int) bool { return col <= 14 }, 14, 5, '|'},
		{"slope going down to the right", func(col, row int) bool { return col <= row }, 5, 5, '\\'},
		{"slope going up to the right", func(col, row int) bool { return col+row >= 10 }, 5, 5, '/'},
		{"inside the block", func(col, row int) bool { return true }, 5, 5, '@'},
	}
	for _, tc := range cases {
		c := New(20, 10)
		paint(c, 3, 1, tc.pick)
		if got := rows(c)[tc.row][tc.col]; got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFrame_OutlineOnlyOnTheSurfaceInFront(t *testing.T) {
	c := New(20, 10)
	paint(c, 3, 1, func(col, _ int) bool { return col < 10 })
	paint(c, 3+2*edgeGap, 1, func(col, _ int) bool { return col >= 10 })
	if got := rows(c)[5][8:12]; got != "@|@@" {
		t.Errorf("around the step in depth: %q", got)
	}
}

func TestFrame_LightPicksTheFill(t *testing.T) {
	for light, want := range map[float64]byte{0: '.', 0.5: '*', 1: '@'} {
		c := New(5, 5)
		paint(c, 3, light, func(_, _ int) bool { return true })
		if got := rows(c)[2][2]; got != want {
			t.Errorf("light %v: got %q, want %q", light, got, want)
		}
	}
}

func TestFrame_Colors(t *testing.T) {
	c := New(4, 1)
	c.Text(0, 0, "ab", Color{10, 20, 30})
	c.Text(2, 0, "c", Color{255, 0, 0})

	if got, want := c.Frame(TrueColor), "\x1b[1;1H\x1b[38;2;10;20;30mab\x1b[38;2;255;0;0mc \x1b[39m"; got != want {
		t.Errorf("true color:\n got %q\nwant %q", got, want)
	}
	if got, want := c.Frame(Color256), "\x1b[1;1H\x1b[38;5;16mab\x1b[38;5;196mc \x1b[39m"; got != want {
		t.Errorf("256 colors:\n got %q\nwant %q", got, want)
	}
	if got := c.Frame(NoColor); colorRe.MatchString(got) {
		t.Errorf("no color: %q", got)
	}
}

func TestFrame_FartherIsDarker(t *testing.T) {
	c := New(40, 5)
	c.Dot(Vec3{X: -0.5, Z: -1}, 'n', white)
	c.Dot(Vec3{X: 0.5, Z: 1}, 'f', white)
	frame := c.Frame(TrueColor)
	if !strings.Contains(frame, "\x1b[38;2;255;255;255mn") || !strings.Contains(frame, "\x1b[38;2;77;77;77mf") {
		t.Errorf("frame = %q", frame)
	}
}

func TestText_ClipsAtTheBorder(t *testing.T) {
	c := New(4, 2)
	c.Text(-2, 0, "abcdefgh", white)
	c.Text(0, 5, "fora", white)
	c.Text(0, -1, "fora", white)
	if got := rows(c); got[0] != "cdef" || got[1] != "    " {
		t.Errorf("frame = %q", got)
	}
}

func TestCanvas_WithoutSize(t *testing.T) {
	for _, size := range [][2]int{{0, 0}, {-3, 5}, {5, 0}} {
		c := New(size[0], size[1])
		c.Surface(Vec3{}, facing, white)
		c.Dot(Vec3{}, 'x', white)
		c.Text(0, 0, "texto", white)
		c.Clear()
		if got := colorRe.ReplaceAllString(moveRe.ReplaceAllString(c.Frame(TrueColor), ""), ""); got != "" {
			t.Errorf("%v: frame = %q", size, got)
		}
	}
}

func TestClear(t *testing.T) {
	c := New(4, 2)
	c.Text(0, 0, "abcd", white)
	c.Clear()
	if got := strings.Join(rows(c), ""); strings.TrimSpace(got) != "" {
		t.Errorf("frame = %q", got)
	}
}

func TestIndex256(t *testing.T) {
	cases := map[Color]int{
		{0, 0, 0}:       16,
		{255, 255, 255}: 231,
		{255, 0, 0}:     196,
		{40, 40, 40}:    16,
		{60, 60, 60}:    59,
		{0, 0, 120}:     18,
	}
	for color, want := range cases {
		if got := color.index256(); got != want {
			t.Errorf("index256(%v) = %d, want %d", color, got, want)
		}
	}
}

func TestStrokeChar(t *testing.T) {
	cases := []struct {
		gx, gy float64
		want   rune
	}{
		{1, 0, '|'}, {-1, 0, '|'},
		{0, 1, '-'}, {0, -1, '-'},
		{1, 1, '/'}, {-1, -1, '/'},
		{-1, 1, '\\'}, {1, -1, '\\'},
	}
	for _, tc := range cases {
		if got := strokeChar(tc.gx, tc.gy); got != tc.want {
			t.Errorf("strokeChar(%v, %v) = %q, want %q", tc.gx, tc.gy, got, tc.want)
		}
	}
}

func TestTurn(t *testing.T) {
	cases := []struct {
		aroundY, aroundX float64
		in, want         Vec3
	}{
		{0, 0, Vec3{1, 2, 3}, Vec3{1, 2, 3}},
		{math.Pi / 2, 0, Vec3{X: 1}, Vec3{Z: -1}},
		{0, math.Pi / 2, Vec3{Y: 1}, Vec3{Z: 1}},
		{math.Pi / 2, math.Pi / 2, Vec3{X: 1}, Vec3{Y: 1}},
	}
	for _, tc := range cases {
		got := NewTurn(tc.aroundY, tc.aroundX).Apply(tc.in)
		if d := got.Sub(tc.want); d.Dot(d) > 1e-18 {
			t.Errorf("NewTurn(%v, %v).Apply(%v) = %v, want %v", tc.aroundY, tc.aroundX, tc.in, got, tc.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	got := (Vec3{3, 0, 4}).Normalize()
	if d := got.Sub(Vec3{0.6, 0, 0.8}); d.Dot(d) > 1e-18 {
		t.Errorf("Normalize = %v", got)
	}
	if got := (Vec3{}).Normalize(); got != (Vec3{}) {
		t.Errorf("Normalize of zero = %v", got)
	}
}

func TestCross(t *testing.T) {
	if got := (Vec3{X: 1}).Cross(Vec3{Y: 1}); got != (Vec3{Z: 1}) {
		t.Errorf("Cross = %v", got)
	}
}
