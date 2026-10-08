package cds

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTransformValue(t *testing.T) {
	tests := []struct {
		name      string
		value     float64
		transform string
		year      int
		month     int
		want      float64
		wantUnit  string
	}{
		{"precip January (31 days)", 0.001, "m_per_day_to_mm_month", 2026, 1, 31, "mm"},
		{"precip April (30 days)", 0.001, "m_per_day_to_mm_month", 2026, 4, 30, "mm"},
		{"precip February leap year 2024 (29 days)", 0.001, "m_per_day_to_mm_month", 2024, 2, 29, "mm"},
		{"precip February 2026 (28 days)", 0.001, "m_per_day_to_mm_month", 2026, 2, 28, "mm"},
		{"precip December (31 days, year rollover)", 0.001, "m_per_day_to_mm_month", 2025, 12, 31, "mm"},
		{"m_to_mm unchanged", 0.001, "m_to_mm", 2026, 2, 1, "mm"},
		{"kelvin_to_celsius unchanged", 300, "kelvin_to_celsius", 2026, 2, 26.85, "Cel"},
		{"unknown transform passes through", 42, "", 2026, 2, 42, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, unit := TransformValue(tt.value, tt.transform, tt.year, tt.month)
			if math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("value = %v, want %v", got, tt.want)
			}
			if unit != tt.wantUnit {
				t.Errorf("unit = %q, want %q", unit, tt.wantUnit)
			}
		})
	}
}

func TestSampleNearestFallback(t *testing.T) {
	nan := math.NaN()
	// ERA5 latitudes are descending; the centre cell is sea (NaN)
	grid := &CDSGridData{
		Lats: []float64{10.2, 10.1, 10.0},
		Lons: []float64{-14.2, -14.1, -14.0},
		Values: [][]float64{
			{nan, nan, nan},
			{nan, nan, 7},
			{nan, 5, nan},
		},
	}

	s, ok := grid.SampleNearest(10.1, -14.1)
	if !ok || !s.Fallback {
		t.Fatalf("expected fallback sample, got ok=%v %+v", ok, s)
	}
	if s.NearestLat != 10.1 || s.NearestLon != -14.1 {
		t.Errorf("nearest cell = (%v, %v), want (10.1, -14.1)", s.NearestLat, s.NearestLon)
	}
	// Both neighbours are 1 cell away; the scan order keeps the first found (lower lat index)
	if s.CellLat != 10.1 || s.CellLon != -14.0 || s.Value != 7 {
		t.Errorf("used cell = (%v, %v) value %v, want (10.1, -14.0) value 7", s.CellLat, s.CellLon, s.Value)
	}

	v, ok := grid.ExtractValueForCoordinate(10.0, -14.1)
	if !ok || v != 5 {
		t.Errorf("land cell: got %v ok=%v, want 5", v, ok)
	}
}

func TestDailyStatisticsInputs(t *testing.T) {
	in := dailyStatisticsInputs("2m_temperature", "daily_maximum", 2024, 2)
	days := in["day"].([]string)
	if len(days) != 29 || days[0] != "01" || days[28] != "29" {
		t.Errorf("days = %v, want 01..29 for February 2024", days)
	}
	if in["daily_statistic"] != "daily_maximum" || in["month"] != "02" || in["year"] != "2024" || in["time_zone"] != "utc+00:00" {
		t.Errorf("inputs = %+v", in)
	}
	if _, ok := in["product_type"]; ok {
		t.Error("daily statistics requests take no product_type")
	}
}

func TestReduceSteps(t *testing.T) {
	nan := math.NaN()
	// 3 days x 3 cells; the last cell is sea (NaN every day)
	steps := [][]float64{
		{300, 290, nan},
		{305, 288, nan},
		{302, 292, nan},
	}
	for how, want := range map[string][]float64{
		"max":  {305, 292},
		"min":  {300, 288},
		"mean": {302.3333333333333, 290},
	} {
		got, err := reduceSteps(steps, how)
		if err != nil {
			t.Fatalf("%s: %v", how, err)
		}
		if math.Abs(got[0]-want[0]) > 1e-9 || math.Abs(got[1]-want[1]) > 1e-9 || !math.IsNaN(got[2]) {
			t.Errorf("%s = %v, want %v and NaN", how, got, want)
		}
	}
	if _, err := reduceSteps(steps, "sum"); err == nil {
		t.Error("expected error for unknown aggregation")
	}
}

func TestToFloat64Steps(t *testing.T) {
	steps, err := toFloat64Steps([][][]float32{{{1, 2}, {3, 4}}, {{5, 6}, {7, 8}}})
	if err != nil || len(steps) != 2 || steps[1][3] != 8 || len(steps[0]) != 4 {
		t.Fatalf("3D: %v %v", steps, err)
	}
	// Monthly means without a time dimension: one step
	steps, err = toFloat64Steps([][]float32{{1, 2}, {3, 4}})
	if err != nil || len(steps) != 1 || len(steps[0]) != 4 {
		t.Fatalf("2D: %v %v", steps, err)
	}
}

func TestFetchSubmitErrorClassification(t *testing.T) {
	for code, permanent := range map[int]bool{400: true, 403: true, 502: false, 503: false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		c := NewCDSClient(srv.URL, "key")
		_, err := c.FetchMonthlyData("reanalysis-era5-land-monthly-means", "2m_temperature", "monthly_averaged_reanalysis", 2023, 8)
		srv.Close()
		if err == nil {
			t.Fatalf("%d: expected an error", code)
		}
		if errors.Is(err, ErrPermanent) != permanent {
			t.Errorf("%d: permanent = %v, want %v (%v)", code, !permanent, permanent, err)
		}
	}
}

// fakeCDS answers submit with a job, then the job status, and the results
// problem document with traceback when the job is rejected.
func fakeCDS(t *testing.T, status, traceback string) (*httptest.Server, *[]string) {
	var deleted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST":
			w.Write([]byte(`{"jobID":"job1","status":"accepted"}`))
		case r.Method == "DELETE":
			deleted = append(deleted, r.URL.Path)
		case strings.HasSuffix(r.URL.Path, "/results"):
			w.WriteHeader(400)
			w.Write([]byte(`{"title":"The job has been rejected","traceback":"` + traceback + `"}`))
		default:
			w.Write([]byte(`{"jobID":"job1","status":"` + status + `"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &deleted
}

func TestFetchRejectedClassification(t *testing.T) {
	tests := []struct {
		traceback string
		want      error
	}{
		{"Number queued requests for this dataset is temporarily limited. Please configure your scripts accordingly ", ErrQueueLimited},
		{"Request too large", ErrPermanent},
	}
	for _, tt := range tests {
		srv, _ := fakeCDS(t, "rejected", tt.traceback)
		c := NewCDSClient(srv.URL, "key")
		c.PollInterval = time.Millisecond
		_, err := c.FetchMonthlyData("reanalysis-era5-land-monthly-means", "2m_temperature", "monthly_averaged_reanalysis", 2024, 5)
		if !errors.Is(err, tt.want) {
			t.Errorf("%q: got %v, want %v", tt.traceback, err, tt.want)
		}
		if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(tt.traceback)) {
			t.Errorf("error should include the CDS reason, got %v", err)
		}
	}
}

func TestFetchTimeoutDeletesJob(t *testing.T) {
	srv, deleted := fakeCDS(t, "accepted", "")
	c := NewCDSClient(srv.URL, "key")
	c.PollInterval = time.Millisecond
	c.JobTimeout = 20 * time.Millisecond

	_, err := c.FetchMonthlyData("reanalysis-era5-land-monthly-means", "2m_temperature", "monthly_averaged_reanalysis", 2024, 5)
	if err == nil || errors.Is(err, ErrPermanent) {
		t.Fatalf("timeout should be a transient error, got %v", err)
	}
	if len(*deleted) != 1 || !strings.HasSuffix((*deleted)[0], "/jobs/job1") {
		t.Errorf("abandoned job not deleted: %v", *deleted)
	}
}
