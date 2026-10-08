package handler

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/didate/climate-mediator/internal/state"
)

func TestHandleStatus(t *testing.T) {
	st, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.MarkGrid("2m_temperature", "2024-05", "ds", state.StatusSaved, 1, "", 3189, 0)
	st.MarkGrid("2m_temperature_max", "2024-05", "ds", state.StatusFailed, 6, "CDS job rejected", 0, 3189)
	st.MarkGrid("2m_temperature", "2025-01", "ds", state.StatusSaved, 1, "", 3189, 0)
	st.MarkPushed("2m_temperature", "2025-01")

	rec := httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest("GET", "/climate/status?year=2024", nil), st)

	var body struct {
		Summary statusSummary `json:"summary"`
		Grids   []state.Grid  `json:"grids"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	if len(body.Grids) != 2 || body.Summary.Saved != 1 || body.Summary.Failed != 1 || body.Summary.NotPushed != 1 {
		t.Errorf("status = %+v", body)
	}

	rec = httptest.NewRecorder()
	HandleStatus(rec, httptest.NewRequest("GET", "/climate/status", nil), nil)
	if rec.Code != 503 {
		t.Errorf("without state db: code %d, want 503", rec.Code)
	}
}

func TestStateCounter(t *testing.T) {
	st, err := state.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.MarkGrid("2m_temperature", "2024-05", "ds", state.StatusSaved, 1, "", 3189, 0)
	st.MarkGrid("total_precipitation", "2024-05", "ds", state.StatusFailed, 6, "rejected", 0, 3189)
	hapi := fakeCounter{"2m_dewpoint_temperature 2024-05": 3189, "total_precipitation 2024-05": 3189}
	c := stateCounter{st: st, hapi: hapi}

	for code, want := range map[string]int{
		"2m_temperature":          3189, // saved in the state db
		"total_precipitation":     0,    // failed in the state db, HAPI not trusted
		"2m_dewpoint_temperature": 3189, // unknown to the db: counted in HAPI
	} {
		if got, _ := c.CountObservations(code, 2024, 5); got != want {
			t.Errorf("%s = %d, want %d", code, got, want)
		}
	}
}
