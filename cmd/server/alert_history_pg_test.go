package main

import (
	"encoding/json"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
)

func TestCleanupAlertHistoryUsesFiredAt(t *testing.T) {
	p, mock := mockPgStore(t)
	mock.ExpectExec(`DELETE FROM alert_history WHERE fired_at > 0 AND fired_at < \$1`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 3))

	p.cleanupAlertHistory(90)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("cleanup must delete by fired_at (schema has no ts/created_at): %v", err)
	}
}

func TestResolveAlertRecordUpdatesJSONBAndMatchesByKey(t *testing.T) {
	p, mock := mockPgStore(t)
	const key = "host-1/cpu"
	const resolvedAt int64 = 1_700_000_100
	mock.ExpectExec(`UPDATE alert_history\s+SET resolved_at = \$1,\s+data = jsonb_set`).
		WithArgs(resolvedAt, key).
		WillReturnResult(sqlmock.NewResult(0, 1))

	p.resolveAlertRecord(key, resolvedAt)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("resolve must update JSONB by key: %v", err)
	}
}

func TestLoadRecentAlertsOverlaysResolvedAtColumn(t *testing.T) {
	p, mock := mockPgStore(t)
	raw, err := json.Marshal(AlertRecord{
		ID: 7, Key: "host-1/cpu", Status: "firing", FiredAt: 1_700_000_000, ResolvedAt: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	const columnResolved int64 = 1_700_000_050
	rows := sqlmock.NewRows([]string{"data", "resolved_at"}).AddRow(raw, columnResolved)
	mock.ExpectQuery(`SELECT data, COALESCE\(resolved_at, 0\)`).
		WithArgs(10).
		WillReturnRows(rows)

	got, err := p.loadRecentAlerts(10)
	if err != nil {
		t.Fatalf("loadRecentAlerts: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 record, got %d", len(got))
	}
	if got[0].ResolvedAt != columnResolved {
		t.Fatalf("ResolvedAt overlay: want %d, got %d", columnResolved, got[0].ResolvedAt)
	}
	if got[0].Status != "resolved" {
		t.Fatalf("Status overlay: want resolved, got %q", got[0].Status)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestStoreAlertHistoryRoundTripPersistsResolvedState(t *testing.T) {
	p, mock := mockPgStore(t)
	s := NewStore()
	s.pg = p

	rec := AlertRecord{Key: "h1/disk", HostID: "h1", Level: "warning", Type: "disk", Message: "full"}
	mock.ExpectExec(`INSERT INTO alert_history\(key,fired_at,data\)`).
		WithArgs(rec.Key, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	id := s.AddAlertRecord(rec)
	if id <= 0 {
		t.Fatalf("expected positive id, got %d", id)
	}

	resolvedAt := time.Now().Unix()
	mock.ExpectExec(`UPDATE alert_history\s+SET resolved_at = \$1,\s+data = jsonb_set`).
		WithArgs(resolvedAt, rec.Key).
		WillReturnResult(sqlmock.NewResult(0, 1))
	s.ResolveAlert(rec.Key, resolvedAt)

	hist := s.AlertHistory(10, false)
	if len(hist) != 1 || hist[0].Status != "resolved" || hist[0].ResolvedAt != resolvedAt {
		t.Fatalf("in-memory resolve broken: %+v", hist)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("PG write order/args: %v", err)
	}
}
