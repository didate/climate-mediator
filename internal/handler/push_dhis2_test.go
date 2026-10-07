package handler

import (
	"testing"

	"github.com/didate/climate-mediator/internal/fhir"
	"github.com/didate/climate-mediator/internal/mapping"
)

func TestObservationsForPeriod(t *testing.T) {
	m := &mapping.VariableMapping{DHIS2DataElement: "de1"}
	oct := fhir.ClimateValueToObservation("ou1", "entrepot-ou1", "2m_temperature", 27, "Cel", 2025, 10, m)
	nov := fhir.ClimateValueToObservation("ou1", "entrepot-ou1", "2m_temperature", 26, "Cel", 2025, 11, m)

	// What HAPI returns for November when October's period overlaps it
	got := observationsForPeriod([]fhir.FHIRObservation{*oct, *nov}, "202511")
	if len(got) != 1 || got[0].ID != nov.ID {
		t.Fatalf("kept %d observation(s), want only November", len(got))
	}
}

func TestObservationPeriodEndsOnLastDay(t *testing.T) {
	m := &mapping.VariableMapping{}
	for _, tc := range []struct {
		year, month int
		end         string
	}{
		{2025, 10, "2025-10-31"}, {2024, 2, "2024-02-29"}, {2025, 12, "2025-12-31"},
	} {
		obs := fhir.ClimateValueToObservation("ou1", "entrepot-ou1", "t", 1, "", tc.year, tc.month, m)
		if obs.EffectivePeriod.End != tc.end {
			t.Errorf("%d-%02d end = %s, want %s", tc.year, tc.month, obs.EffectivePeriod.End, tc.end)
		}
	}
}
