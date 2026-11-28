package cli

import (
	"context"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/MatheusGustav/mister/internal/canvas"
)

const (
	demoFrame = time.Second / 30
	demoTitle = "m i s t e r"
	demoHint  = "qualquer tecla sai"

	// The terminal shows the scene on its other screen, without the cursor, and
	// gets both back at the end.
	demoEnter = "\x1b[?1049h\x1b[?25l"
	demoLeave = "\x1b[?25h\x1b[?1049l"
)

func newDemoCommand() *cobra.Command {
	return &cobra.Command{
		Use:    "demo",
		Short:  "Mostra uma cena animada desenhada com caracteres",
		Hidden: true,
		Args:   noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, inOK := cmd.InOrStdin().(*os.File)
			out, outOK := cmd.OutOrStdout().(*os.File)
			if !inOK || !outOK || !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(out.Fd())) {
				return errors.New("o demo só roda direto no terminal")
			}
			return runDemo(cmd.Context(), in, out)
		},
	}
}

func runDemo(ctx context.Context, in, out *os.File) error {
	state, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return err
	}
	defer term.Restore(int(in.Fd()), state)
	if _, err := io.WriteString(out, demoEnter); err != nil {
		return err
	}
	defer io.WriteString(out, demoLeave)

	// In raw mode Ctrl+C arrives as a key, like any other.
	key := make(chan struct{})
	go func() {
		var b [1]byte
		_, _ = in.Read(b[:])
		close(key)
	}()

	mode := demoColors(out)
	var scene *demoScene
	start := time.Now()
	ticker := time.NewTicker(demoFrame)
	defer ticker.Stop()
	for {
		cols, rows, err := term.GetSize(int(out.Fd()))
		if err != nil {
			return err
		}
		if scene == nil || !scene.fits(cols, rows) {
			scene = newDemoScene(cols, rows)
		}
		if _, err := io.WriteString(out, scene.frame(time.Since(start).Seconds(), mode)); err != nil {
			return err
		}
		select {
		case <-ticker.C:
		case <-key:
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

func demoColors(out *os.File) canvas.ColorMode {
	if !styled(out) {
		return canvas.NoColor
	}
	if mode := os.Getenv("COLORTERM"); mode == "truecolor" || mode == "24bit" {
		return canvas.TrueColor
	}
	return canvas.Color256
}

type knotPoint struct {
	pos, normal canvas.Vec3
	color       canvas.Color
}

type speck struct {
	pos   canvas.Vec3
	speed float64
	ch    rune
}

// demoScene is a tube tied in a knot, turning in front of specks that drift up.
type demoScene struct {
	canvas *canvas.Canvas
	knot   []knotPoint
	specks []speck
}

var (
	demoKnotColors = []canvas.Color{{R: 255, G: 170, B: 60}, {R: 255, G: 90, B: 90}, {R: 150, G: 110, B: 255}}
	demoSpeckColor = canvas.Color{R: 170, G: 200, B: 240}
	// demoLift raises the knot to leave the last rows to the title.
	demoLift       = canvas.Vec3{Y: 0.12}
	demoTitleColor = canvas.Color{R: 235, G: 235, B: 235}
	demoHintColor  = canvas.Color{R: 120, G: 120, B: 120}
)

func newDemoScene(cols, rows int) *demoScene {
	s := &demoScene{canvas: canvas.New(cols, rows), knot: demoKnot(cols, rows)}
	random := rand.New(rand.NewPCG(7, 11))
	for range min(cols*rows/60, 70) {
		s.specks = append(s.specks, speck{
			pos:   canvas.Vec3{X: random.Float64()*4.4 - 2.2, Y: random.Float64()*2.6 - 1.3, Z: 0.8 + random.Float64()*1.2},
			speed: 0.02 + 0.05*random.Float64(),
			ch:    []rune("...+*")[random.IntN(5)],
		})
	}
	return s
}

func (s *demoScene) fits(cols, rows int) bool {
	c, r := s.canvas.Size()
	return c == cols && r == rows
}

// frame draws the scene as it is t seconds after the start.
func (s *demoScene) frame(t float64, mode canvas.ColorMode) string {
	c := s.canvas
	c.Clear()

	turn := canvas.NewTurn(t*0.5, 0.4+0.35*math.Sin(t*0.3))
	for _, p := range s.knot {
		normal := turn.Apply(p.normal)
		// What faces away from the viewer is hidden by the tube itself.
		if normal.Z > 0.3 {
			continue
		}
		c.Surface(turn.Apply(p.pos).Add(demoLift), normal, p.color)
	}
	for _, k := range s.specks {
		pos := k.pos
		pos.Y = math.Mod(pos.Y+k.speed*t+1.3, 2.6) - 1.3
		c.Dot(pos, k.ch, demoSpeckColor)
	}

	cols, rows := c.Size()
	// The title scrambles in for a moment every few seconds.
	settle := math.Mod(t, 6) / 1.2
	c.Text((cols-len(demoTitle))/2, rows-3, canvas.Scramble(demoTitle, settle, int(t*20)), demoTitleColor)
	c.Text((cols-len(demoHint))/2, rows-2, demoHint, demoHintColor)
	return c.Frame(mode)
}

// demoKnot samples the surface of a tube tied in a trefoil knot, with points
// close enough to leave no hole on a grid of this size.
func demoKnot(cols, rows int) []knotPoint {
	const (
		scale  = 1 / 4.8
		radius = 0.62 * scale
	)
	unit := math.Min(float64(rows)/2, float64(cols)/4)
	along := min(max(int(unit*60), 200), 6000)
	around := min(max(int(unit*4), 12), 240)

	center := func(u float64) canvas.Vec3 {
		ring := 2 + math.Cos(3*u)
		return canvas.Vec3{X: ring * math.Cos(2*u), Y: ring * math.Sin(2*u), Z: math.Sin(3 * u)}.Scale(scale)
	}
	points := make([]knotPoint, 0, along*around)
	for i := range along {
		u := 2 * math.Pi * float64(i) / float64(along)
		tangent := center(u + 1e-4).Sub(center(u - 1e-4)).Normalize()
		outward := canvas.Vec3{X: math.Cos(2 * u), Y: math.Sin(2 * u)}
		side := outward.Sub(tangent.Scale(outward.Dot(tangent))).Normalize()
		up := tangent.Cross(side)
		color := demoGradient(float64(i) / float64(along))
		for j := range around {
			sin, cos := math.Sincos(2 * math.Pi * float64(j) / float64(around))
			normal := side.Scale(cos).Add(up.Scale(sin))
			points = append(points, knotPoint{pos: center(u).Add(normal.Scale(radius)), normal: normal, color: color})
		}
	}
	return points
}

// demoGradient walks around the colors of the knot as at goes from 0 to 1.
func demoGradient(at float64) canvas.Color {
	n := len(demoKnotColors)
	pos := at * float64(n)
	i := int(pos) % n
	a, b, f := demoKnotColors[i], demoKnotColors[(i+1)%n], pos-math.Floor(pos)
	mixed := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*f)) }
	return canvas.Color{R: mixed(a.R, b.R), G: mixed(a.G, b.G), B: mixed(a.B, b.B)}
}
