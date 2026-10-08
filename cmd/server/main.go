package main

import (
	"log"
	"net/http"

	"github.com/didate/climate-mediator/internal/config"
	"github.com/didate/climate-mediator/internal/handler"
	"github.com/didate/climate-mediator/internal/mapping"
	"github.com/didate/climate-mediator/internal/openhim"
	"github.com/didate/climate-mediator/internal/state"
)

func main() {
	cfg := config.LoadConfig()

	ohc := openhim.NewOpenHIMClient(cfg)
	if err := ohc.Register(); err != nil {
		log.Fatalf("OpenHIM registration failed: %v", err)
	}
	ohc.Heartbeat()

	mp, err := mapping.LoadMapping(cfg.MappingFile)
	if err != nil {
		log.Fatalf("Failed to load mapping: %v", err)
	}
	log.Printf("Loaded %d variable mappings", len(mp.Mappings))

	// Without the state database the mediator still works, only without status
	// tracking and with missing-only pulls falling back to counting in HAPI
	st, err := state.Open(cfg.StateDBPath)
	if err != nil {
		log.Printf("State database unavailable, continuing without it: %v", err)
		st = nil
	} else {
		log.Printf("State database: %s", cfg.StateDBPath)
		defer st.Close()
	}

	http.HandleFunc("/climate/pull-orgunit", func(w http.ResponseWriter, r *http.Request) {
		handler.HandlePullOrgUnit(w, r, cfg, ohc)
	})
	http.HandleFunc("/climate/pull-climate", func(w http.ResponseWriter, r *http.Request) {
		handler.HandlePullClimate(w, r, cfg, ohc, mp, st)
	})
	http.HandleFunc("/climate/push-to-dhis2", func(w http.ResponseWriter, r *http.Request) {
		handler.HandlePushToDHIS2(w, r, cfg, ohc, mp, st)
	})
	http.HandleFunc("/climate/status", func(w http.ResponseWriter, r *http.Request) {
		handler.HandleStatus(w, r, st)
	})
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	addr := ":" + cfg.MediatorPort
	log.Printf("Climate mediator listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
