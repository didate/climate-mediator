package handler

import (
	"errors"
	"log"
	"sync"
	"time"

	"github.com/didate/climate-mediator/internal/cds"
	"github.com/didate/climate-mediator/internal/mapping"
	"github.com/didate/climate-mediator/internal/period"
)

// gridFetcher is the part of the CDS client used to download grids.
type gridFetcher interface {
	FetchMonthlyData(dataset, variable, productType string, year, month int) (*cds.CDSGridData, error)
	FetchDailyStatistics(dataset, variable, statistic, monthlyAggregation string, year, month int) (*cds.CDSGridData, error)
}

// fetchRetryDelays are the waits before each retry of a transient CDS failure
// (502 from the CDS gateway, job timeout or failure). Variable for tests.
var fetchRetryDelays = []time.Duration{1 * time.Minute, 5 * time.Minute}

// gridKey identifies one downloaded grid: a mapping (by Key) for a period.
type gridKey struct {
	period period.YearMonth
	key    string
}

type gridResult struct {
	grid       *cds.CDSGridData
	err        error
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

// fetchAllGrids downloads every mapping x period grid with at most parallel CDS
// requests in flight, so CDS queue time overlaps instead of adding up.
func fetchAllGrids(c gridFetcher, mappings []mapping.VariableMapping, periods []period.YearMonth, parallel int) map[gridKey]gridResult {
	if parallel < 1 {
		parallel = 1
	}
	results := make(map[gridKey]gridResult, len(mappings)*len(periods))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)

	for _, p := range periods {
		for _, m := range mappings {
			wg.Add(1)
			go func(m mapping.VariableMapping, p period.YearMonth) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				start := time.Now()
				grid, err := fetchGrid(c, m, p)
				for attempt, delay := range fetchRetryDelays {
					if err == nil || errors.Is(err, cds.ErrPermanent) {
						break
					}
					log.Printf("CDS fetch for %s %s failed (attempt %d/%d), retrying in %v: %v", m.Key(), p, attempt+1, len(fetchRetryDelays)+1, delay, err)
					time.Sleep(delay)
					grid, err = fetchGrid(c, m, p)
				}
				r := gridResult{grid: grid, err: err, start: start, end: time.Now()}
				if err != nil {
					log.Printf("CDS fetch failed for %s %s: %v", m.Key(), p, err)
				} else {
					log.Printf("Downloaded CDS grid for %s %s: %dx%d in %v", m.Key(), p, len(grid.Lats), len(grid.Lons), r.end.Sub(start))
				}

				mu.Lock()
				results[gridKey{period: p, key: m.Key()}] = r
				mu.Unlock()
			}(m, p)
		}
	}
	wg.Wait()
	return results
}
