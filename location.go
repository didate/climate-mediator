package main

import "encoding/json"

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

func OrgUnitToLocation(ou OrgUnit, dhis2BaseURL string) *FHIRLocation {
	loc := &FHIRLocation{
		ResourceType: "Location",
		ID:           ou.ID,
		Name:         ou.Name,
		Status:       "active",
		Identifier: []Identifier{{
			System: dhis2BaseURL + "/api/organisationUnits",
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

func LocationToOrgUnit(loc *FHIRLocation) OrgUnit {
	id := loc.ID
	if len(loc.Identifier) > 0 {
		id = loc.Identifier[0].Value
	}
	ou := OrgUnit{
		ID:   id,
		Name: loc.Name,
	}
	if loc.Position != nil {
		coords, _ := json.Marshal([]float64{loc.Position.Longitude, loc.Position.Latitude})
		ou.Geometry = &Geometry{
			Type:        "Point",
			Coordinates: coords,
		}
	}
	return ou
}
