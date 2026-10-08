// Package state keeps a small SQLite record of pull/push runs and of each
// variable x month grid, so gaps are visible and missing-only pulls do not
// depend on searching HAPI.
//
// All methods are no-ops on a nil *Store: the mediator keeps working if the
// database cannot be opened.
package state

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Grid statuses.
const (
	StatusDownloaded = "downloaded" // grid fetched from CDS, Observations not written yet
	StatusSaved      = "saved"      // Observations written to HAPI
	StatusFailed     = "failed"     // CDS fetch or computation failed
)

type Store struct {
	db *sql.DB
}

// Grid is the state of one variable for one month.
type Grid struct {
	Variable  string `json:"variable"`
	Period    string `json:"period"` // YYYY-MM
	Dataset   string `json:"dataset,omitempty"`
	Status    string `json:"status"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"lastError,omitempty"`
	Saved     int    `json:"saved"`
	Failed    int    `json:"failed"`
	PushedAt  string `json:"pushedAt,omitempty"`
	UpdatedAt string `json:"updatedAt"`
}

// Run is one pull or push execution.
type Run struct {
	ID            int64  `json:"id"`
	Kind          string `json:"kind"`
	TransactionID string `json:"transactionId,omitempty"`
	Params        string `json:"params,omitempty"`
	Status        string `json:"status"`
	StartedAt     string `json:"startedAt"`
	FinishedAt    string `json:"finishedAt,omitempty"`
	Summary       string `json:"summary,omitempty"`
}

const schema = `
CREATE TABLE IF NOT EXISTS grids (
	variable   TEXT NOT NULL,
	period     TEXT NOT NULL,
	dataset    TEXT NOT NULL DEFAULT '',
	status     TEXT NOT NULL,
	attempts   INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	saved      INTEGER NOT NULL DEFAULT 0,
	failed     INTEGER NOT NULL DEFAULT 0,
	pushed_at  TEXT NOT NULL DEFAULT '',
	updated_at TEXT NOT NULL,
	PRIMARY KEY (variable, period)
);
CREATE TABLE IF NOT EXISTS runs (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	kind           TEXT NOT NULL,
	transaction_id TEXT NOT NULL DEFAULT '',
	params         TEXT NOT NULL DEFAULT '',
	status         TEXT NOT NULL,
	started_at     TEXT NOT NULL,
	finished_at    TEXT NOT NULL DEFAULT '',
	summary        TEXT NOT NULL DEFAULT ''
);`

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// Open opens (or creates) the database at path.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open state db: %w", err)
	}
	// One connection: writes come from many goroutines, SQLite serializes them anyway
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create state schema: %w", err)
	}
	// Runs still "running" were cut short by a restart
	if _, err := db.Exec(`UPDATE runs SET status = 'interrupted', finished_at = ? WHERE status = 'running'`, now()); err != nil {
		db.Close()
		return nil, fmt.Errorf("mark interrupted runs: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	return s.db.Close()
}

func logErr(op string, err error) {
	if err != nil {
		log.Printf("State: %s failed: %v", op, err)
	}
}

// BeginRun records the start of a run and returns its ID.
func (s *Store) BeginRun(kind, transactionID, params string) int64 {
	if s == nil {
		return 0
	}
	res, err := s.db.Exec(`INSERT INTO runs (kind, transaction_id, params, status, started_at) VALUES (?, ?, ?, 'running', ?)`,
		kind, transactionID, params, now())
	if err != nil {
		logErr("begin run", err)
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// FinishRun records the end of a run with its OpenHIM status and JSON summary.
func (s *Store) FinishRun(id int64, status, summary string) {
	if s == nil || id == 0 {
		return
	}
	_, err := s.db.Exec(`UPDATE runs SET status = ?, finished_at = ?, summary = ? WHERE id = ?`, status, now(), summary, id)
	logErr("finish run", err)
}

// MarkGrid records the state of a grid. Saving it again resets pushed_at:
// the Observations changed and have to be pushed again.
func (s *Store) MarkGrid(variable, period, dataset, status string, attempts int, lastError string, saved, failed int) {
	if s == nil {
		return
	}
	_, err := s.db.Exec(`
		INSERT INTO grids (variable, period, dataset, status, attempts, last_error, saved, failed, pushed_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?)
		ON CONFLICT (variable, period) DO UPDATE SET
			dataset = excluded.dataset, status = excluded.status, attempts = excluded.attempts,
			last_error = excluded.last_error, saved = excluded.saved, failed = excluded.failed,
			pushed_at = CASE WHEN excluded.status = 'saved' THEN '' ELSE grids.pushed_at END,
			updated_at = excluded.updated_at`,
		variable, period, dataset, status, attempts, lastError, saved, failed, now())
	logErr("mark grid", err)
}

// MarkPushed records that a grid's Observations were pushed to DHIS2.
func (s *Store) MarkPushed(variable, period string) {
	if s == nil {
		return
	}
	_, err := s.db.Exec(`UPDATE grids SET pushed_at = ? WHERE variable = ? AND period = ?`, now(), variable, period)
	logErr("mark pushed", err)
}

// GetGrid returns the state of a grid, or false if it was never recorded.
func (s *Store) GetGrid(variable, period string) (Grid, bool, error) {
	if s == nil {
		return Grid{}, false, nil
	}
	var g Grid
	err := s.db.QueryRow(`SELECT variable, period, dataset, status, attempts, last_error, saved, failed, pushed_at, updated_at
		FROM grids WHERE variable = ? AND period = ?`, variable, period).
		Scan(&g.Variable, &g.Period, &g.Dataset, &g.Status, &g.Attempts, &g.LastError, &g.Saved, &g.Failed, &g.PushedAt, &g.UpdatedAt)
	if err == sql.ErrNoRows {
		return Grid{}, false, nil
	}
	if err != nil {
		return Grid{}, false, err
	}
	return g, true, nil
}

// Grids lists grid states, optionally only for periods starting with prefix (e.g. "2024").
func (s *Store) Grids(prefix string) ([]Grid, error) {
	if s == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT variable, period, dataset, status, attempts, last_error, saved, failed, pushed_at, updated_at
		FROM grids WHERE period LIKE ? ORDER BY period, variable`, prefix+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var grids []Grid
	for rows.Next() {
		var g Grid
		if err := rows.Scan(&g.Variable, &g.Period, &g.Dataset, &g.Status, &g.Attempts, &g.LastError, &g.Saved, &g.Failed, &g.PushedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		grids = append(grids, g)
	}
	return grids, rows.Err()
}

// Runs lists the most recent runs, newest first.
func (s *Store) Runs(limit int) ([]Run, error) {
	if s == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT id, kind, transaction_id, params, status, started_at, finished_at, summary
		FROM runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.Kind, &r.TransactionID, &r.Params, &r.Status, &r.StartedAt, &r.FinishedAt, &r.Summary); err != nil {
			return nil, err
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}
