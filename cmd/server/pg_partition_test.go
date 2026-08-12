package main

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRunMigrationPhasesOrdersDependencies(t *testing.T) {
	var got []string
	phase := func(name string) func() error {
		return func() error {
			got = append(got, name)
			return nil
		}
	}

	if err := runMigrationPhases(phase("bootstrap"), phase("dual"), phase("versioned")); err != nil {
		t.Fatal(err)
	}
	want := []string{"bootstrap", "dual", "versioned"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("phase order = %v, want %v", got, want)
	}
}

func TestRunMigrationPhasesStopsAfterFailure(t *testing.T) {
	wantErr := errors.New("dual failed")
	var got []string
	err := runMigrationPhases(
		func() error { got = append(got, "bootstrap"); return nil },
		func() error { got = append(got, "dual"); return wantErr },
		func() error { got = append(got, "versioned"); return nil },
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	want := []string{"bootstrap", "dual"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("phases = %v, want %v", got, want)
	}
}

func TestReconcileAuditMirrorsRejectsSameHashDifferentContentBeforeMutation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &pgStore{db: db}

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT content_hash.*HAVING COUNT`).
		WillReturnRows(sqlmock.NewRows([]string{"content_hash"}).AddRow("conflicting-hash"))
	mock.ExpectRollback()

	err = p.withPgTx(context.Background(), func(tx *sql.Tx) error {
		_, err := reconcileAuditMirrors(context.Background(), tx)
		return err
	})
	var conflict *errAuditMirrorConflict
	if !errors.As(err, &conflict) || conflict.Kind != "content_hash" {
		t.Fatalf("error = %#v, want content_hash conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileAuditMirrorsRejectsSameSequenceDifferentHashBeforeMutation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &pgStore{db: db}

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT content_hash.*HAVING COUNT`).
		WillReturnRows(sqlmock.NewRows([]string{"content_hash"}))
	mock.ExpectQuery(`(?s)SELECT chain_seq.*HAVING COUNT`).
		WillReturnRows(sqlmock.NewRows([]string{"chain_seq"}).AddRow(int64(19)))
	mock.ExpectRollback()

	err = p.withPgTx(context.Background(), func(tx *sql.Tx) error {
		_, err := reconcileAuditMirrors(context.Background(), tx)
		return err
	})
	var conflict *errAuditMirrorConflict
	if !errors.As(err, &conflict) || conflict.Kind != "chain_seq" {
		t.Fatalf("error = %#v, want chain_seq conflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileAuditMirrorsRepairsOnlyUnambiguousHistory(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &pgStore{db: db}

	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT content_hash.*HAVING COUNT`).
		WillReturnRows(sqlmock.NewRows([]string{"content_hash"}))
	mock.ExpectQuery(`(?s)SELECT chain_seq.*HAVING COUNT`).
		WillReturnRows(sqlmock.NewRows([]string{"chain_seq"}))
	mock.ExpectExec(`(?s)DELETE FROM audit_log_p`).
		WillReturnResult(sqlmock.NewResult(0, 162))
	mock.ExpectExec(`(?s)INSERT INTO audit_log\s*\(`).
		WillReturnResult(sqlmock.NewResult(0, 18))
	mock.ExpectExec(`(?s)INSERT INTO audit_log_p\s*\(`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`(?s)setval.*audit_log.*MAX\(id\)`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)setval.*audit_log_p.*MAX\(id\)`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)SELECT.*COUNT\(\*\).*audit_log.*audit_log_p.*COUNT\(DISTINCT content_hash\)`).
		WillReturnRows(sqlmock.NewRows([]string{"legacy_rows", "partition_rows", "unique_hashes"}).AddRow(int64(229), int64(229), int64(229)))
	mock.ExpectCommit()

	var got auditMirrorStats
	err = p.withPgTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		got, err = reconcileAuditMirrors(context.Background(), tx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	want := auditMirrorStats{
		LegacyRows: 229, PartitionRows: 229, UniqueHashes: 229,
		RemovedDuplicates: 162, BackfilledLegacy: 18, BackfilledPartition: 0,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stats = %+v, want %+v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedPartitionParentsExcludesAuditHistory(t *testing.T) {
	got := retainedPartitionParents()
	want := []string{"events_p"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("retention parents = %v, want %v", got, want)
	}
}

func TestBackfillPartitionTwinsUsesMultiplicityAwareBidirectionalRanks(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &pgStore{db: db}

	mock.ExpectBegin()
	for i := 0; i < 4; i++ {
		mock.ExpectExec(`(?s)setval.*MAX\(id\)`).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(`(?s)ROW_NUMBER\(\) OVER.*INSERT INTO events_p`).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(`(?s)ROW_NUMBER\(\) OVER.*INSERT INTO events\s*\(ts, data\)`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)ROW_NUMBER\(\) OVER.*INSERT INTO ai_call_events_p`).WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(`(?s)ROW_NUMBER\(\) OVER.*INSERT INTO ai_call_events\s*\(`).WillReturnResult(sqlmock.NewResult(0, 0))
	for i := 0; i < 4; i++ {
		mock.ExpectExec(`(?s)setval.*MAX\(id\)`).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectCommit()

	if err := p.backfillPartitionTwins(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestBackfillPartitionTwinsRollsBackOnRepairFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &pgStore{db: db}

	mock.ExpectBegin()
	for i := 0; i < 4; i++ {
		mock.ExpectExec(`(?s)setval.*MAX\(id\)`).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	wantErr := errors.New("rank repair failed")
	mock.ExpectExec(`(?s)ROW_NUMBER\(\) OVER.*INSERT INTO events_p`).WillReturnError(wantErr)
	mock.ExpectRollback()

	if err := p.backfillPartitionTwins(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want repair failure", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
