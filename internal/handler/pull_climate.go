package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/didate/climate-mediator/internal/cds"
	"github.com/didate/climate-mediator/internal/config"
	"github.com/didate/climate-mediator/internal/dhis2"
	"github.com/didate/climate-mediator/internal/fhir"
	"github.com/didate/climate-mediator/internal/mapping"
	"github.com/didate/climate-mediator/internal/openhim"
	"github.com/didate/climate-mediator/internal/period"
)

func HandlePullClimate(w http.ResponseWriter, r *http.Request, cfg *config.Config, ohc *openhim.OpenHIMClient, mp *mapping.MappingConfigFull) {
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

	openhim.RespondAccepted(w, cfg.MediatorURN, fmt.Sprintf("Pull climate data for %d period(s) started", len(periods)))

	go func() {
		startTotal := time.Now()
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

		// For each period x variable, download CDS data and extract values
		totalSaved := 0
		totalFailed := 0
		// "<variable> <period>" of each grid that could not be produced, so a
		// partial run is visible and the missing months can be re-run
		var failedGrids []string

		// Download all grids first, several CDS requests at a time
		log.Printf("Fetching %d CDS grid(s), %d in parallel", len(periods)*len(mp.Mappings), cfg.CDSMaxParallel)
		fetched := fetchAllGrids(cdsClient, mp.Mappings, periods, cfg.CDSMaxParallel)

		for _, p := range periods {
			// Store grids for computed variables (e.g., relative humidity)
			grids := make(map[string]*cds.CDSGridData)

			for mi, m := range mp.Mappings {
				r := fetched[gridKey{period: p, key: m.Key()}]
				grid, err, startCDS, endCDS := r.grid, r.err, r.start, r.end

				if err != nil {
					orchestrations = append(orchestrations, openhim.Orchestration{
						Name: fmt.Sprintf("fetch-cds-%s-%s", m.Key(), p),
						Request: openhim.OHRequest{
							Path:      fmt.Sprintf("cds://%s/%s?%s", m.CDSDataset, m.Key(), p),
							Method:    "POST",
							Timestamp: startCDS,
						},
						Response: openhim.OHResponse{
							Status:    500,
							Headers:   map[string]string{"Content-Type": "application/json"},
							Body:      fmt.Sprintf(`{"error":%q}`, err.Error()),
							Timestamp: endCDS,
						},
					})
					totalFailed += len(orgUnits)
					failedGrids = append(failedGrids, fmt.Sprintf("%s %s", m.Key(), p))
					continue
				}

				orchestrations = append(orchestrations, openhim.Orchestration{
					Name: fmt.Sprintf("fetch-cds-%s-%s", m.Key(), p),
					Request: openhim.OHRequest{
						Path:      fmt.Sprintf("cds://%s/%s?%s", m.CDSDataset, m.Key(), p),
						Method:    "POST",
						Timestamp: startCDS,
					},
					Response: openhim.OHResponse{
						Status:    200,
						Headers:   map[string]string{"Content-Type": "application/json"},
						Body:      fmt.Sprintf(`{"lats":%d,"lons":%d}`, len(grid.Lats), len(grid.Lons)),
						Timestamp: endCDS,
					},
				})

				grids[m.Key()] = grid

				// Extract values for each org unit and save as Observations
				startSave := time.Now()
				saved := 0
				failed := 0

				jobs := make(chan dhis2.OrgUnit, len(orgUnits))
				var mu sync.Mutex
				var wg sync.WaitGroup
				currentMapping := m
				currentPeriod := p
				// Org units that reached a land cell through the coastal fallback, by cell.
				// ERA5-Land variables share one land-sea mask, so the first variable is enough.
				checkFallback := mi == 0
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
							rawValue := sample.Value
							if checkFallback && sample.Fallback {
								mu.Lock()
								cell := [2]float64{sample.CellLat, sample.CellLon}
								fallbackCells[cell] = append(fallbackCells[cell], ou.Name)
								mu.Unlock()
							}

							log.Printf("Grid sample [OU=%s %s] %s %s: centroid=(%.4f, %.4f) nearestCell=(%.4f, %.4f) usedCell=(%.4f, %.4f) fallback=%t raw=%g",
								ou.ID, ou.Name, currentMapping.Key(), currentPeriod, lat, lon,
								sample.NearestLat, sample.NearestLon, sample.CellLat, sample.CellLon, sample.Fallback, rawValue)

							value, unit := cds.TransformValue(rawValue, currentMapping.Transform, currentPeriod.Year, currentPeriod.Month)
							obs := fhir.ClimateValueToObservation(ou.ID, fhir.LocationID(cfg.LocationIDPrefix, ou.ID), currentMapping.Key(), value, unit, currentPeriod.Year, currentPeriod.Month, &currentMapping)

							if err := hapi.PutObservation(obs); err != nil {
								log.Printf("Save Observation failed [%s/%s/%s]: %v", ou.ID, currentMapping.Key(), currentPeriod, err)
								mu.Lock()
								failed++
								mu.Unlock()
							} else {
								mu.Lock()
								saved++
								mu.Unlock()
							}
						}
					}()
				}

				for _, ou := range orgUnits {
					jobs <- ou
				}
				close(jobs)
				wg.Wait()
				endSave := time.Now()

				for cell, names := range fallbackCells {
					if len(names) > 1 {
						log.Printf("WARNING: %d org units use the same grid cell (%.4f, %.4f) via coastal fallback for %s, their values will be identical: %s",
							len(names), cell[0], cell[1], p, strings.Join(names, ", "))
					}
				}

				totalSaved += saved
				totalFailed += failed

				orchestrations = append(orchestrations, openhim.Orchestration{
					Name: fmt.Sprintf("save-observations-%s-%s", m.Key(), p),
					Request: openhim.OHRequest{
						Path:      cfg.HAPIFhirURL + "/Observation",
						Method:    "PUT",
						Timestamp: startSave,
					},
					Response: openhim.OHResponse{
						Status:    200,
						Headers:   map[string]string{"Content-Type": "application/json"},
						Body:      fmt.Sprintf(`{"saved":%d,"failed":%d}`, saved, failed),
						Timestamp: endSave,
					},
				})

				log.Printf("Variable %s %s: saved %d, failed %d in %v", m.Key(), p, saved, failed, endSave.Sub(startSave))
			}

			// Compute derived variables (e.g., relative humidity)
			for _, c := range mp.Computed {
				if c.Compute == "relative_humidity" {
					tempGrid := grids["2m_temperature"]
					dewGrid := grids["2m_dewpoint_temperature"]
					if tempGrid == nil || dewGrid == nil {
						log.Printf("Cannot compute %s %s: missing temperature or dewpoint grid", c.Name, p)
						totalFailed += len(orgUnits)
						failedGrids = append(failedGrids, fmt.Sprintf("%s %s", c.Name, p))
						continue
					}

					startSave := time.Now()
					saved := 0
					failed := 0
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
					endSave := time.Now()
					totalSaved += saved
					totalFailed += failed

					orchestrations = append(orchestrations, openhim.Orchestration{
						Name: fmt.Sprintf("compute-%s-%s", c.Name, p),
						Request: openhim.OHRequest{
							Path:      "internal://compute-relative-humidity",
							Method:    "COMPUTE",
							Timestamp: startSave,
						},
						Response: openhim.OHResponse{
							Status:    200,
							Headers:   map[string]string{"Content-Type": "application/json"},
							Body:      fmt.Sprintf(`{"saved":%d,"failed":%d}`, saved, failed),
							Timestamp: endSave,
						},
					})

					log.Printf("Computed %s %s: saved %d, failed %d in %v", c.Name, p, saved, failed, endSave.Sub(startSave))
				}
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
			"periods":     periodStrs,
			"orgUnits":    len(orgUnits),
			"variables":   len(mp.Mappings),
			"totalSaved":  totalSaved,
			"totalFailed": totalFailed,
			"failedGrids": failedGrids,
			"duration":    time.Since(startTotal).String(),
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

		log.Printf("Pull climate completed in %v", time.Since(startTotal))
	}()
}
