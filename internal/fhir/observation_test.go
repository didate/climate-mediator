package fhir

import (
	"testing"

	"github.com/didate/climate-mediator/internal/mapping"
)

func TestObservationToDataValueOrgUnit(t *testing.T) {
	m := &mapping.VariableMapping{DHIS2DataElement: "de1"}

	// New observations: subject is the prefixed Location, org unit comes from the extension
	obs := ClimateValueToObservation("ou1", LocationID("entrepot", "ou1"), "total_precipitation", 760.43, "mm", 2026, 8, m)
	if obs.Subject.Reference != "Location/entrepot-ou1" {
		t.Errorf("subject = %q, want Location/entrepot-ou1", obs.Subject.Reference)
	}
	if obs.ID != "ou1-total-precipitation-202608" {
		t.Errorf("ID = %q, must not change (reprocessing overwrites by ID)", obs.ID)
	}
	dv := ObservationToDataValue(obs)
	if dv.OrgUnit != "ou1" || dv.DataElement != "de1" || dv.Period != "202608" || dv.Value != "760.43" {
		t.Errorf("data value = %+v", dv)
	}

	// Observations written before the extension existed: subject is Location/<uid>
	legacy := &FHIRObservation{Subject: &Reference{Reference: "Location/ou2"}}
	if got := ObservationToDataValue(legacy).OrgUnit; got != "ou2" {
		t.Errorf("legacy org unit = %q, want ou2", got)
	}
}
