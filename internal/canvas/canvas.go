// Package canvas draws on the terminal with a grid of colored characters. A
// scene in three dimensions is projected onto the grid, and each cell shows
// the character and the color of what is nearest to the viewer.
package canvas

import (
	"math"
	"strconv"
	"strings"
)

const (
	// cellAspect is how many times a cell of the terminal is taller than wide.
	cellAspect = 2.0
	// nearPlane is the closest to the viewer a point can be and still be drawn.
	nearPlane = 0.1
	// edgeGap is the difference in depth that tells two surfaces apart.
	edgeGap = 0.15
	// fill goes from the darkest to the brightest surface. It has no glyph that
	// looks like a stroke, so the outline stands out.
	fill = ".:;+*o#%@"
)

// Color is the color of a character.
type Color struct{ R, G, B uint8 }

func (c Color) scale(f float64) Color {
	channel := func(v uint8) uint8 { return uint8(math.Round(clamp(float64(v)*f, 0, 255))) }
	return Color{channel(c.R), channel(c.G), channel(c.B)}
}

// cubeLevels are the values each channel takes in the 6x6x6 color cube every
// 256-color terminal has.
var cubeLevels = [6]int{0, 95, 135, 175, 215, 255}

// index256 is the color of that cube nearest to c.
func (c Color) index256() int {
	level := func(v uint8) int {
		nearest := 0
		for i, value := range cubeLevels {
			if abs(value-int(v)) < abs(cubeLevels[nearest]-int(v)) {
				nearest = i
			}
		}
		return nearest
	}
	return 16 + 36*level(c.R) + 6*level(c.G) + level(c.B)
}

// ColorMode is how many colors the terminal takes.
type ColorMode int

const (
	TrueColor ColorMode = iota
	Color256
	NoColor
)

type kind uint8

const (
	empty kind = iota
	// surface is a lit point: its character is chosen when the frame is written.
	surface
	// glyph is a character given by the caller.
	glyph
)

type cell struct {
	kind  kind
	ch    rune
	color Color
	depth float64
	light float64
}

// Canvas is one frame being drawn. The viewer is at (0, 0, -Distance) looking
// at the origin; at the origin, Y from -1 to 1 fills the height of the grid.
type Canvas struct {
	cols, rows int
	cells      []cell

	// Distance is how far the viewer is from the origin of the scene.
	Distance float64
	// Light is the direction from the scene to the light.
	Light Vec3
}

// New returns an empty canvas of cols by rows cells.
func New(cols, rows int) *Canvas {
	cols, rows = max(cols, 0), max(rows, 0)
	return &Canvas{
		cols:     cols,
		rows:     rows,
		cells:    make([]cell, cols*rows),
		Distance: 3,
		Light:    Vec3{-0.5, 0.6, -0.65}.Normalize(),
	}
}

// Size returns the columns and the rows of the grid.
func (c *Canvas) Size() (cols, rows int) { return c.cols, c.rows }

// Clear empties every cell, for the next frame.
func (c *Canvas) Clear() { clear(c.cells) }

// Surface draws a point of a lit surface. normal is the direction the surface
// faces at that point.
func (c *Canvas) Surface(p, normal Vec3, color Color) {
	if at, depth, ok := c.project(p); ok {
		at.kind, at.color, at.depth = surface, color, depth
		at.light = clamp(normal.Dot(c.Light), 0, 1)
	}
}

// Dot draws one character at a point of the scene.
func (c *Canvas) Dot(p Vec3, ch rune, color Color) {
	if at, depth, ok := c.project(p); ok {
		at.kind, at.ch, at.color, at.depth = glyph, ch, color, depth
	}
}

// Text writes s over the scene, one cell per character, starting at the cell
// (col, row). What falls off the grid is left out.
func (c *Canvas) Text(col, row int, s string, color Color) {
	if row < 0 || row >= c.rows {
		return
	}
	for _, r := range s {
		if col >= 0 && col < c.cols {
			c.cells[row*c.cols+col] = cell{kind: glyph, ch: r, color: color}
		}
		col++
	}
}

// project finds the cell a point falls on. It answers false when the point is
// off the grid, behind the viewer or behind what the cell already shows.
func (c *Canvas) project(p Vec3) (at *cell, depth float64, ok bool) {
	depth = c.Distance + p.Z
	if depth < nearPlane {
		return nil, 0, false
	}
	unit := math.Min(float64(c.rows)/2, float64(c.cols)/(2*cellAspect))
	zoom := c.Distance / depth * unit
	col := int(math.Floor(float64(c.cols)/2 + p.X*zoom*cellAspect))
	row := int(math.Floor(float64(c.rows)/2 - p.Y*zoom))
	if col < 0 || col >= c.cols || row < 0 || row >= c.rows {
		return nil, 0, false
	}
	at = &c.cells[row*c.cols+col]
	if at.kind != empty && at.depth <= depth {
		return nil, 0, false
	}
	return at, depth, true
}

// Frame returns what to write to the terminal to show the canvas: every row
// placed by its own cursor move, so it does not depend on line breaks.
func (c *Canvas) Frame(mode ColorMode) string {
	var b strings.Builder
	b.Grow(c.rows * (c.cols*4 + 8))
	current := -1
	for row := range c.rows {
		b.WriteString("\x1b[")
		b.WriteString(strconv.Itoa(row + 1))
		b.WriteString(";1H")
		for col := range c.cols {
			at := c.cells[row*c.cols+col]
			if at.kind == empty {
				b.WriteByte(' ')
				continue
			}
			ch, shade := at.ch, c.fog(at.depth)
			if at.kind == surface {
				ch = c.surfaceChar(col, row)
				shade *= 0.3 + 0.7*at.light
			}
			if mode != NoColor {
				current = writeColor(&b, at.color.scale(shade), mode, current)
			}
			b.WriteRune(ch)
		}
	}
	if current >= 0 {
		b.WriteString("\x1b[39m")
	}
	return b.String()
}

// writeColor changes the color of the text when it is not the current one, and
// returns the one in use.
func writeColor(b *strings.Builder, color Color, mode ColorMode, current int) int {
	if mode == Color256 {
		index := color.index256()
		if index != current {
			b.WriteString("\x1b[38;5;")
			b.WriteString(strconv.Itoa(index))
			b.WriteByte('m')
		}
		return index
	}
	packed := int(color.R)<<16 | int(color.G)<<8 | int(color.B)
	if packed != current {
		b.WriteString("\x1b[38;2;")
		b.WriteString(strconv.Itoa(int(color.R)))
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(int(color.G)))
		b.WriteByte(';')
		b.WriteString(strconv.Itoa(int(color.B)))
		b.WriteByte('m')
	}
	return packed
}

// fog darkens what is farther from the viewer.
func (c *Canvas) fog(depth float64) float64 {
	return clamp(1-0.35*(depth-(c.Distance-1)), 0.3, 1)
}

// surfaceChar picks the character of a surface cell: a stroke along the
// outline where the surface ends, and a glyph as dense as the light elsewhere.
func (c *Canvas) surfaceChar(col, row int) rune {
	here := c.cells[row*c.cols+col]
	// inside says whether a neighbor is still this surface or something in
	// front of it. Past the border of the grid counts as inside, so the border
	// draws no outline.
	inside := func(dc, dr int) float64 {
		x, y := col+dc, row+dr
		if x < 0 || x >= c.cols || y < 0 || y >= c.rows {
			return 1
		}
		n := c.cells[y*c.cols+x]
		if n.kind == empty || n.depth >= here.depth+edgeGap {
			return 0
		}
		return 1
	}
	if inside(-1, 0)+inside(1, 0)+inside(0, -1)+inside(0, 1) == 4 {
		return fillChar(here.light)
	}
	gx := inside(1, -1) + 2*inside(1, 0) + inside(1, 1) - inside(-1, -1) - 2*inside(-1, 0) - inside(-1, 1)
	gy := inside(-1, 1) + 2*inside(0, 1) + inside(1, 1) - inside(-1, -1) - 2*inside(0, -1) - inside(1, -1)
	if gx == 0 && gy == 0 {
		return fillChar(here.light)
	}
	return strokeChar(gx, gy)
}

func fillChar(light float64) rune {
	return rune(fill[min(int(light*float64(len(fill))), len(fill)-1)])
}

// strokeChar picks the stroke that follows an outline. (gx, gy) points from
// the outside to the inside of the surface, with rows growing downward, so the
// outline runs across it.
func strokeChar(gx, gy float64) rune {
	angle := math.Atan2(gy, gx)
	if angle < 0 {
		angle += math.Pi
	}
	switch {
	case angle < math.Pi/8 || angle >= 7*math.Pi/8:
		return '|'
	case angle < 3*math.Pi/8:
		return '/'
	case angle < 5*math.Pi/8:
		return '-'
	}
	return '\\'
}

func clamp(v, low, high float64) float64 {
	return math.Max(low, math.Min(high, v))
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
