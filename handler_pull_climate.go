package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

func handlePullClimate(w http.ResponseWriter, r *http.Request, cfg *Config, ohc *OpenHIMClient, mapping *MappingConfigFull) {
	log.Printf("Received %s %s", r.Method, r.URL.String())
	transactionID := r.Header.Get("X-OpenHIM-TransactionID")

	// Determine periods: either year/month or last N months
	var periods []YearMonth
	if y := r.URL.Query().Get("year"); y != "" {
		m := r.URL.Query().Get("month")
		if m == "" {
			respondError(w, cfg.MediatorURN, http.StatusBadRequest, "Missing month param when year is specified")
			return
		}
		year, _ := strconv.Atoi(y)
		month, _ := strconv.Atoi(m)
		if year < 1950 || month < 1 || month > 12 {
			respondError(w, cfg.MediatorURN, http.StatusBadRequest, "Invalid year or month")
			return
		}
		periods = []YearMonth{{Year: year, Month: month}}
	} else {
		months := 3 // default
		if v := r.URL.Query().Get("months"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				months = n
			}
		}
		periods = GenerateMonthPeriods(months)
	}

	respondAccepted(w, cfg.MediatorURN, fmt.Sprintf("Pull climate data for %d period(s) started", len(periods)))

	go func() {
		startTotal := time.Now()
		cds := NewCDSClient(cfg.CDSAPIURL, cfg.CDSAPIKey)
		hapi := NewHAPIClient(cfg.HAPIFhirURL)
		var orchestrations []Orchestration

		// Get org units from HAPI FHIR
		startLoc := time.Now()
		locations, err := hapi.GetAllLocations(cfg.OUIdentifierSystem)
		endLoc := time.Now()

		if err != nil {
			log.Printf("Fetch locations error: %v", err)
			ohc.updateTransactionFailed(transactionID, cfg.MediatorURN,
				fmt.Sprintf("Failed to fetch locations: %v", err))
			return
		}

		orchestrations = append(orchestrations, Orchestration{
			Name: "fetch-locations-from-hapi",
			Request: OHRequest{
				Path:      cfg.HAPIFhirURL + "/Location",
				Method:    "GET",
				Timestamp: startLoc,
			},
			Response: OHResponse{
				Status:    200,
				Headers:   map[string]string{"Content-Type": "application/json"},
				Body:      fmt.Sprintf(`{"count":%d}`, len(locations)),
				Timestamp: endLoc,
			},
		})

		log.Printf("Got %d locations from HAPI FHIR", len(locations))

		// Convert locations to org units
		orgUnits := make([]OrgUnit, 0, len(locations))
		for _, loc := range locations {
			ou := LocationToOrgUnit(&loc)
			if ou.Geometry != nil {
				orgUnits = append(orgUnits, ou)
			}
		}
		log.Printf("%d org units have coordinates", len(orgUnits))

		// For each period × variable, download CDS data and extract values
		totalSaved := 0
		totalFailed := 0

		for _, p := range periods {
			// Store grids for computed variables (e.g., relative humidity)
			grids := make(map[string]*CDSGridData)

			for _, m := range mapping.Mappings {
				startCDS := time.Now()
				grid, err := cds.FetchMonthlyData(m.CDSDataset, m.CDSVariable, m.CDSProductType, p.Year, p.Month)
				endCDS := time.Now()

				if err != nil {
					log.Printf("CDS fetch failed for %s %s: %v", m.CDSVariable, p, err)
					orchestrations = append(orchestrations, Orchestration{
						Name: fmt.Sprintf("fetch-cds-%s-%s", m.CDSVariable, p),
						Request: OHRequest{
							Path:      fmt.Sprintf("cds://%s/%s?%s", m.CDSDataset, m.CDSVariable, p),
							Method:    "POST",
							Timestamp: startCDS,
						},
						Response: OHResponse{
							Status:    500,
							Headers:   map[string]string{"Content-Type": "application/json"},
							Body:      fmt.Sprintf(`{"error":%q}`, err.Error()),
							Timestamp: endCDS,
						},
					})
					continue
				}

				orchestrations = append(orchestrations, Orchestration{
					Name: fmt.Sprintf("fetch-cds-%s-%s", m.CDSVariable, p),
					Request: OHRequest{
						Path:      fmt.Sprintf("cds://%s/%s?%s", m.CDSDataset, m.CDSVariable, p),
						Method:    "POST",
						Timestamp: startCDS,
					},
					Response: OHResponse{
						Status:    200,
						Headers:   map[string]string{"Content-Type": "application/json"},
						Body:      fmt.Sprintf(`{"lats":%d,"lons":%d}`, len(grid.Lats), len(grid.Lons)),
						Timestamp: endCDS,
					},
				})

				log.Printf("Downloaded CDS grid for %s %s: %dx%d", m.CDSVariable, p, len(grid.Lats), len(grid.Lons))
				grids[m.CDSVariable] = grid

				// Extract values for each org unit and save as Observations
				startSave := time.Now()
				saved := 0
				failed := 0

				jobs := make(chan OrgUnit, len(orgUnits))
				var mu sync.Mutex
				var wg sync.WaitGroup
				currentMapping := m
				currentPeriod := p

				for i := 0; i < cfg.MaxWorkers; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for ou := range jobs {
							lon, lat, ok := ou.Geometry.PointCoordinates()
							if !ok {
								mu.Lock()
								failed++
								mu.Unlock()
								continue
							}

							rawValue, ok := grid.ExtractValueForCoordinate(lat, lon)
							if !ok {
								mu.Lock()
								failed++
								mu.Unlock()
								continue
							}

							value, unit := TransformValue(rawValue, currentMapping.Transform)
							obs := ClimateValueToObservation(ou.ID, currentMapping.CDSVariable, value, unit, currentPeriod.Year, currentPeriod.Month, &currentMapping)

							if err := hapi.PutObservation(obs); err != nil {
								log.Printf("Save Observation failed [%s/%s/%s]: %v", ou.ID, currentMapping.CDSVariable, currentPeriod, err)
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

				totalSaved += saved
				totalFailed += failed

				orchestrations = append(orchestrations, Orchestration{
					Name: fmt.Sprintf("save-observations-%s-%s", m.CDSVariable, p),
					Request: OHRequest{
						Path:      cfg.HAPIFhirURL + "/Observation",
						Method:    "PUT",
						Timestamp: startSave,
					},
					Response: OHResponse{
						Status:    200,
						Headers:   map[string]string{"Content-Type": "application/json"},
						Body:      fmt.Sprintf(`{"saved":%d,"failed":%d}`, saved, failed),
						Timestamp: endSave,
					},
				})

				log.Printf("Variable %s %s: saved %d, failed %d in %v", m.CDSVariable, p, saved, failed, endSave.Sub(startSave))
			}

			// Compute derived variables (e.g., relative humidity)
			for _, c := range mapping.Computed {
				if c.Compute == "relative_humidity" {
					tempGrid := grids["2m_temperature"]
					dewGrid := grids["2m_dewpoint_temperature"]
					if tempGrid == nil || dewGrid == nil {
						log.Printf("Cannot compute %s: missing temperature or dewpoint grid", c.Name)
						continue
					}

					startSave := time.Now()
					saved := 0
					failed := 0
					computedMapping := VariableMapping{
						CDSVariable:           c.Name,
						DHIS2DataElement:      c.DHIS2DataElement,
						DHIS2CategoryOptCombo: c.DHIS2CategoryOptCombo,
					}

					for _, ou := range orgUnits {
						lon, lat, ok := ou.Geometry.PointCoordinates()
						if !ok {
							failed++
							continue
						}

						tempVal, ok1 := tempGrid.ExtractValueForCoordinate(lat, lon)
						dewVal, ok2 := dewGrid.ExtractValueForCoordinate(lat, lon)
						if !ok1 || !ok2 {
							failed++
							continue
						}

						rh := ComputeRelativeHumidity(tempVal, dewVal)
						obs := ClimateValueToObservation(ou.ID, c.Name, rh, "%", p.Year, p.Month, &computedMapping)

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

					orchestrations = append(orchestrations, Orchestration{
						Name: fmt.Sprintf("compute-%s-%s", c.Name, p),
						Request: OHRequest{
							Path:      "internal://compute-relative-humidity",
							Method:    "COMPUTE",
							Timestamp: startSave,
						},
						Response: OHResponse{
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
			"variables":   len(mapping.Mappings),
			"totalSaved":  totalSaved,
			"totalFailed": totalFailed,
			"duration":    time.Since(startTotal).String(),
		}, "", "  ")

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
