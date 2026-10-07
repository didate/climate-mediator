package handler

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/didate/climate-mediator/internal/cds"
	"github.com/didate/climate-mediator/internal/mapping"
	"github.com/didate/climate-mediator/internal/period"
)

type fakeFetcher struct {
	mu          sync.Mutex
	inFlight    int
	maxInFlight int
	calls       []string // "monthly:<variable>" or "daily:<variable>:<statistic>:<aggregation>"
}

func (f *fakeFetcher) track(call string) func() {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
	f.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	return func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}
}

func (f *fakeFetcher) FetchMonthlyData(dataset, variable, productType string, year, month int) (*cds.CDSGridData, error) {
	defer f.track("monthly:" + variable)()
	if variable == "broken" {
		return nil, errors.New("CDS job failed")
	}
	return &cds.CDSGridData{Variable: variable, Year: year, Month: month}, nil
}

func (f *fakeFetcher) FetchDailyStatistics(dataset, variable, statistic, agg string, year, month int) (*cds.CDSGridData, error) {
	defer f.track("daily:" + variable + ":" + statistic + ":" + agg)()
	return &cds.CDSGridData{Variable: variable, Year: year, Month: month}, nil
}

func TestFetchAllGrids(t *testing.T) {
	mappings := []mapping.VariableMapping{
		{CDSVariable: "2m_temperature"},
		{Name: "2m_temperature_max", CDSVariable: "2m_temperature", DailyStatistic: "daily_maximum", MonthlyAggregation: "max"},
		{CDSVariable: "broken"},
	}
	periods := []period.YearMonth{{Year: 2026, Month: 7}, {Year: 2026, Month: 8}}
	f := &fakeFetcher{}

	results := fetchAllGrids(f, mappings, periods, 2)

	if len(results) != 6 {
		t.Fatalf("got %d results, want 6", len(results))
	}
	if f.maxInFlight > 2 {
		t.Errorf("%d requests in flight, limit is 2", f.maxInFlight)
	}
	if f.maxInFlight < 2 {
		t.Errorf("requests were not run in parallel (max in flight %d)", f.maxInFlight)
	}

	// The CDS request uses the CDS variable, never the mapping name
	for _, c := range f.calls {
		if c != "monthly:2m_temperature" && c != "daily:2m_temperature:daily_maximum:max" && c != "monthly:broken" {
			t.Errorf("unexpected CDS call %q", c)
		}
	}

	r := results[gridKey{period: periods[1], key: "2m_temperature_max"}]
	if r.err != nil || r.grid == nil || r.grid.Month != 8 {
		t.Errorf("Tmax August = %+v", r)
	}
	// A failed variable does not prevent the others
	if r := results[gridKey{period: periods[0], key: "broken"}]; r.err == nil {
		t.Error("expected an error for the broken variable")
	}
}
