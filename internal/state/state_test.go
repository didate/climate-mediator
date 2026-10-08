package state

import (
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "sub", "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestGridLifecycle(t *testing.T) {
	st := openTest(t)

	if _, ok, err := st.GetGrid("2m_temperature", "2024-05"); ok || err != nil {
		t.Fatalf("unknown grid: ok=%v err=%v", ok, err)
	}

	st.MarkGrid("2m_temperature", "2024-05", "reanalysis-era5-land-monthly-means", StatusFailed, 3, "CDS job rejected", 0, 3189)
	g, ok, _ := st.GetGrid("2m_temperature", "2024-05")
	if !ok || g.Status != StatusFailed || g.Attempts != 3 || g.LastError != "CDS job rejected" {
		t.Fatalf("failed grid = %+v", g)
	}

	st.MarkGrid("2m_temperature", "2024-05", "reanalysis-era5-land-monthly-means", StatusSaved, 1, "", 3189, 0)
	st.MarkPushed("2m_temperature", "2024-05")
	g, _, _ = st.GetGrid("2m_temperature", "2024-05")
	if g.Status != StatusSaved || g.Saved != 3189 || g.LastError != "" || g.PushedAt == "" {
		t.Fatalf("saved+pushed grid = %+v", g)
	}

	// Saving again means new values: they must be pushed again
	st.MarkGrid("2m_temperature", "2024-05", "reanalysis-era5-land-monthly-means", StatusSaved, 1, "", 3189, 0)
	if g, _, _ = st.GetGrid("2m_temperature", "2024-05"); g.PushedAt != "" {
		t.Errorf("re-saved grid still marked pushed: %+v", g)
	}

	st.MarkGrid("total_precipitation", "2025-01", "", StatusSaved, 1, "", 3189, 0)
	grids, err := st.Grids("2024")
	if err != nil || len(grids) != 1 || grids[0].Period != "2024-05" {
		t.Errorf("Grids(2024) = %+v, %v", grids, err)
	}
	if all, _ := st.Grids(""); len(all) != 2 {
		t.Errorf("Grids() = %d grids, want 2", len(all))
	}
}

func TestRunsAndInterruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	done := st.BeginRun("push-to-dhis2", "tx1", "months=1")
	st.FinishRun(done, "Successful", `{"pushSuccess":3189}`)
	st.BeginRun("pull-climate", "tx2", "months=45") // cut short by a restart
	st.Close()

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	runs, err := st.Runs(10)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	if runs[0].Kind != "pull-climate" || runs[0].Status != "interrupted" || runs[0].FinishedAt == "" {
		t.Errorf("unfinished run after restart = %+v, want interrupted", runs[0])
	}
	if runs[1].Status != "Successful" || runs[1].Summary != `{"pushSuccess":3189}` {
		t.Errorf("finished run = %+v", runs[1])
	}
}

func TestNilStoreIsNoop(t *testing.T) {
	var st *Store
	st.MarkGrid("v", "2024-01", "", StatusSaved, 1, "", 1, 0)
	st.MarkPushed("v", "2024-01")
	st.FinishRun(st.BeginRun("pull-climate", "", ""), "Successful", "")
	if _, ok, err := st.GetGrid("v", "2024-01"); ok || err != nil {
		t.Errorf("nil store GetGrid: ok=%v err=%v", ok, err)
	}
	if err := st.Close(); err != nil {
		t.Error(err)
	}
}
