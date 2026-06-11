package main

import (
	"log"
	"net/http"

	"github.com/didate/climate-mediator/internal/config"
	"github.com/didate/climate-mediator/internal/handler"
	"github.com/didate/climate-mediator/internal/mapping"
	"github.com/didate/climate-mediator/internal/openhim"
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

	http.HandleFunc("/climate/pull-orgunit", func(w http.ResponseWriter, r *http.Request) {
		handler.HandlePullOrgUnit(w, r, cfg, ohc)
	})
	http.HandleFunc("/climate/pull-climate", func(w http.ResponseWriter, r *http.Request) {
		handler.HandlePullClimate(w, r, cfg, ohc, mp)
	})
	http.HandleFunc("/climate/push-to-dhis2", func(w http.ResponseWriter, r *http.Request) {
		handler.HandlePushToDHIS2(w, r, cfg, ohc, mp)
	})
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	addr := ":" + cfg.MediatorPort
	log.Printf("Climate mediator listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
