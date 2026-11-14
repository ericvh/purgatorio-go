// Package memdraw is the in-memory graphics library behind the draw
// device: images in Inferno's channel formats, Porter-Duff compositing,
// lines, ellipses, arcs, polygons, subfont strings, and layers (windows
// on screens). It follows Inferno's libmemdraw and libmemlayer so that
// pixel results match Inferno's.
//
// The package is portable: it imports nothing from the host.
package memdraw

import "fmt"

// Point is a point in the plane; y grows downward.
type Point struct{ X, Y int }

// Rectangle is half open: it contains Min but not Max.
type Rectangle struct{ Min, Max Point }

// ZP is the zero point.
var ZP Point

// Pt returns the point (x, y).
func Pt(x, y int) Point { return Point{x, y} }

// Rect returns the rectangle with corners (x0, y0) and (x1, y1).
func Rect(x0, y0, x1, y1 int) Rectangle { return Rectangle{Point{x0, y0}, Point{x1, y1}} }

// Rpt returns the rectangle with corners min and max.
func Rpt(min, max Point) Rectangle { return Rectangle{min, max} }

func (p Point) Add(q Point) Point { return Point{p.X + q.X, p.Y + q.Y} }
func (p Point) Sub(q Point) Point { return Point{p.X - q.X, p.Y - q.Y} }
func (p Point) Mul(k int) Point   { return Point{p.X * k, p.Y * k} }
func (p Point) Div(k int) Point   { return Point{p.X / k, p.Y / k} }
func (p Point) Eq(q Point) bool   { return p == q }
func (p Point) String() string    { return fmt.Sprintf("[%d %d]", p.X, p.Y) }
func (p Point) In(r Rectangle) bool {
	return p.X >= r.Min.X && p.X < r.Max.X && p.Y >= r.Min.Y && p.Y < r.Max.Y
}

func (r Rectangle) Dx() int                   { return r.Max.X - r.Min.X }
func (r Rectangle) Dy() int                   { return r.Max.Y - r.Min.Y }
func (r Rectangle) Add(p Point) Rectangle     { return Rectangle{r.Min.Add(p), r.Max.Add(p)} }
func (r Rectangle) Sub(p Point) Rectangle     { return Rectangle{r.Min.Sub(p), r.Max.Sub(p)} }
func (r Rectangle) Inset(n int) Rectangle     { return Rect(r.Min.X+n, r.Min.Y+n, r.Max.X-n, r.Max.Y-n) }
func (r Rectangle) Eq(s Rectangle) bool       { return r == s }
func (r Rectangle) Empty() bool               { return r.Min.X >= r.Max.X || r.Min.Y >= r.Max.Y }
func (r Rectangle) String() string            { return r.Min.String() + " " + r.Max.String() }
func (r Rectangle) Overlaps(s Rectangle) bool { return RectXRect(r, s) }

// In reports whether r lies within s (rectinrect).
func (r Rectangle) In(s Rectangle) bool {
	return s.Min.X <= r.Min.X && r.Max.X <= s.Max.X && s.Min.Y <= r.Min.Y && r.Max.Y <= s.Max.Y
}

// RectXRect reports whether r and s overlap.
func RectXRect(r, s Rectangle) bool {
	return r.Min.X < s.Max.X && s.Min.X < r.Max.X && r.Min.Y < s.Max.Y && s.Min.Y < r.Max.Y
}

// RectClip clips *r to b and reports whether they overlapped. When they
// do not, *r is unchanged.
func RectClip(r *Rectangle, b Rectangle) bool {
	if !RectXRect(*r, b) {
		return false
	}
	if r.Min.X < b.Min.X {
		r.Min.X = b.Min.X
	}
	if r.Min.Y < b.Min.Y {
		r.Min.Y = b.Min.Y
	}
	if r.Max.X > b.Max.X {
		r.Max.X = b.Max.X
	}
	if r.Max.Y > b.Max.Y {
		r.Max.Y = b.Max.Y
	}
	return true
}

// Canon returns r with min and max ordered.
func (r Rectangle) Canon() Rectangle {
	if r.Max.X < r.Min.X {
		r.Min.X, r.Max.X = r.Max.X, r.Min.X
	}
	if r.Max.Y < r.Min.Y {
		r.Min.Y, r.Max.Y = r.Max.Y, r.Min.Y
	}
	return r
}

// CombineRect grows *r to cover s.
func CombineRect(r *Rectangle, s Rectangle) {
	if r.Min.X > s.Min.X {
		r.Min.X = s.Min.X
	}
	if r.Min.Y > s.Min.Y {
		r.Min.Y = s.Min.Y
	}
	if r.Max.X < s.Max.X {
		r.Max.X = s.Max.X
	}
	if r.Max.Y < s.Max.Y {
		r.Max.Y = s.Max.Y
	}
}

// ReplXY maps x into [min, max) by replication.
func ReplXY(min, max, x int) int {
	sx := (x - min) % (max - min)
	if sx < 0 {
		sx += max - min
	}
	return sx + min
}

// Repl maps p into r by replication.
func Repl(r Rectangle, p Point) Point {
	return Point{ReplXY(r.Min.X, r.Max.X, p.X), ReplXY(r.Min.Y, r.Max.Y, p.Y)}
}
