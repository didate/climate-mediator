package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/didate/climate-mediator/internal/config"
	"github.com/didate/climate-mediator/internal/dhis2"
	"github.com/didate/climate-mediator/internal/fhir"
	"github.com/didate/climate-mediator/internal/openhim"
)

func HandlePullOrgUnit(w http.ResponseWriter, r *http.Request, cfg *config.Config, ohc *openhim.OpenHIMClient) {
	log.Printf("Received %s %s", r.Method, r.URL.String())
	transactionID := r.Header.Get("X-OpenHIM-TransactionID")

	openhim.RespondAccepted(w, cfg.MediatorURN, "Pull org units with coordinates started")

	go func() {
		startTotal := time.Now()
		d := dhis2.NewDHIS2Client(cfg.DHIS2TargetURL, cfg.DHIS2TargetPAT)
		hapi := fhir.NewHAPIClient(cfg.HAPIFhirURL)
		var orchestrations []openhim.Orchestration

		// Save workers start first so each DHIS2 page is written to HAPI while the next one is fetched
		success := 0
		failed := 0
		total := 0

		jobs := make(chan dhis2.OrgUnit, dhis2.OrgUnitPageSize)
		var mu sync.Mutex
		var wg sync.WaitGroup

		for i := 0; i < cfg.MaxWorkers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ou := range jobs {
					loc := fhir.OrgUnitToLocation(ou, cfg.OUIdentifierSystem)
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

		// Fetch org units with coordinates from DHIS2, page by page
		startFetch := time.Now()
		pages, fetchErr := d.FetchOrgUnitsWithCoordinates(func(page []dhis2.OrgUnit) error {
			total += len(page)
			for _, ou := range page {
				jobs <- ou
			}
			return nil
		})
		endFetch := time.Now()
		close(jobs)
		wg.Wait()
		endSave := time.Now()

		fetchStatus := 200
		fetchBody := fmt.Sprintf(`{"count":%d,"pages":%d}`, total, pages)
		if fetchErr != nil {
			log.Printf("Fetch org units error after %d page(s): %v", pages, fetchErr)
			fetchStatus = 502
			fetchBody = fmt.Sprintf(`{"count":%d,"pages":%d,"error":%q}`, total, pages, fetchErr.Error())
		}

		orchestrations = append(orchestrations, openhim.Orchestration{
			Name: "fetch-orgUnits-with-coordinates",
			Request: openhim.OHRequest{
				Path:      fmt.Sprintf("%s/api/organisationUnits?filter=geometry:!null&pageSize=%d", cfg.DHIS2TargetURL, dhis2.OrgUnitPageSize),
				Method:    "GET",
				Headers:   map[string]string{"Authorization": "ApiToken ***"},
				Timestamp: startFetch,
			},
			Response: openhim.OHResponse{
				Status:    fetchStatus,
				Headers:   map[string]string{"Content-Type": "application/json"},
				Body:      fetchBody,
				Timestamp: endFetch,
			},
		})

		log.Printf("Fetched %d org units with coordinates in %d page(s)", total, pages)

		orchestrations = append(orchestrations, openhim.Orchestration{
			Name: "save-locations-to-hapi-fhir",
			Request: openhim.OHRequest{
				Path:      cfg.HAPIFhirURL + "/Location",
				Method:    "PUT",
				Timestamp: startFetch,
			},
			Response: openhim.OHResponse{
				Status:    200,
				Headers:   map[string]string{"Content-Type": "application/json"},
				Body:      fmt.Sprintf(`{"success":%d,"failed":%d,"total":%d}`, success, failed, total),
				Timestamp: endSave,
			},
		})

		log.Printf("Saved %d Locations to HAPI, %d failed in %v", success, failed, endSave.Sub(startFetch))

		status := "Successful"
		if success == 0 {
			status = "Failed"
		} else if failed > 0 || fetchErr != nil {
			status = "Completed"
		}

		summaryMap := map[string]interface{}{
			"orgUnitsFound":        total,
			"pages":                pages,
			"savedWithCoordinates": success,
			"failed":               failed,
			"duration":             time.Since(startTotal).String(),
		}
		if fetchErr != nil {
			summaryMap["fetchError"] = fetchErr.Error()
		}
		summary, _ := json.MarshalIndent(summaryMap, "", "  ")

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
				"orgUnits.total": strconv.Itoa(total),
				"orgUnits.saved": strconv.Itoa(success),
			},
		})

		log.Printf("Pull org units completed in %v", time.Since(startTotal))
	}()
}
