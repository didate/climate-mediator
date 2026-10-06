package dhis2

import (
	"math"
	"sort"
)

// Geometry helpers work directly on lon/lat degrees (planar approximation).
// At Guinea's latitudes (7–13°N) and prefecture scale the distortion is
// negligible for picking a representative point.

// RepresentativePoint returns a point that represents a (multi)polygon:
// the area-weighted centroid of all polygons, holes subtracted. When that
// centroid lies outside the shape, it returns a point on the surface of the
// largest polygon instead, so the point always falls on the org unit itself.
func RepresentativePoint(polys [][][][]float64) (lon, lat float64, ok bool) {
	var totalArea, sumX, sumY float64
	largest, largestArea := -1, 0.0

	for i, rings := range polys {
		area, cx, cy := polygonAreaCentroid(rings)
		if area <= 0 {
			continue
		}
		totalArea += area
		sumX += area * cx
		sumY += area * cy
		if area > largestArea {
			largest, largestArea = i, area
		}
	}

	if totalArea <= 0 {
		// Degenerate shape (no area): fall back to the mean of the first ring's vertices
		if len(polys) == 0 || len(polys[0]) == 0 {
			return 0, 0, false
		}
		return vertexMean(polys[0][0])
	}

	cx, cy := sumX/totalArea, sumY/totalArea
	if pointInMultiPolygon(cx, cy, polys) {
		return cx, cy, true
	}

	if x, y, found := pointOnSurface(polys[largest], cy); found {
		return x, y, true
	}
	return cx, cy, true
}

// polygonAreaCentroid returns the area (outer ring minus holes) and centroid of one polygon.
func polygonAreaCentroid(rings [][][]float64) (area, cx, cy float64) {
	var sumX, sumY float64
	for i, ring := range rings {
		a, x, y := ringAreaCentroid(ring)
		a = math.Abs(a)
		if i > 0 {
			a = -a // holes remove area
		}
		area += a
		sumX += a * x
		sumY += a * y
	}
	if area <= 0 {
		return 0, 0, 0
	}
	return area, sumX / area, sumY / area
}

// ringAreaCentroid uses the shoelace formula. The returned area is signed
// (sign depends on winding); the centroid does not depend on the sign.
func ringAreaCentroid(ring [][]float64) (area, cx, cy float64) {
	n := len(ring)
	if n < 3 {
		return 0, 0, 0
	}
	var a, sx, sy float64
	for i := 0; i < n; i++ {
		p, q := ring[i], ring[(i+1)%n]
		if len(p) < 2 || len(q) < 2 {
			continue
		}
		cross := p[0]*q[1] - q[0]*p[1]
		a += cross
		sx += (p[0] + q[0]) * cross
		sy += (p[1] + q[1]) * cross
	}
	if a == 0 {
		return 0, 0, 0
	}
	a /= 2
	return a, sx / (6 * a), sy / (6 * a)
}

// pointOnSurface returns the midpoint of the widest interior segment of a
// horizontal line crossing the polygon, trying the given latitude first and
// then the middle of the polygon's latitude range.
func pointOnSurface(rings [][][]float64, preferredLat float64) (lon, lat float64, ok bool) {
	if len(rings) == 0 {
		return 0, 0, false
	}
	minLat, maxLat := math.Inf(1), math.Inf(-1)
	for _, p := range rings[0] {
		if len(p) >= 2 {
			minLat = math.Min(minLat, p[1])
			maxLat = math.Max(maxLat, p[1])
		}
	}

	for _, y := range []float64{preferredLat, (minLat + maxLat) / 2} {
		var xs []float64
		for _, ring := range rings {
			n := len(ring)
			for i := 0; i < n; i++ {
				p, q := ring[i], ring[(i+1)%n]
				if len(p) < 2 || len(q) < 2 {
					continue
				}
				// Half-open rule so a vertex exactly on the line is counted once
				if (p[1] > y) != (q[1] > y) {
					xs = append(xs, p[0]+(y-p[1])*(q[0]-p[0])/(q[1]-p[1]))
				}
			}
		}
		sort.Float64s(xs)

		// Even-odd: [xs[0], xs[1]], [xs[2], xs[3]], … are inside (holes excluded)
		bestWidth := 0.0
		for i := 0; i+1 < len(xs); i += 2 {
			if w := xs[i+1] - xs[i]; w > bestWidth {
				bestWidth = w
				lon, lat, ok = (xs[i]+xs[i+1])/2, y, true
			}
		}
		if ok {
			return lon, lat, true
		}
	}
	return 0, 0, false
}

// pointInMultiPolygon reports whether (x, y) is inside any polygon (even-odd, holes excluded).
func pointInMultiPolygon(x, y float64, polys [][][][]float64) bool {
	for _, rings := range polys {
		inside := false
		for _, ring := range rings {
			n := len(ring)
			for i, j := 0, n-1; i < n; j, i = i, i+1 {
				p, q := ring[i], ring[j]
				if len(p) < 2 || len(q) < 2 {
					continue
				}
				if (p[1] > y) != (q[1] > y) && x < (q[0]-p[0])*(y-p[1])/(q[1]-p[1])+p[0] {
					inside = !inside
				}
			}
		}
		if inside {
			return true
		}
	}
	return false
}

func vertexMean(ring [][]float64) (lon, lat float64, ok bool) {
	var sumLon, sumLat float64
	n := 0
	for _, pt := range ring {
		if len(pt) < 2 {
			continue
		}
		sumLon += pt[0]
		sumLat += pt[1]
		n++
	}
	if n == 0 {
		return 0, 0, false
	}
	return sumLon / float64(n), sumLat / float64(n), true
}
