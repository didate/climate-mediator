package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

type OpenHIMResponse struct {
	XMediatorURN   string            `json:"x-mediator-urn"`
	Status         string            `json:"status"`
	Response       OHResponse        `json:"response"`
	Orchestrations []Orchestration   `json:"orchestrations"`
	Properties     map[string]string `json:"properties,omitempty"`
}

type OHResponse struct {
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"`
	Timestamp time.Time         `json:"timestamp"`
}

type Orchestration struct {
	Name     string     `json:"name"`
	Request  OHRequest  `json:"request"`
	Response OHResponse `json:"response"`
}

type OHRequest struct {
	Path        string            `json:"path"`
	Headers     map[string]string `json:"headers"`
	Querystring string            `json:"querystring,omitempty"`
	Body        string            `json:"body,omitempty"`
	Method      string            `json:"method"`
	Timestamp   time.Time         `json:"timestamp"`
}

func main() {
	cfg := LoadConfig()

	ohc := NewOpenHIMClient(cfg)
	if err := ohc.Register(); err != nil {
		log.Fatalf("OpenHIM registration failed: %v", err)
	}
	ohc.Heartbeat()

	mapping, err := LoadMapping(cfg.MappingFile)
	if err != nil {
		log.Fatalf("Failed to load mapping: %v", err)
	}
	log.Printf("Loaded %d variable mappings", len(mapping.Mappings))

	http.HandleFunc("/climate/pull-orgunit", func(w http.ResponseWriter, r *http.Request) {
		handlePullOrgUnit(w, r, cfg, ohc)
	})
	http.HandleFunc("/climate/pull-climate", func(w http.ResponseWriter, r *http.Request) {
		handlePullClimate(w, r, cfg, ohc, mapping)
	})
	http.HandleFunc("/climate/push-to-dhis2", func(w http.ResponseWriter, r *http.Request) {
		handlePushToDHIS2(w, r, cfg, ohc, mapping)
	})
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	addr := ":" + cfg.MediatorPort
	log.Printf("Climate mediator listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func respondAccepted(w http.ResponseWriter, mediatorURN, message string) {
	w.Header().Set("Content-Type", "application/json+openhim")
	json.NewEncoder(w).Encode(OpenHIMResponse{
		XMediatorURN: mediatorURN,
		Status:       "Processing",
		Response: OHResponse{
			Status:    202,
			Headers:   map[string]string{"Content-Type": "application/json"},
			Body:      fmt.Sprintf(`{"message":%q}`, message),
			Timestamp: time.Now(),
		},
	})
}

func respondError(w http.ResponseWriter, mediatorURN string, status int, message string) {
	w.Header().Set("Content-Type", "application/json+openhim")
	json.NewEncoder(w).Encode(OpenHIMResponse{
		XMediatorURN: mediatorURN,
		Status:       "Failed",
		Response: OHResponse{
			Status:    status,
			Headers:   map[string]string{"Content-Type": "application/json"},
			Body:      fmt.Sprintf(`{"error":%q}`, message),
			Timestamp: time.Now(),
		},
	})
}
