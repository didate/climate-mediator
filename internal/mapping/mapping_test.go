package mapping

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProjectMapping(t *testing.T) {
	cfg, err := LoadMapping("../../mapping.json")
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, m := range cfg.Mappings {
		keys[m.Key()] = true
	}
	for _, k := range []string{"2m_temperature", "2m_temperature_max", "2m_temperature_min", "total_precipitation"} {
		if !keys[k] {
			t.Errorf("mapping %q missing", k)
		}
	}
}

func TestLoadMappingValidation(t *testing.T) {
	tests := map[string]string{
		"duplicate": `{"mappings":[{"cdsVariable":"2m_temperature"},{"cdsVariable":"2m_temperature"}]}`,
		"bad stat":  `{"mappings":[{"cdsVariable":"t","dailyStatistic":"daily_median","monthlyAggregation":"max"}]}`,
		"no agg":    `{"mappings":[{"cdsVariable":"t","dailyStatistic":"daily_maximum"}]}`,
	}
	for name, content := range tests {
		p := filepath.Join(t.TempDir(), "m.json")
		os.WriteFile(p, []byte(content), 0o644)
		if _, err := LoadMapping(p); err == nil || !strings.Contains(err.Error(), "mapping") {
			t.Errorf("%s: expected validation error, got %v", name, err)
		}
	}
}
