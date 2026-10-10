package handler

import (
	"errors"
	"log"
	"sync"
	"time"

	"github.com/didate/climate-mediator/internal/cds"
	"github.com/didate/climate-mediator/internal/mapping"
	"github.com/didate/climate-mediator/internal/period"
	"github.com/didate/climate-mediator/internal/state"
)

// gridFetcher is the part of the CDS client used to download grids.
type gridFetcher interface {
	FetchMonthlyData(dataset, variable, productType string, year, month int) (*cds.CDSGridData, error)
	FetchDailyStatistics(dataset, variable, statistic, monthlyAggregation string, year, month int) (*cds.CDSGridData, error)
}

// fetchRetryDelays are the waits before each retry of a transient CDS failure
// (502 from the CDS gateway, job timeout or failure). Variable for tests.
var fetchRetryDelays = []time.Duration{1 * time.Minute, 5 * time.Minute}

// queueRetryDelays are the waits after a rejection for the per-dataset queue
// limit: longer, to let our own queued jobs finish first. Variable for tests.
var queueRetryDelays = []time.Duration{10 * time.Minute, 10 * time.Minute, 15 * time.Minute, 15 * time.Minute, 20 * time.Minute}

// gridKey identifies one downloaded grid: a mapping (by Key) for a period.
type gridKey struct {
	period period.YearMonth
	key    string
}

type gridResult struct {
	grid       *cds.CDSGridData
	err        error
	attempts   int
	start, end time.Time
}

// fetchGrid downloads the grid of one mapping for one period. The CDS request
// always uses the CDS variable, never the mapping name.
func fetchGrid(c gridFetcher, m mapping.VariableMapping, p period.YearMonth) (*cds.CDSGridData, error) {
	if m.DailyStatistic != "" {
		return c.FetchDailyStatistics(m.CDSDataset, m.CDSVariable, m.DailyStatistic, m.MonthlyAggregation, p.Year, p.Month)
	}
	return c.FetchMonthlyData(m.CDSDataset, m.CDSVariable, m.CDSProductType, p.Year, p.Month)
}

// gridJob is one grid to download: a mapping for a period.
type gridJob struct {
	m mapping.VariableMapping
	p period.YearMonth
}

// allGridJobs returns every mapping x period grid.
func allGridJobs(mappings []mapping.VariableMapping, periods []period.YearMonth) []gridJob {
	jobs := make([]gridJob, 0, len(mappings)*len(periods))
	for _, p := range periods {
		for _, m := range mappings {
			jobs = append(jobs, gridJob{m: m, p: p})
		}
	}
	return jobs
}

// relativeHumidityInputs are the mapping keys relative humidity is computed from.
var relativeHumidityInputs = []string{"2m_temperature", "2m_dewpoint_temperature"}

// observationCounter counts the Observations already stored for a code and month.
type observationCounter interface {
	CountObservations(code string, year, month int) (int, error)
}

// stateCounter answers from the state database, and falls back to counting in
// HAPI for grids pulled before the database existed. A grid HAPI shows as
// complete (expected Observations) is recorded as saved, so the database
// catches up with earlier runs.
type stateCounter struct {
	st       *state.Store
	hapi     observationCounter
	expected int
}

func (c stateCounter) CountObservations(code string, year, month int) (int, error) {
	g, ok, err := c.st.GetGrid(code, period.YearMonth{Year: year, Month: month}.String())
	if err != nil {
		return 0, err
	}
	if !ok {
		n, err := c.hapi.CountObservations(code, year, month)
		if err == nil && n == c.expected {
			c.st.MarkGrid(code, period.YearMonth{Year: year, Month: month}.String(), "", state.StatusSaved, 0, "", n, 0)
		}
		return n, err
	}
	if g.Status != state.StatusSaved {
		return 0, nil
	}
	return g.Saved, nil
}

// missingGridJobs plans a "missing only" run: a mapping or computed variable is
// complete for a period when HAPI holds exactly one Observation per org unit.
// It returns the grids to download and the keys to skip. A missing computed
// variable (relative humidity) also downloads its inputs.
func missingGridJobs(counter observationCounter, mappings []mapping.VariableMapping, computed []mapping.ComputedMapping, periods []period.YearMonth, orgUnits int) ([]gridJob, map[gridKey]bool) {
	complete := func(code string, p period.YearMonth) bool {
		n, err := counter.CountObservations(code, p.Year, p.Month)
		if err != nil {
			log.Printf("Count observations for %s %s failed, will re-fetch: %v", code, p, err)
			return false
		}
		return n == orgUnits
	}

	var jobs []gridJob
	skip := make(map[gridKey]bool)
	for _, p := range periods {
		needed := make(map[string]bool)
		for _, m := range mappings {
			if !complete(m.Key(), p) {
				needed[m.Key()] = true
			}
		}
		for _, c := range computed {
			if complete(c.Name, p) {
				skip[gridKey{period: p, key: c.Name}] = true
				continue
			}
			if c.Compute == "relative_humidity" {
				for _, k := range relativeHumidityInputs {
					needed[k] = true
				}
			}
		}
		for _, m := range mappings {
			if needed[m.Key()] {
				jobs = append(jobs, gridJob{m: m, p: p})
			} else {
				skip[gridKey{period: p, key: m.Key()}] = true
			}
		}
	}
	return jobs, skip
}

// fetchAllGrids downloads the grids with at most parallel CDS requests in
// flight overall and at most perDataset per CDS dataset: CDS rejects jobs when
// an account queues too many requests for one dataset, while different
// datasets queue separately.
//
// onDone, if not nil, is called as soon as each grid is downloaded or has
// finally failed, so progress is visible during long runs.
func fetchAllGrids(c gridFetcher, jobs []gridJob, parallel, perDataset int, onDone func(gridJob, gridResult)) map[gridKey]gridResult {
	if parallel < 1 {
		parallel = 1
	}
	if perDataset < 1 {
		perDataset = 1
	}
	results := make(map[gridKey]gridResult, len(jobs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)
	datasetSems := make(map[string]chan struct{})
	for _, j := range jobs {
		if datasetSems[j.m.CDSDataset] == nil {
			datasetSems[j.m.CDSDataset] = make(chan struct{}, perDataset)
		}
	}

	for _, j := range jobs {
		wg.Add(1)
		go func(m mapping.VariableMapping, p period.YearMonth) {
			defer wg.Done()
			// Dataset slot first, so a job waiting for its dataset never holds
			// a global slot another dataset could use
			dsem := datasetSems[m.CDSDataset]
			dsem <- struct{}{}
			defer func() { <-dsem }()
			sem <- struct{}{}
			defer func() { <-sem }()

			start := time.Now()
			grid, err := fetchGrid(c, m, p)
			transient, limited := 0, 0
			for err != nil && !errors.Is(err, cds.ErrPermanent) {
				var delay time.Duration
				if errors.Is(err, cds.ErrQueueLimited) {
					if limited == len(queueRetryDelays) {
						break
					}
					delay = queueRetryDelays[limited]
					limited++
				} else {
					if transient == len(fetchRetryDelays) {
						break
					}
					delay = fetchRetryDelays[transient]
					transient++
				}
				log.Printf("CDS fetch for %s %s failed, retrying in %v: %v", m.Key(), p, delay, err)
				time.Sleep(delay)
				grid, err = fetchGrid(c, m, p)
			}
			r := gridResult{grid: grid, err: err, attempts: 1 + transient + limited, start: start, end: time.Now()}
			if err != nil {
				log.Printf("CDS fetch failed for %s %s: %v", m.Key(), p, err)
			} else {
				log.Printf("Downloaded CDS grid for %s %s: %dx%d in %v", m.Key(), p, len(grid.Lats), len(grid.Lons), r.end.Sub(start))
			}

			if onDone != nil {
				onDone(gridJob{m: m, p: p}, r)
			}
			mu.Lock()
			results[gridKey{period: p, key: m.Key()}] = r
			mu.Unlock()
		}(j.m, j.p)
	}
	wg.Wait()
	return results
}
