package main

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

	if ou.Geometry != nil && ou.Geometry.Type == "Point" && len(ou.Geometry.Coordinates) >= 2 {
		loc.Position = &Position{
			Longitude: ou.Geometry.Coordinates[0],
			Latitude:  ou.Geometry.Coordinates[1],
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
		ou.Geometry = &Geometry{
			Type:        "Point",
			Coordinates: []float64{loc.Position.Longitude, loc.Position.Latitude},
		}
	}
	return ou
}
