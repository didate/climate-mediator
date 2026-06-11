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
	CDSVariable           string `json:"cdsVariable"`
	CDSDataset            string `json:"cdsDataset"`
	CDSProductType        string `json:"cdsProductType"`
	DHIS2DataElement      string `json:"dhis2DataElement"`
	DHIS2CategoryOptCombo string `json:"dhis2CategoryOptionCombo"`
	Transform             string `json:"transform"`
	Description           string `json:"description,omitempty"`
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

	return &cfg, nil
}
