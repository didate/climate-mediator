package handler

import (
	"encoding/json"
	"net/http"

	"github.com/didate/climate-mediator/internal/state"
)

// statusSummary counts grids by status, plus saved grids not pushed to DHIS2 yet.
type statusSummary struct {
	Downloaded int `json:"downloaded"`
	Saved      int `json:"saved"`
	Failed     int `json:"failed"`
	NotPushed  int `json:"savedNotPushed"`
}

// HandleStatus returns the state of every grid (optionally for one year with
// ?year=2024) and the latest runs.
func HandleStatus(w http.ResponseWriter, r *http.Request, st *state.Store) {
	w.Header().Set("Content-Type", "application/json")
	if st == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "state database unavailable"})
		return
	}

	grids, err := st.Grids(r.URL.Query().Get("year"))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	runs, err := st.Runs(20)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	var sum statusSummary
	for _, g := range grids {
		switch g.Status {
		case state.StatusDownloaded:
			sum.Downloaded++
		case state.StatusSaved:
			sum.Saved++
			if g.PushedAt == "" {
				sum.NotPushed++
			}
		case state.StatusFailed:
			sum.Failed++
		}
	}

	if grids == nil {
		grids = []state.Grid{}
	}
	if runs == nil {
		runs = []state.Run{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false) // keep "&" readable in run params
	enc.Encode(map[string]interface{}{
		"summary": sum,
		"grids":   grids,
		"runs":    runs,
	})
}
