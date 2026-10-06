package fhir

import (
	"encoding/base64"
	"encoding/json"

	"github.com/didate/climate-mediator/internal/dhis2"
)

// FHIRLocation represents a FHIR R4 Location with position (lat/lon).
type FHIRLocation struct {
	ResourceType string          `json:"resourceType"`
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Status       string          `json:"status"`
	Identifier   []Identifier    `json:"identifier,omitempty"`
	Position     *Position       `json:"position,omitempty"`
	Extension    []FHIRExtension `json:"extension,omitempty"`
}

// ExtLocationBoundary is the FHIR R4 core extension holding a Location's
// boundary as GeoJSON (kept so polygons are available for zonal statistics).
const ExtLocationBoundary = "http://hl7.org/fhir/StructureDefinition/location-boundary-geojson"

type Identifier struct {
	System string `json:"system"`
	Value  string `json:"value"`
}

type Position struct {
	Longitude float64 `json:"longitude"`
	Latitude  float64 `json:"latitude"`
}

// LocationID returns the HAPI resource ID for an org unit. The prefix keeps
// this mediator's Locations apart from other mediators writing to the same
// HAPI server (e.g. dhis2-sync-mediator uses the bare org unit UID), since a
// PUT replaces the whole resource. An empty prefix uses the bare UID.
func LocationID(prefix, orgUnitID string) string {
	if prefix == "" {
		return orgUnitID
	}
	return prefix + "-" + orgUnitID
}

func OrgUnitToLocation(ou dhis2.OrgUnit, identifierSystem, idPrefix string) *FHIRLocation {
	loc := &FHIRLocation{
		ResourceType: "Location",
		ID:           LocationID(idPrefix, ou.ID),
		Name:         ou.Name,
		Status:       "active",
		Identifier: []Identifier{{
			System: identifierSystem,
			Value:  ou.ID,
		}},
	}

	if ou.Geometry != nil {
		if lon, lat, ok := ou.Geometry.PointCoordinates(); ok {
			loc.Position = &Position{
				Longitude: lon,
				Latitude:  lat,
			}
		}
		if _, ok := ou.Geometry.Polygons(); ok {
			if geojson, err := json.Marshal(ou.Geometry); err == nil {
				loc.Extension = append(loc.Extension, FHIRExtension{
					URL: ExtLocationBoundary,
					ValueAttachment: &Attachment{
						ContentType: "application/geo+json",
						Data:        base64.StdEncoding.EncodeToString(geojson),
					},
				})
			}
		}
	}

	return loc
}

// LocationBoundary returns the polygon geometry stored in the Location's
// boundary extension, if any.
func LocationBoundary(loc *FHIRLocation) (*dhis2.Geometry, bool) {
	for _, ext := range loc.Extension {
		if ext.URL != ExtLocationBoundary || ext.ValueAttachment == nil {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(ext.ValueAttachment.Data)
		if err != nil {
			return nil, false
		}
		var g dhis2.Geometry
		if err := json.Unmarshal(raw, &g); err != nil {
			return nil, false
		}
		if _, ok := g.Polygons(); !ok {
			return nil, false
		}
		return &g, true
	}
	return nil, false
}

func LocationToOrgUnit(loc *FHIRLocation) dhis2.OrgUnit {
	id := loc.ID
	if len(loc.Identifier) > 0 {
		id = loc.Identifier[0].Value
	}
	ou := dhis2.OrgUnit{
		ID:   id,
		Name: loc.Name,
	}
	if loc.Position != nil {
		coords, _ := json.Marshal([]float64{loc.Position.Longitude, loc.Position.Latitude})
		ou.Geometry = &dhis2.Geometry{
			Type:        "Point",
			Coordinates: coords,
		}
	}
	return ou
}
