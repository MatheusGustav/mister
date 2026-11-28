package canvas

import "math"

// Vec3 is a point or a direction of the scene. X grows to the right, Y grows
// up and Z grows away from the viewer.
type Vec3 struct{ X, Y, Z float64 }

func (v Vec3) Add(o Vec3) Vec3      { return Vec3{v.X + o.X, v.Y + o.Y, v.Z + o.Z} }
func (v Vec3) Sub(o Vec3) Vec3      { return Vec3{v.X - o.X, v.Y - o.Y, v.Z - o.Z} }
func (v Vec3) Scale(f float64) Vec3 { return Vec3{v.X * f, v.Y * f, v.Z * f} }
func (v Vec3) Dot(o Vec3) float64   { return v.X*o.X + v.Y*o.Y + v.Z*o.Z }

func (v Vec3) Cross(o Vec3) Vec3 {
	return Vec3{v.Y*o.Z - v.Z*o.Y, v.Z*o.X - v.X*o.Z, v.X*o.Y - v.Y*o.X}
}

// Normalize returns the direction of v with length 1. The zero vector stays zero.
func (v Vec3) Normalize() Vec3 {
	length := math.Sqrt(v.Dot(v))
	if length == 0 {
		return v
	}
	return v.Scale(1 / length)
}

// Turn is a rotation of the scene: around the Y axis, then around the X axis.
type Turn struct{ sinY, cosY, sinX, cosX float64 }

// NewTurn returns the rotation by these angles, in radians.
func NewTurn(aroundY, aroundX float64) Turn {
	var t Turn
	t.sinY, t.cosY = math.Sincos(aroundY)
	t.sinX, t.cosX = math.Sincos(aroundX)
	return t
}

// Apply returns v rotated.
func (t Turn) Apply(v Vec3) Vec3 {
	x, z := v.X*t.cosY+v.Z*t.sinY, -v.X*t.sinY+v.Z*t.cosY
	return Vec3{x, v.Y*t.cosX - z*t.sinX, v.Y*t.sinX + z*t.cosX}
}
