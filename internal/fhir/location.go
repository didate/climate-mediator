package fhir

import (
	"encoding/json"

	"github.com/didate/climate-mediator/internal/dhis2"
)

// FHIRLocation represents a FHIR R4 Location with position (lat/lon).
type FHIRLocation struct {
	ResourceType string       `json:"resourceType"`
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Status       string       `json:"status"`
	Identifier   []Identifier `json:"identifier,omitempty"`
	Position     *Position    `json:"position,omitempty"`
}

type Identifier struct {
	System string `json:"system"`
	Value  string `json:"value"`
}

type Position struct {
	Longitude float64 `json:"longitude"`
	Latitude  float64 `json:"latitude"`
}

func OrgUnitToLocation(ou dhis2.OrgUnit, identifierSystem string) *FHIRLocation {
	loc := &FHIRLocation{
		ResourceType: "Location",
		ID:           ou.ID,
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
	}

	return loc
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
