package fhir

import (
	"fmt"
	"strings"

	"github.com/didate/climate-mediator/internal/dhis2"
	"github.com/didate/climate-mediator/internal/mapping"
)

// FHIRObservation represents a climate measurement at a location and time.
type FHIRObservation struct {
	ResourceType    string           `json:"resourceType"`
	ID              string           `json:"id"`
	Status          string           `json:"status"`
	Code            *CodeableConcept `json:"code"`
	Subject         *Reference       `json:"subject,omitempty"`
	EffectivePeriod *FHIRPeriod      `json:"effectivePeriod,omitempty"`
	ValueQuantity   *Quantity        `json:"valueQuantity,omitempty"`
	Extension       []FHIRExtension  `json:"extension,omitempty"`
}

type CodeableConcept struct {
	Coding []Coding `json:"coding,omitempty"`
	Text   string   `json:"text,omitempty"`
}

type Coding struct {
	System string `json:"system,omitempty"`
	Code   string `json:"code"`
}

type Reference struct {
	Reference string `json:"reference"`
}

type FHIRPeriod struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type Quantity struct {
	Value  float64 `json:"value"`
	Unit   string  `json:"unit,omitempty"`
	System string  `json:"system,omitempty"`
	Code   string  `json:"code,omitempty"`
}

type FHIRExtension struct {
	URL         string `json:"url"`
	ValueString string `json:"valueString,omitempty"`
}

const (
	ExtDHIS2Period = "https://dhis2.org/fhir/period"
	ExtDHIS2DE     = "https://dhis2.org/fhir/dataElement"
	ExtDHIS2COC    = "https://dhis2.org/fhir/categoryOptionCombo"
	CdsSystem      = "https://cds.climate.copernicus.eu/variables"
)

// ClimateValueToObservation creates a FHIR Observation for a climate value.
func ClimateValueToObservation(orgUnitID, cdsVariable string, value float64, unit string, year, month int, m *mapping.VariableMapping) *FHIRObservation {
	period := fmt.Sprintf("%d%02d", year, month)
	// FHIR resource IDs: alphanumeric + hyphens only, max 64 chars
	safeVar := strings.ReplaceAll(cdsVariable, "_", "-")
	id := fmt.Sprintf("%s-%s-%s", orgUnitID, safeVar, period)

	start := fmt.Sprintf("%d-%02d-01", year, month)
	// End of month
	endYear, endMonth := year, month+1
	if endMonth > 12 {
		endMonth = 1
		endYear++
	}
	end := fmt.Sprintf("%d-%02d-01", endYear, endMonth)

	return &FHIRObservation{
		ResourceType: "Observation",
		ID:           id,
		Status:       "final",
		Code: &CodeableConcept{
			Coding: []Coding{{
				System: CdsSystem,
				Code:   cdsVariable,
			}},
			Text: cdsVariable,
		},
		Subject: &Reference{Reference: "Location/" + orgUnitID},
		EffectivePeriod: &FHIRPeriod{
			Start: start,
			End:   end,
		},
		ValueQuantity: &Quantity{
			Value: value,
			Unit:  unit,
		},
		Extension: []FHIRExtension{
			{URL: ExtDHIS2Period, ValueString: period},
			{URL: ExtDHIS2DE, ValueString: m.DHIS2DataElement},
			{URL: ExtDHIS2COC, ValueString: m.DHIS2CategoryOptCombo},
		},
	}
}

// ObservationToDataValue converts a FHIR Observation back to a DHIS2 DataValue.
func ObservationToDataValue(obs *FHIRObservation) *dhis2.DataValue {
	dv := &dhis2.DataValue{}

	// Extract DHIS2 metadata from extensions
	for _, ext := range obs.Extension {
		switch ext.URL {
		case ExtDHIS2Period:
			dv.Period = ext.ValueString
		case ExtDHIS2DE:
			dv.DataElement = ext.ValueString
		case ExtDHIS2COC:
			dv.CategoryOptionCombo = ext.ValueString
		}
	}

	// Extract org unit from subject reference
	if obs.Subject != nil {
		parts := splitLast(obs.Subject.Reference, "/")
		dv.OrgUnit = parts
	}

	// Extract value
	if obs.ValueQuantity != nil {
		dv.Value = fmt.Sprintf("%.2f", obs.ValueQuantity.Value)
	}

	// Add import comment with source info
	varName := ""
	if obs.Code != nil && len(obs.Code.Coding) > 0 {
		varName = obs.Code.Coding[0].Code
	}
	dv.Comment = fmt.Sprintf("ERA5-Land %s - Climate Mediator", varName)

	return dv
}

func splitLast(s, sep string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if string(s[i]) == sep {
			return s[i+1:]
		}
	}
	return s
}
