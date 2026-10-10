package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/didate/climate-mediator/internal/cds"
	"github.com/didate/climate-mediator/internal/config"
	"github.com/didate/climate-mediator/internal/dhis2"
	"github.com/didate/climate-mediator/internal/fhir"
	"github.com/didate/climate-mediator/internal/mapping"
	"github.com/didate/climate-mediator/internal/openhim"
	"github.com/didate/climate-mediator/internal/period"
	"github.com/didate/climate-mediator/internal/state"
)

// pullRunning prevents concurrent pulls, which would overload the CDS queue and HAPI.
var pullRunning atomic.Bool

func HandlePullClimate(w http.ResponseWriter, r *http.Request, cfg *config.Config, ohc *openhim.OpenHIMClient, mp *mapping.MappingConfigFull, st *state.Store) {
	log.Printf("Received %s %s", r.Method, r.URL.String())
	transactionID := r.Header.Get("X-OpenHIM-TransactionID")

	// Determine periods: either year/month or last N months
	var periods []period.YearMonth
	if y := r.URL.Query().Get("year"); y != "" {
		m := r.URL.Query().Get("month")
		if m == "" {
			openhim.RespondError(w, cfg.MediatorURN, http.StatusBadRequest, "Missing month param when year is specified")
			return
		}
		year, _ := strconv.Atoi(y)
		month, _ := strconv.Atoi(m)
		if year < 1950 || month < 1 || month > 12 {
			openhim.RespondError(w, cfg.MediatorURN, http.StatusBadRequest, "Invalid year or month")
			return
		}
		periods = []period.YearMonth{{Year: year, Month: month}}
	} else {
		months := cfg.DefaultMonths
		if v := r.URL.Query().Get("months"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				months = n
			}
		}
		periods = period.GenerateMonthPeriods(months)
	}

	// missingOnly=true skips grids whose Observations are already all in HAPI,
	// e.g. to complete a backfill without downloading everything again
	missingOnly := r.URL.Query().Get("missingOnly") == "true"

	if !pullRunning.CompareAndSwap(false, true) {
		openhim.RespondError(w, cfg.MediatorURN, http.StatusConflict, "A pull is already running, wait for it to finish")
		return
	}
	params := r.URL.RawQuery

	openhim.RespondAccepted(w, cfg.MediatorURN, fmt.Sprintf("Pull climate data for %d period(s) started", len(periods)))

	go func() {
		defer pullRunning.Store(false)
		startTotal := time.Now()
		runID := st.BeginRun("pull-climate", transactionID, params)
		cdsClient := cds.NewCDSClient(cfg.CDSAPIURL, cfg.CDSAPIKey)
		cdsClient.JobTimeout = time.Duration(cfg.CDSJobTimeoutMin) * time.Minute
		hapi := fhir.NewHAPIClient(cfg.HAPIFhirURL)
		var orchestrations []openhim.Orchestration

		// Get org units from HAPI FHIR
		startLoc := time.Now()
		locations, err := hapi.GetAllLocations(cfg.OUIdentifierSystem)
		endLoc := time.Now()

		if err != nil {
			log.Printf("Fetch locations error: %v", err)
			ohc.UpdateTransactionFailed(transactionID, cfg.MediatorURN,
				fmt.Sprintf("Failed to fetch locations: %v", err))
			st.FinishRun(runID, "Failed", fmt.Sprintf("Failed to fetch locations: %v", err))
			return
		}

		orchestrations = append(orchestrations, openhim.Orchestration{
			Name: "fetch-locations-from-hapi",
			Request: openhim.OHRequest{
				Path:      cfg.HAPIFhirURL + "/Location",
				Method:    "GET",
				Timestamp: startLoc,
			},
			Response: openhim.OHResponse{
				Status:    200,
				Headers:   map[string]string{"Content-Type": "application/json"},
				Body:      fmt.Sprintf(`{"count":%d}`, len(locations)),
				Timestamp: endLoc,
			},
		})

		log.Printf("Got %d locations from HAPI FHIR", len(locations))

		// Convert locations to org units
		orgUnits := make([]dhis2.OrgUnit, 0, len(locations))
		legacy := 0
		for _, loc := range locations {
			ou := fhir.LocationToOrgUnit(&loc)
			// Skip Locations stored under another ID (e.g. before LOCATION_ID_PREFIX)
			// so an org unit is never processed twice with different coordinates
			if loc.ID != fhir.LocationID(cfg.LocationIDPrefix, ou.ID) {
				legacy++
				continue
			}
			if ou.Geometry != nil {
				orgUnits = append(orgUnits, ou)
			}
		}
		if legacy > 0 {
			log.Printf("Skipped %d Location(s) whose ID does not use prefix %q (legacy, run pull-orgunit to recreate them)", legacy, cfg.LocationIDPrefix)
		}
		log.Printf("%d org units have coordinates", len(orgUnits))

		// Totals and orchestrations are updated by the grid goroutines
		var mu sync.Mutex
		totalSaved := 0
		totalFailed := 0
		// "<variable> <period>" of each grid that could not be produced, so a
		// partial run is visible and the missing months can be re-run
		var failedGrids []string
		addOrchestration := func(o openhim.Orchestration) {
			mu.Lock()
			orchestrations = append(orchestrations, o)
			mu.Unlock()
		}
		addCounts := func(saved, failed int) {
			mu.Lock()
			totalSaved += saved
			totalFailed += failed
			mu.Unlock()
		}
		addFailedGrid := func(key string, p period.YearMonth) {
			mu.Lock()
			totalFailed += len(orgUnits)
			failedGrids = append(failedGrids, fmt.Sprintf("%s %s", key, p))
			mu.Unlock()
		}

		// Plan the grids to download
		jobs := allGridJobs(mp.Mappings, periods)
		skip := map[gridKey]bool{}
		if missingOnly {
			jobs, skip = missingGridJobs(stateCounter{st: st, hapi: hapi, expected: len(orgUnits)}, mp.Mappings, mp.Computed, periods, len(orgUnits))
			log.Printf("Missing only: %d grid(s) to fetch, %d already complete in HAPI", len(jobs), len(skip))
		}

		// One grid saved to HAPI at a time, as when saving after the downloads
		saveSem := make(chan struct{}, 1)

		// Relative humidity needs the temperature and dewpoint grids of the same
		// month: it is computed as soon as both arrived, or failed if one failed
		var rhMapping *mapping.ComputedMapping
		for i := range mp.Computed {
			if mp.Computed[i].Compute == "relative_humidity" {
				rhMapping = &mp.Computed[i]
			}
		}
		type rhInputs struct {
			grids map[string]*cds.CDSGridData
			done  bool
		}
		var rhMu sync.Mutex
		rhPending := make(map[period.YearMonth]*rhInputs)
		if rhMapping != nil {
			for _, p := range periods {
				if !skip[gridKey{period: p, key: rhMapping.Name}] {
					rhPending[p] = &rhInputs{grids: make(map[string]*cds.CDSGridData)}
				}
			}
		}
		failRH := func(p period.YearMonth) {
			log.Printf("Cannot compute %s %s: missing temperature or dewpoint grid", rhMapping.Name, p)
			addFailedGrid(rhMapping.Name, p)
			st.MarkGrid(rhMapping.Name, p.String(), "", state.StatusFailed, 0, "missing temperature or dewpoint grid", 0, len(orgUnits))
		}
		// rhInput registers an RH input grid (nil when it failed)
		rhInput := func(p period.YearMonth, key string, grid *cds.CDSGridData) {
			if rhMapping == nil || (key != relativeHumidityInputs[0] && key != relativeHumidityInputs[1]) {
				return
			}
			rhMu.Lock()
			in := rhPending[p]
			if in == nil || in.done {
				rhMu.Unlock()
				return
			}
			if grid == nil {
				in.done = true
				rhMu.Unlock()
				failRH(p)
				return
			}
			in.grids[key] = grid
			tempGrid, dewGrid := in.grids[relativeHumidityInputs[0]], in.grids[relativeHumidityInputs[1]]
			if tempGrid == nil || dewGrid == nil {
				rhMu.Unlock()
				return
			}
			in.done = true
			in.grids = nil
			rhMu.Unlock()

			saveSem <- struct{}{}
			startSave := time.Now()
			saved, failed := saveRelativeHumidity(cfg, hapi, orgUnits, rhMapping, p, tempGrid, dewGrid)
			endSave := time.Now()
			<-saveSem

			addCounts(saved, failed)
			if saved == 0 {
				st.MarkGrid(rhMapping.Name, p.String(), "", state.StatusFailed, 0, "no Observation saved to HAPI", 0, failed)
			} else {
				st.MarkGrid(rhMapping.Name, p.String(), "", state.StatusSaved, 0, "", saved, failed)
			}
			addOrchestration(openhim.Orchestration{
				Name:     fmt.Sprintf("compute-%s-%s", rhMapping.Name, p),
				Request:  openhim.OHRequest{Path: "internal://compute-relative-humidity", Method: "COMPUTE", Timestamp: startSave},
				Response: openhim.OHResponse{Status: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: fmt.Sprintf(`{"saved":%d,"failed":%d}`, saved, failed), Timestamp: endSave},
			})
			log.Printf("Computed %s %s: saved %d, failed %d in %v", rhMapping.Name, p, saved, failed, endSave.Sub(startSave))
		}

		// Each grid is saved to HAPI as soon as it is downloaded: a pull cut
		// short only loses the grids still downloading, and missingOnly resumes
		firstKey := ""
		if len(mp.Mappings) > 0 {
			firstKey = mp.Mappings[0].Key()
		}
		onGrid := func(j gridJob, r gridResult) {
			m, p := j.m, j.p
			fetchPath := fmt.Sprintf("cds://%s/%s?%s", m.CDSDataset, m.Key(), p)

			if r.err != nil {
				st.MarkGrid(m.Key(), p.String(), m.CDSDataset, state.StatusFailed, r.attempts, r.err.Error(), 0, len(orgUnits))
				addOrchestration(openhim.Orchestration{
					Name:     fmt.Sprintf("fetch-cds-%s-%s", m.Key(), p),
					Request:  openhim.OHRequest{Path: fetchPath, Method: "POST", Timestamp: r.start},
					Response: openhim.OHResponse{Status: 500, Headers: map[string]string{"Content-Type": "application/json"}, Body: fmt.Sprintf(`{"error":%q}`, r.err.Error()), Timestamp: r.end},
				})
				addFailedGrid(m.Key(), p)
				rhInput(p, m.Key(), nil)
				return
			}

			st.MarkGrid(m.Key(), p.String(), m.CDSDataset, state.StatusDownloaded, r.attempts, "", 0, 0)
			addOrchestration(openhim.Orchestration{
				Name:     fmt.Sprintf("fetch-cds-%s-%s", m.Key(), p),
				Request:  openhim.OHRequest{Path: fetchPath, Method: "POST", Timestamp: r.start},
				Response: openhim.OHResponse{Status: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: fmt.Sprintf(`{"lats":%d,"lons":%d}`, len(r.grid.Lats), len(r.grid.Lons)), Timestamp: r.end},
			})

			saveSem <- struct{}{}
			startSave := time.Now()
			// ERA5-Land variables share one land-sea mask, so checking the coastal
			// fallback on the first variable is enough
			saved, failed := saveGridObservations(cfg, hapi, orgUnits, m, p, r.grid, m.Key() == firstKey)
			endSave := time.Now()
			<-saveSem

			addCounts(saved, failed)
			addOrchestration(openhim.Orchestration{
				Name:     fmt.Sprintf("save-observations-%s-%s", m.Key(), p),
				Request:  openhim.OHRequest{Path: cfg.HAPIFhirURL + "/Observation", Method: "PUT", Timestamp: startSave},
				Response: openhim.OHResponse{Status: 200, Headers: map[string]string{"Content-Type": "application/json"}, Body: fmt.Sprintf(`{"saved":%d,"failed":%d}`, saved, failed), Timestamp: endSave},
			})
			log.Printf("Variable %s %s: saved %d, failed %d in %v", m.Key(), p, saved, failed, endSave.Sub(startSave))
			if saved == 0 {
				st.MarkGrid(m.Key(), p.String(), m.CDSDataset, state.StatusFailed, r.attempts, "no Observation saved to HAPI", 0, failed)
			} else {
				st.MarkGrid(m.Key(), p.String(), m.CDSDataset, state.StatusSaved, r.attempts, "", saved, failed)
			}
			rhInput(p, m.Key(), r.grid)
		}

		log.Printf("Fetching %d CDS grid(s), %d in parallel, %d per dataset", len(jobs), cfg.CDSMaxParallel, cfg.CDSMaxParallelPerDataset)
		fetchAllGrids(cdsClient, jobs, cfg.CDSMaxParallel, cfg.CDSMaxParallelPerDataset, onGrid)

		// Relative humidity whose inputs never arrived (not part of this run)
		for _, p := range periods {
			if in := rhPending[p]; in != nil && !in.done {
				failRH(p)
			}
		}

		// Update OpenHIM transaction
		status := "Successful"
		if totalFailed > 0 && totalSaved > 0 {
			status = "Completed"
		} else if totalSaved == 0 {
			status = "Failed"
		}

		periodStrs := make([]string, len(periods))
		for i, p := range periods {
			periodStrs[i] = p.String()
		}

		summary, _ := json.MarshalIndent(map[string]interface{}{
			"periods":      periodStrs,
			"orgUnits":     len(orgUnits),
			"variables":    len(mp.Mappings),
			"totalSaved":   totalSaved,
			"totalFailed":  totalFailed,
			"failedGrids":  failedGrids,
			"missingOnly":  missingOnly,
			"skippedGrids": len(skip),
			"duration":     time.Since(startTotal).String(),
		}, "", "  ")
		if len(failedGrids) > 0 {
			log.Printf("Pull climate: %d grid(s) failed, re-run these months: %s", len(failedGrids), strings.Join(failedGrids, ", "))
		}

		ohc.UpdateTransaction(transactionID, map[string]interface{}{
			"status": status,
			"response": map[string]interface{}{
				"status":    200,
				"headers":   map[string]string{"Content-Type": "application/json"},
				"body":      string(summary),
				"timestamp": time.Now(),
			},
			"orchestrations": orchestrations,
			"properties": map[string]string{
				"saved":  strconv.Itoa(totalSaved),
				"failed": strconv.Itoa(totalFailed),
			},
		})

		st.FinishRun(runID, status, string(summary))
		log.Printf("Pull climate completed in %v", time.Since(startTotal))
	}()
}

// saveGridObservations writes one Observation per org unit for a downloaded
// grid and returns how many were saved and failed.
func saveGridObservations(cfg *config.Config, hapi *fhir.HAPIClient, orgUnits []dhis2.OrgUnit, m mapping.VariableMapping, p period.YearMonth, grid *cds.CDSGridData, checkFallback bool) (saved, failed int) {
	jobs := make(chan dhis2.OrgUnit, len(orgUnits))
	var mu sync.Mutex
	var wg sync.WaitGroup
	// Org units that reached a land cell through the coastal fallback, by cell
	fallbackCells := make(map[[2]float64][]string)

	for i := 0; i < cfg.MaxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ou := range jobs {
				lon, lat, ok := ou.Geometry.PointCoordinates()
				if !ok {
					log.Printf("No coordinates [OU=%s %s]", ou.ID, ou.Name)
					mu.Lock()
					failed++
					mu.Unlock()
					continue
				}

				sample, ok := grid.SampleNearest(lat, lon)
				if !ok {
					log.Printf("No grid value [OU=%s %s] lat=%.4f lon=%.4f nearestCell=(%.4f, %.4f) fallback=%t (outside grid or NaN)",
						ou.ID, ou.Name, lat, lon, sample.NearestLat, sample.NearestLon, sample.Fallback)
					mu.Lock()
					failed++
					mu.Unlock()
					continue
				}
				// Only the coastal fallback cases are logged: one line per org
				// unit and grid made the logs too large to read
				if checkFallback && sample.Fallback {
					log.Printf("Grid sample [OU=%s %s] %s %s: centroid=(%.4f, %.4f) nearestCell=(%.4f, %.4f) usedCell=(%.4f, %.4f) fallback=true",
						ou.ID, ou.Name, m.Key(), p, lat, lon, sample.NearestLat, sample.NearestLon, sample.CellLat, sample.CellLon)
					mu.Lock()
					cell := [2]float64{sample.CellLat, sample.CellLon}
					fallbackCells[cell] = append(fallbackCells[cell], ou.Name)
					mu.Unlock()
				}

				value, unit := cds.TransformValue(sample.Value, m.Transform, p.Year, p.Month)
				obs := fhir.ClimateValueToObservation(ou.ID, fhir.LocationID(cfg.LocationIDPrefix, ou.ID), m.Key(), value, unit, p.Year, p.Month, &m)

				err := hapi.PutObservation(obs)
				mu.Lock()
				if err != nil {
					log.Printf("Save Observation failed [%s/%s/%s]: %v", ou.ID, m.Key(), p, err)
					failed++
				} else {
					saved++
				}
				mu.Unlock()
			}
		}()
	}

	for _, ou := range orgUnits {
		jobs <- ou
	}
	close(jobs)
	wg.Wait()

	for cell, names := range fallbackCells {
		if len(names) > 1 {
			log.Printf("WARNING: %d org units use the same grid cell (%.4f, %.4f) via coastal fallback for %s, their values will be identical: %s",
				len(names), cell[0], cell[1], p, strings.Join(names, ", "))
		}
	}
	return saved, failed
}

// saveRelativeHumidity computes relative humidity from the month's temperature
// and dewpoint grids and writes one Observation per org unit.
func saveRelativeHumidity(cfg *config.Config, hapi *fhir.HAPIClient, orgUnits []dhis2.OrgUnit, c *mapping.ComputedMapping, p period.YearMonth, tempGrid, dewGrid *cds.CDSGridData) (saved, failed int) {
	computedMapping := mapping.VariableMapping{
		CDSVariable:           c.Name,
		DHIS2DataElement:      c.DHIS2DataElement,
		DHIS2CategoryOptCombo: c.DHIS2CategoryOptCombo,
	}
	for _, ou := range orgUnits {
		lon, lat, ok := ou.Geometry.PointCoordinates()
		if !ok {
			log.Printf("No coordinates for RH [OU=%s %s]", ou.ID, ou.Name)
			failed++
			continue
		}
		tempVal, ok1 := tempGrid.ExtractValueForCoordinate(lat, lon)
		dewVal, ok2 := dewGrid.ExtractValueForCoordinate(lat, lon)
		if !ok1 || !ok2 {
			failed++
			continue
		}

		rh := cds.ComputeRelativeHumidity(tempVal, dewVal)
		obs := fhir.ClimateValueToObservation(ou.ID, fhir.LocationID(cfg.LocationIDPrefix, ou.ID), c.Name, rh, "%", p.Year, p.Month, &computedMapping)
		if err := hapi.PutObservation(obs); err != nil {
			log.Printf("Save RH Observation failed [%s]: %v", ou.ID, err)
			failed++
		} else {
			saved++
		}
	}
	return saved, failed
}
