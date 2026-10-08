package handler

import (
	"errors"
	"fmt"
	"os"
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

func TestMain(m *testing.M) {
	fetchRetryDelays = []time.Duration{0, 0} // no waiting between retries in tests
	os.Exit(m.Run())
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

// flakyFetcher fails the first failures calls with err, then succeeds.
type flakyFetcher struct {
	failures int
	err      error
	calls    int
}

func (f *flakyFetcher) FetchMonthlyData(dataset, variable, productType string, year, month int) (*cds.CDSGridData, error) {
	f.calls++
	if f.calls <= f.failures {
		return nil, f.err
	}
	return &cds.CDSGridData{Variable: variable}, nil
}

func (f *flakyFetcher) FetchDailyStatistics(dataset, variable, statistic, agg string, year, month int) (*cds.CDSGridData, error) {
	return f.FetchMonthlyData(dataset, variable, "", year, month)
}

func TestFetchAllGridsRetries(t *testing.T) {
	m := []mapping.VariableMapping{{CDSVariable: "2m_temperature"}}
	p := []period.YearMonth{{Year: 2023, Month: 8}}
	key := gridKey{period: p[0], key: "2m_temperature"}

	// 502 from the CDS gateway twice, then success on the 3rd attempt
	f := &flakyFetcher{failures: 2, err: errors.New("CDS API returned 502")}
	if r := fetchAllGrids(f, m, p, 1)[key]; r.err != nil || f.calls != 3 {
		t.Errorf("transient: err=%v after %d calls, want success after 3", r.err, f.calls)
	}

	// Still failing after all retries
	f = &flakyFetcher{failures: 10, err: errors.New("CDS job timed out")}
	if r := fetchAllGrids(f, m, p, 1)[key]; r.err == nil || f.calls != 3 {
		t.Errorf("exhausted: err=%v after %d calls, want error after 3", r.err, f.calls)
	}

	// Permanent errors are not retried
	f = &flakyFetcher{failures: 10, err: fmt.Errorf("%w: CDS API returned 400", cds.ErrPermanent)}
	if r := fetchAllGrids(f, m, p, 1)[key]; r.err == nil || f.calls != 1 {
		t.Errorf("permanent: err=%v after %d calls, want error after 1", r.err, f.calls)
	}
}
