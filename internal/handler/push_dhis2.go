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
	"github.com/didate/climate-mediator/internal/mapping"
	"github.com/didate/climate-mediator/internal/openhim"
	"github.com/didate/climate-mediator/internal/period"
)

func HandlePushToDHIS2(w http.ResponseWriter, r *http.Request, cfg *config.Config, ohc *openhim.OpenHIMClient, mp *mapping.MappingConfigFull) {
	log.Printf("Received %s %s", r.Method, r.URL.String())
	transactionID := r.Header.Get("X-OpenHIM-TransactionID")

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
		months := 3
		if v := r.URL.Query().Get("months"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				months = n
			}
		}
		periods = period.GenerateMonthPeriods(months)
	}

	openhim.RespondAccepted(w, cfg.MediatorURN, fmt.Sprintf("Push climate data for %d period(s) to DHIS2 started", len(periods)))

	go func() {
		startTotal := time.Now()
		target := dhis2.NewDHIS2Client(cfg.DHIS2TargetURL, cfg.DHIS2TargetPAT)
		hapi := fhir.NewHAPIClient(cfg.HAPIFhirURL)
		var orchestrations []openhim.Orchestration

		// Collect all variable codes to search (mappings + computed)
		var varCodes []string
		for _, m := range mp.Mappings {
			varCodes = append(varCodes, m.CDSVariable)
		}
		for _, c := range mp.Computed {
			varCodes = append(varCodes, c.Name)
		}

		// Fetch observations for each period x variable from HAPI
		var allObservations []fhir.FHIRObservation

		for _, p := range periods {
			date := p.String()
			for _, varCode := range varCodes {
				startFetch := time.Now()
				obs, err := hapi.GetObservations(varCode, date)
				endFetch := time.Now()

				if err != nil {
					log.Printf("Fetch observations for %s %s error: %v", varCode, p, err)
					continue
				}

				allObservations = append(allObservations, obs...)

				orchestrations = append(orchestrations, openhim.Orchestration{
					Name: fmt.Sprintf("fetch-observations-%s-%s", varCode, p),
					Request: openhim.OHRequest{
						Path:      fmt.Sprintf("%s/Observation?code=%s&date=%s", cfg.HAPIFhirURL, varCode, date),
						Method:    "GET",
						Timestamp: startFetch,
					},
					Response: openhim.OHResponse{
						Status:    200,
						Headers:   map[string]string{"Content-Type": "application/json"},
						Body:      fmt.Sprintf(`{"count":%d}`, len(obs)),
						Timestamp: endFetch,
					},
				})

				log.Printf("Got %d observations for %s %s", len(obs), varCode, p)
			}
		}

		if len(allObservations) == 0 {
			ohc.UpdateTransaction(transactionID, map[string]interface{}{
				"status": "Completed",
				"response": map[string]interface{}{
					"status":    200,
					"headers":   map[string]string{"Content-Type": "application/json"},
					"body":      `{"message":"No observations found to push"}`,
					"timestamp": time.Now(),
				},
				"orchestrations": orchestrations,
			})
			return
		}

		// Group data values by org unit for efficient posting
		orgUnitValues := make(map[string][]dhis2.DataValue)
		for _, obs := range allObservations {
			dv := fhir.ObservationToDataValue(&obs)
			if dv.OrgUnit != "" && dv.DataElement != "" && dv.Value != "" {
				orgUnitValues[dv.OrgUnit] = append(orgUnitValues[dv.OrgUnit], *dv)
			}
		}

		// Push to DHIS2
		startPush := time.Now()
		pushSuccess := 0
		pushFail := 0
		totalImported := 0
		totalUpdated := 0
		totalIgnored := 0

		type pushJob struct {
			OrgUnit    string
			DataValues []dhis2.DataValue
		}

		jobs := make(chan pushJob, len(orgUnitValues))
		var mu sync.Mutex
		var wg sync.WaitGroup

		for i := 0; i < cfg.MaxWorkers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for job := range jobs {
					dvs := &dhis2.DataValueSet{
						OrgUnit:    job.OrgUnit,
						DataValues: job.DataValues,
					}

					respBody, _, err := target.PostDataValueSet(dvs)
					if err != nil {
						log.Printf("Push failed [OU=%s]: %v", job.OrgUnit, err)
						mu.Lock()
						pushFail++
						mu.Unlock()
						continue
					}

					ic := dhis2.ParseImportCount(respBody)
					mu.Lock()
					pushSuccess++
					totalImported += ic.Imported
					totalUpdated += ic.Updated
					totalIgnored += ic.Ignored
					mu.Unlock()
				}
			}()
		}

		for ouID, values := range orgUnitValues {
			jobs <- pushJob{OrgUnit: ouID, DataValues: values}
		}
		close(jobs)
		wg.Wait()
		endPush := time.Now()

		orchestrations = append(orchestrations, openhim.Orchestration{
			Name: "push-to-dhis2",
			Request: openhim.OHRequest{
				Path:      cfg.DHIS2TargetURL + "/api/dataValueSets",
				Method:    "BATCH-POST",
				Timestamp: startPush,
			},
			Response: openhim.OHResponse{
				Status:  200,
				Headers: map[string]string{"Content-Type": "application/json"},
				Body: fmt.Sprintf(`{"success":%d,"failed":%d,"imported":%d,"updated":%d,"ignored":%d}`,
					pushSuccess, pushFail, totalImported, totalUpdated, totalIgnored),
				Timestamp: endPush,
			},
		})

		log.Printf("Push complete: %d success, %d failed (imported=%d, updated=%d) in %v",
			pushSuccess, pushFail, totalImported, totalUpdated, endPush.Sub(startPush))

		status := "Successful"
		if pushFail > 0 && pushSuccess > 0 {
			status = "Completed"
		} else if pushSuccess == 0 {
			status = "Failed"
		}

		summary, _ := json.MarshalIndent(map[string]interface{}{
			"periods":      len(periods),
			"observations": len(allObservations),
			"orgUnits":     len(orgUnitValues),
			"pushSuccess":  pushSuccess,
			"pushFail":     pushFail,
			"imported":     totalImported,
			"updated":      totalUpdated,
			"ignored":      totalIgnored,
			"duration":     time.Since(startTotal).String(),
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
				"push.success":  strconv.Itoa(pushSuccess),
				"push.failed":   strconv.Itoa(pushFail),
				"push.imported": strconv.Itoa(totalImported),
				"push.updated":  strconv.Itoa(totalUpdated),
			},
		})

		log.Printf("Push to DHIS2 completed in %v", time.Since(startTotal))
	}()
}
