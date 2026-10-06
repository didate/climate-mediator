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

// PointCoordinates returns a single [lon, lat] point representing the geometry.
// For Point: the coordinates themselves.
// For Polygon/MultiPolygon: the area-weighted centroid of all polygons (holes
// subtracted), or a point guaranteed inside the shape when that centroid falls
// outside it (concave shapes, scattered islands). See RepresentativePoint.
func (g *Geometry) PointCoordinates() (lon, lat float64, ok bool) {
	switch g.Type {
	case "Point":
		var coords []float64
		if err := json.Unmarshal(g.Coordinates, &coords); err != nil || len(coords) < 2 {
			return 0, 0, false
		}
		return coords[0], coords[1], true

	case "Polygon", "MultiPolygon":
		polys, ok := g.Polygons()
		if !ok {
			return 0, 0, false
		}
		return RepresentativePoint(polys)

	default:
		return 0, 0, false
	}
}

// Polygons returns the geometry as a list of polygons, each a list of rings
// (outer ring first, then holes), each ring a list of [lon, lat] points.
func (g *Geometry) Polygons() ([][][][]float64, bool) {
	switch g.Type {
	case "Polygon":
		var rings [][][]float64
		if err := json.Unmarshal(g.Coordinates, &rings); err != nil || len(rings) == 0 {
			return nil, false
		}
		return [][][][]float64{rings}, true

	case "MultiPolygon":
		var polys [][][][]float64
		if err := json.Unmarshal(g.Coordinates, &polys); err != nil || len(polys) == 0 {
			return nil, false
		}
		return polys, true

	default:
		return nil, false
	}
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
