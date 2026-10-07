package mapping

import (
	"encoding/json"
	"fmt"
	"os"
)

type MappingConfig struct {
	Mappings []VariableMapping `json:"mappings"`
}

type VariableMapping struct {
	// Name identifies the mapping (Observation code and ID, logs). Defaults to
	// CDSVariable; set it when several mappings use the same CDS variable.
	Name           string `json:"name,omitempty"`
	CDSVariable    string `json:"cdsVariable"`
	CDSDataset     string `json:"cdsDataset"`
	CDSProductType string `json:"cdsProductType,omitempty"`
	// DailyStatistic requests a daily statistics dataset
	// (derived-era5-land-daily-statistics): daily_mean, daily_maximum or daily_minimum.
	DailyStatistic string `json:"dailyStatistic,omitempty"`
	// MonthlyAggregation reduces the daily values to the month: max, min or mean.
	MonthlyAggregation    string `json:"monthlyAggregation,omitempty"`
	DHIS2DataElement      string `json:"dhis2DataElement"`
	DHIS2CategoryOptCombo string `json:"dhis2CategoryOptionCombo"`
	Transform             string `json:"transform"`
	Description           string `json:"description,omitempty"`
}

// Key returns the mapping's identifier: Name, or CDSVariable when Name is empty.
func (m VariableMapping) Key() string {
	if m.Name != "" {
		return m.Name
	}
	return m.CDSVariable
}

// ComputedMapping represents a derived variable computed from other CDS variables.
type ComputedMapping struct {
	Name                  string `json:"name"`
	DHIS2DataElement      string `json:"dhis2DataElement"`
	DHIS2CategoryOptCombo string `json:"dhis2CategoryOptionCombo"`
	Compute               string `json:"compute"`
	Description           string `json:"description,omitempty"`
}

type MappingConfigFull struct {
	Mappings []VariableMapping `json:"mappings"`
	Computed []ComputedMapping `json:"computed,omitempty"`
}

func LoadMapping(path string) (*MappingConfigFull, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read mapping file: %w", err)
	}

	var cfg MappingConfigFull
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse mapping file: %w", err)
	}

	if len(cfg.Mappings) == 0 {
		return nil, fmt.Errorf("mapping file has no mappings")
	}

	seen := make(map[string]bool)
	for _, m := range cfg.Mappings {
		if seen[m.Key()] {
			return nil, fmt.Errorf("duplicate mapping %q: set a distinct \"name\" when several mappings use the same cdsVariable", m.Key())
		}
		seen[m.Key()] = true

		if m.DailyStatistic != "" {
			switch m.DailyStatistic {
			case "daily_mean", "daily_maximum", "daily_minimum":
			default:
				return nil, fmt.Errorf("mapping %q: unknown dailyStatistic %q", m.Key(), m.DailyStatistic)
			}
			switch m.MonthlyAggregation {
			case "max", "min", "mean":
			default:
				return nil, fmt.Errorf("mapping %q: monthlyAggregation must be max, min or mean, got %q", m.Key(), m.MonthlyAggregation)
			}
		}
	}

	return &cfg, nil
}
