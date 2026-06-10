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

func handlePullOrgUnit(w http.ResponseWriter, r *http.Request, cfg *Config, ohc *OpenHIMClient) {
	log.Printf("Received %s %s", r.Method, r.URL.String())
	transactionID := r.Header.Get("X-OpenHIM-TransactionID")

	respondAccepted(w, cfg.MediatorURN, "Pull org units with coordinates started")

	go func() {
		startTotal := time.Now()
		dhis2 := NewDHIS2Client(cfg.DHIS2TargetURL, cfg.DHIS2TargetPAT)
		hapi := NewHAPIClient(cfg.HAPIFhirURL)
		var orchestrations []Orchestration

		// Fetch org units with coordinates from DHIS2
		startFetch := time.Now()
		orgUnits, err := dhis2.FetchOrgUnitsWithCoordinates()
		endFetch := time.Now()

		if err != nil {
			log.Printf("Fetch org units error: %v", err)
			ohc.updateTransactionFailed(transactionID, cfg.MediatorURN,
				fmt.Sprintf("Failed to fetch org units: %v", err))
			return
		}

		orchestrations = append(orchestrations, Orchestration{
			Name: "fetch-orgUnits-with-coordinates",
			Request: OHRequest{
				Path:      cfg.DHIS2TargetURL + "/api/organisationUnits?filter=geometry:!null",
				Method:    "GET",
				Headers:   map[string]string{"Authorization": "ApiToken ***"},
				Timestamp: startFetch,
			},
			Response: OHResponse{
				Status:    200,
				Headers:   map[string]string{"Content-Type": "application/json"},
				Body:      fmt.Sprintf(`{"count":%d}`, len(orgUnits)),
				Timestamp: endFetch,
			},
		})

		log.Printf("Fetched %d org units with coordinates", len(orgUnits))

		// Save as FHIR Locations with position
		startSave := time.Now()
		success := 0
		failed := 0

		jobs := make(chan OrgUnit, len(orgUnits))
		var mu sync.Mutex
		var wg sync.WaitGroup

		for i := 0; i < cfg.MaxWorkers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ou := range jobs {
					loc := OrgUnitToLocation(ou, cfg.DHIS2TargetURL)
					if err := hapi.PutLocation(loc); err != nil {
						log.Printf("Save Location failed [%s]: %v", ou.ID, err)
						mu.Lock()
						failed++
						mu.Unlock()
					} else {
						mu.Lock()
						success++
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

		orchestrations = append(orchestrations, Orchestration{
			Name: "save-locations-to-hapi-fhir",
			Request: OHRequest{
				Path:      cfg.HAPIFhirURL + "/Location",
				Method:    "PUT",
				Timestamp: startSave,
			},
			Response: OHResponse{
				Status:    200,
				Headers:   map[string]string{"Content-Type": "application/json"},
				Body:      fmt.Sprintf(`{"success":%d,"failed":%d,"total":%d}`, success, failed, len(orgUnits)),
				Timestamp: endSave,
			},
		})

		log.Printf("Saved %d Locations to HAPI, %d failed in %v", success, failed, endSave.Sub(startSave))

		status := "Successful"
		if failed > 0 && success > 0 {
			status = "Completed"
		} else if success == 0 {
			status = "Failed"
		}

		summary, _ := json.MarshalIndent(map[string]interface{}{
			"orgUnitsFound":         len(orgUnits),
			"savedWithCoordinates": success,
			"failed":               failed,
			"duration":             time.Since(startTotal).String(),
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
				"orgUnits.total": strconv.Itoa(len(orgUnits)),
				"orgUnits.saved": strconv.Itoa(success),
			},
		})

		log.Printf("Pull org units completed in %v", time.Since(startTotal))
	}()
}
