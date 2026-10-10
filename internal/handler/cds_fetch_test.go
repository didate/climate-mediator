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
	// no waiting between retries in tests
	fetchRetryDelays = []time.Duration{0, 0}
	queueRetryDelays = []time.Duration{0, 0, 0, 0, 0}
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

	results := fetchAllGrids(f, allGridJobs(mappings, periods), 2, 2, nil)

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
	if r := fetchAllGrids(f, allGridJobs(m, p), 1, 1, nil)[key]; r.err != nil || f.calls != 3 {
		t.Errorf("transient: err=%v after %d calls, want success after 3", r.err, f.calls)
	}

	// Still failing after all retries
	f = &flakyFetcher{failures: 10, err: errors.New("CDS job timed out")}
	if r := fetchAllGrids(f, allGridJobs(m, p), 1, 1, nil)[key]; r.err == nil || f.calls != 3 {
		t.Errorf("exhausted: err=%v after %d calls, want error after 3", r.err, f.calls)
	}

	// Permanent errors are not retried
	f = &flakyFetcher{failures: 10, err: fmt.Errorf("%w: CDS API returned 400", cds.ErrPermanent)}
	if r := fetchAllGrids(f, allGridJobs(m, p), 1, 1, nil)[key]; r.err == nil || f.calls != 1 {
		t.Errorf("permanent: err=%v after %d calls, want error after 1", r.err, f.calls)
	}
}

func TestFetchAllGridsQueueLimitRetries(t *testing.T) {
	m := []mapping.VariableMapping{{CDSVariable: "2m_temperature"}}
	p := []period.YearMonth{{Year: 2024, Month: 5}}
	key := gridKey{period: p[0], key: "2m_temperature"}

	// Rejected 4 times for the queue limit: more than the transient retries
	// allow, but within the queue limit retries
	f := &flakyFetcher{failures: 4, err: fmt.Errorf("%w: rejected", cds.ErrQueueLimited)}
	if r := fetchAllGrids(f, allGridJobs(m, p), 1, 1, nil)[key]; r.err != nil || f.calls != 5 {
		t.Errorf("queue limited: err=%v after %d calls, want success after 5", r.err, f.calls)
	}
}

// datasetFetcher records the peak number of in-flight requests per dataset.
type datasetFetcher struct {
	mu       sync.Mutex
	inFlight map[string]int
	peak     map[string]int
	total    int
	peakAll  int
}

func (f *datasetFetcher) run(dataset string) {
	f.mu.Lock()
	f.inFlight[dataset]++
	f.total++
	if f.inFlight[dataset] > f.peak[dataset] {
		f.peak[dataset] = f.inFlight[dataset]
	}
	if f.total > f.peakAll {
		f.peakAll = f.total
	}
	f.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	f.mu.Lock()
	f.inFlight[dataset]--
	f.total--
	f.mu.Unlock()
}

func (f *datasetFetcher) FetchMonthlyData(dataset, variable, productType string, year, month int) (*cds.CDSGridData, error) {
	f.run(dataset)
	return &cds.CDSGridData{}, nil
}

func (f *datasetFetcher) FetchDailyStatistics(dataset, variable, statistic, agg string, year, month int) (*cds.CDSGridData, error) {
	f.run(dataset)
	return &cds.CDSGridData{}, nil
}

func TestFetchAllGridsPerDatasetLimit(t *testing.T) {
	mappings := []mapping.VariableMapping{
		{CDSVariable: "2m_temperature", CDSDataset: "monthly"},
		{CDSVariable: "total_precipitation", CDSDataset: "monthly"},
		{Name: "tmax", CDSVariable: "2m_temperature", CDSDataset: "daily", DailyStatistic: "daily_maximum", MonthlyAggregation: "max"},
		{Name: "tmin", CDSVariable: "2m_temperature", CDSDataset: "daily", DailyStatistic: "daily_minimum", MonthlyAggregation: "min"},
	}
	periods := []period.YearMonth{{Year: 2024, Month: 1}, {Year: 2024, Month: 2}, {Year: 2024, Month: 3}}
	f := &datasetFetcher{inFlight: map[string]int{}, peak: map[string]int{}}

	results := fetchAllGrids(f, allGridJobs(mappings, periods), 4, 1, nil)

	if len(results) != 12 {
		t.Fatalf("got %d results, want 12", len(results))
	}
	for ds, peak := range f.peak {
		if peak != 1 {
			t.Errorf("dataset %s: %d requests in flight, limit is 1", ds, peak)
		}
	}
	// The two datasets still run side by side
	if f.peakAll != 2 {
		t.Errorf("%d requests in flight overall, want 2 (one per dataset)", f.peakAll)
	}
}

// fakeCounter returns the stored count for "code period", 0 otherwise.
type fakeCounter map[string]int

func (f fakeCounter) CountObservations(code string, year, month int) (int, error) {
	return f[fmt.Sprintf("%s %d-%02d", code, year, month)], nil
}

func TestMissingGridJobs(t *testing.T) {
	mappings := []mapping.VariableMapping{
		{CDSVariable: "2m_temperature"},
		{CDSVariable: "2m_dewpoint_temperature"},
		{CDSVariable: "total_precipitation"},
		{Name: "2m_temperature_max", CDSVariable: "2m_temperature", DailyStatistic: "daily_maximum", MonthlyAggregation: "max"},
	}
	computed := []mapping.ComputedMapping{{Name: "relative_humidity", Compute: "relative_humidity"}}
	may, jun := period.YearMonth{Year: 2024, Month: 5}, period.YearMonth{Year: 2024, Month: 6}
	const orgUnits = 3189

	counts := fakeCounter{
		// May: all complete except Tmax
		"2m_temperature 2024-05": orgUnits, "2m_dewpoint_temperature 2024-05": orgUnits,
		"total_precipitation 2024-05": orgUnits, "relative_humidity 2024-05": orgUnits,
		// June: everything complete except relative humidity, and precipitation
		// counted twice (must be re-fetched, not trusted)
		"2m_temperature 2024-06": orgUnits, "2m_dewpoint_temperature 2024-06": orgUnits,
		"total_precipitation 2024-06": 2 * orgUnits, "2m_temperature_max 2024-06": orgUnits,
	}

	jobs, skip := missingGridJobs(counts, mappings, computed, []period.YearMonth{may, jun}, orgUnits)

	got := map[string]bool{}
	for _, j := range jobs {
		got[fmt.Sprintf("%s %s", j.m.Key(), j.p)] = true
	}
	want := map[string]bool{
		"2m_temperature_max 2024-05": true,
		// relative humidity missing in June: its inputs are fetched again
		"2m_temperature 2024-06": true, "2m_dewpoint_temperature 2024-06": true,
		"total_precipitation 2024-06": true,
	}
	if len(got) != len(want) {
		t.Errorf("jobs = %v, want %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("missing job %q (got %v)", k, got)
		}
	}
	if !skip[gridKey{period: may, key: "relative_humidity"}] || skip[gridKey{period: jun, key: "relative_humidity"}] {
		t.Errorf("relative humidity skip = May %v, June %v; want true, false",
			skip[gridKey{period: may, key: "relative_humidity"}], skip[gridKey{period: jun, key: "relative_humidity"}])
	}
	if !skip[gridKey{period: may, key: "2m_temperature"}] || skip[gridKey{period: may, key: "2m_temperature_max"}] {
		t.Error("May: complete temperature should be skipped, missing Tmax fetched")
	}
}

func TestFetchAllGridsReportsEachGrid(t *testing.T) {
	m := []mapping.VariableMapping{{CDSVariable: "2m_temperature"}, {CDSVariable: "broken"}}
	p := []period.YearMonth{{Year: 2024, Month: 5}}
	var mu sync.Mutex
	done := map[string]bool{} // key -> failed

	fetchAllGrids(&fakeFetcher{}, allGridJobs(m, p), 2, 2, func(j gridJob, r gridResult) {
		mu.Lock()
		done[j.m.Key()] = r.err != nil
		mu.Unlock()
	})

	if len(done) != 2 || done["2m_temperature"] || !done["broken"] {
		t.Errorf("onDone calls = %v, want 2m_temperature ok and broken failed", done)
	}
}
