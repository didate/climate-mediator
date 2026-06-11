package dhis2

import "encoding/json"

type OrgUnit struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Geometry *Geometry `json:"geometry,omitempty"`
}

type Geometry struct {
	Type        string          `json:"type"`
	Coordinates json.RawMessage `json:"coordinates"`
}

// PointCoordinates extracts [lon, lat] from any geometry type.
// For Point: returns the coordinates directly.
// For Polygon/MultiPolygon: computes the centroid of the outer ring.
func (g *Geometry) PointCoordinates() (lon, lat float64, ok bool) {
	switch g.Type {
	case "Point":
		var coords []float64
		if err := json.Unmarshal(g.Coordinates, &coords); err != nil || len(coords) < 2 {
			return 0, 0, false
		}
		return coords[0], coords[1], true

	case "Polygon":
		// [[[lon,lat], [lon,lat], ...]]
		var rings [][][]float64
		if err := json.Unmarshal(g.Coordinates, &rings); err != nil || len(rings) == 0 {
			return 0, 0, false
		}
		return centroid(rings[0])

	case "MultiPolygon":
		// [[[[lon,lat], [lon,lat], ...]]]
		var polys [][][][]float64
		if err := json.Unmarshal(g.Coordinates, &polys); err != nil || len(polys) == 0 || len(polys[0]) == 0 {
			return 0, 0, false
		}
		return centroid(polys[0][0])

	default:
		return 0, 0, false
	}
}

// centroid computes the centroid of a polygon ring.
func centroid(ring [][]float64) (lon, lat float64, ok bool) {
	if len(ring) == 0 {
		return 0, 0, false
	}
	var sumLon, sumLat float64
	for _, pt := range ring {
		if len(pt) < 2 {
			continue
		}
		sumLon += pt[0]
		sumLat += pt[1]
	}
	n := float64(len(ring))
	return sumLon / n, sumLat / n, true
}

type DataValueSet struct {
	DataSet    string      `json:"dataSet,omitempty"`
	Period     string      `json:"period,omitempty"`
	OrgUnit    string      `json:"orgUnit,omitempty"`
	DataValues []DataValue `json:"dataValues"`
}

type DataValue struct {
	DataElement          string `json:"dataElement"`
	Period               string `json:"period,omitempty"`
	OrgUnit              string `json:"orgUnit,omitempty"`
	CategoryOptionCombo  string `json:"categoryOptionCombo,omitempty"`
	Value                string `json:"value"`
	Comment              string `json:"comment,omitempty"`
}

type ImportCount struct {
	Imported int `json:"imported"`
	Updated  int `json:"updated"`
	Ignored  int `json:"ignored"`
	Deleted  int `json:"deleted"`
}
