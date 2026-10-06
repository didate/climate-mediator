package fhir

import (
	"encoding/json"
	"testing"

	"github.com/didate/climate-mediator/internal/dhis2"
)

func TestOrgUnitToLocationStoresBoundary(t *testing.T) {
	coords := json.RawMessage(`[[[0,0],[2,0],[2,2],[0,2],[0,0]]]`)
	ou := dhis2.OrgUnit{ID: "ou1", Name: "DPS Test", Geometry: &dhis2.Geometry{Type: "Polygon", Coordinates: coords}}

	loc := OrgUnitToLocation(ou, "urn:test", "entrepot")

	if loc.ID != "entrepot-ou1" {
		t.Errorf("ID = %q, want entrepot-ou1", loc.ID)
	}
	if loc.Position == nil || loc.Position.Longitude != 1 || loc.Position.Latitude != 1 {
		t.Fatalf("position = %+v, want (1, 1)", loc.Position)
	}

	// Survives a JSON round trip, as when stored in and read back from HAPI
	raw, _ := json.Marshal(loc)
	var back FHIRLocation
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	g, ok := LocationBoundary(&back)
	if !ok || g.Type != "Polygon" || string(g.Coordinates) != string(coords) {
		t.Fatalf("boundary = %+v ok=%v", g, ok)
	}
}

func TestOrgUnitToLocationPointHasNoBoundary(t *testing.T) {
	ou := dhis2.OrgUnit{ID: "ou2", Geometry: &dhis2.Geometry{Type: "Point", Coordinates: json.RawMessage(`[-13.5,9.8]`)}}
	loc := OrgUnitToLocation(ou, "urn:test", "entrepot")
	if len(loc.Extension) != 0 {
		t.Fatalf("unexpected extensions: %+v", loc.Extension)
	}
	if _, ok := LocationBoundary(loc); ok {
		t.Fatal("point should have no boundary")
	}
}
