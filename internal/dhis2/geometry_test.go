package dhis2

import (
	"encoding/json"
	"math"
	"testing"
)

func geom(t *testing.T, typ string, coords any) *Geometry {
	t.Helper()
	raw, err := json.Marshal(coords)
	if err != nil {
		t.Fatal(err)
	}
	return &Geometry{Type: typ, Coordinates: raw}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// square returns a closed square ring from (x0, y0) with side s.
func square(x0, y0, s float64) [][]float64 {
	return [][]float64{{x0, y0}, {x0 + s, y0}, {x0 + s, y0 + s}, {x0, y0 + s}, {x0, y0}}
}

func TestPointCoordinatesPoint(t *testing.T) {
	lon, lat, ok := geom(t, "Point", []float64{-13.5, 9.8}).PointCoordinates()
	if !ok || lon != -13.5 || lat != 9.8 {
		t.Fatalf("got (%v, %v, %v)", lon, lat, ok)
	}
}

func TestPointCoordinatesSquare(t *testing.T) {
	lon, lat, ok := geom(t, "Polygon", [][][]float64{square(0, 0, 2)}).PointCoordinates()
	if !ok || !near(lon, 1) || !near(lat, 1) {
		t.Fatalf("got (%v, %v, %v), want (1, 1)", lon, lat, ok)
	}
}

// A densely digitized side (like a mangrove coastline) used to pull the
// vertex-mean "centroid" towards it; the area centroid must not move.
func TestPointCoordinatesIgnoresVertexDensity(t *testing.T) {
	ring := [][]float64{{0, 0}}
	for i := 1; i < 100; i++ {
		ring = append(ring, []float64{float64(i) / 50, 0}) // 99 extra vertices on the bottom edge
	}
	ring = append(ring, []float64{2, 0}, []float64{2, 2}, []float64{0, 2}, []float64{0, 0})

	lon, lat, ok := geom(t, "Polygon", [][][]float64{ring}).PointCoordinates()
	if !ok || !near(lon, 1) || !near(lat, 1) {
		t.Fatalf("got (%v, %v), want (1, 1)", lon, lat)
	}
}

// The first polygon of a MultiPolygon used to be the only one considered,
// even when it was a small island.
func TestPointCoordinatesMultiPolygonIslandFirst(t *testing.T) {
	island := [][][]float64{square(-1, 5, 0.1)}
	mainland := [][][]float64{square(0, 0, 4)}

	lon, lat, ok := geom(t, "MultiPolygon", [][][][]float64{island, mainland}).PointCoordinates()
	if !ok {
		t.Fatal("not ok")
	}
	if !pointInMultiPolygon(lon, lat, [][][][]float64{mainland}) {
		t.Fatalf("point (%v, %v) is not on the mainland", lon, lat)
	}
}

// A C-shaped polygon has its area centroid in the gap; the point must stay inside.
func TestPointCoordinatesConcaveStaysInside(t *testing.T) {
	c := [][]float64{{0, 0}, {3, 0}, {3, 1}, {1, 1}, {1, 2}, {3, 2}, {3, 3}, {0, 3}, {0, 0}}
	polys := [][][][]float64{{c}}

	area, cx, cy := polygonAreaCentroid(polys[0])
	if area <= 0 || pointInMultiPolygon(cx, cy, polys) {
		t.Fatalf("test shape should have its centroid outside, got (%v, %v)", cx, cy)
	}

	lon, lat, ok := geom(t, "Polygon", [][][]float64{c}).PointCoordinates()
	if !ok || !pointInMultiPolygon(lon, lat, polys) {
		t.Fatalf("point (%v, %v) is outside the polygon", lon, lat)
	}
}

// A hole around the centre must not be chosen.
func TestPointCoordinatesHoleExcluded(t *testing.T) {
	rings := [][][]float64{square(0, 0, 4), square(1, 1, 2)}
	polys := [][][][]float64{rings}

	lon, lat, ok := geom(t, "Polygon", rings).PointCoordinates()
	if !ok || !pointInMultiPolygon(lon, lat, polys) {
		t.Fatalf("point (%v, %v) is in the hole or outside", lon, lat)
	}
}

func TestPointCoordinatesInvalid(t *testing.T) {
	for _, g := range []*Geometry{
		geom(t, "Polygon", [][][]float64{}),
		geom(t, "LineString", [][]float64{{0, 0}, {1, 1}}),
		{Type: "Polygon", Coordinates: json.RawMessage(`"bad"`)},
	} {
		if _, _, ok := g.PointCoordinates(); ok {
			t.Errorf("%s %s: expected not ok", g.Type, g.Coordinates)
		}
	}
}
