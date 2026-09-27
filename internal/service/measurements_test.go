package service

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/model"
)

func TestOperatorMeasurementsPersistAndAnalyzeSuppliedValues(t *testing.T) {
	directory := t.TempDir()
	a, err := OpenMeasurementArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create(CreateRunInput{Name: "test A", ModelName: "hull X", Section: "section-a", Carriage: "carriage-1"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UTC()
	for _, input := range []MeasurementInput{
		{ClientID: "frame-1", Kind: model.EvidenceWaveTime, SourceID: "left", Value: 0.1, RecordedAt: at},
		{ClientID: "frame-2", Kind: model.EvidenceWaveTime, SourceID: "right", Value: 0.3, RecordedAt: at.Add(time.Second)},
		{ClientID: "frame-3", Kind: model.EvidenceForce, SourceID: "load-cell", Value: 12, RecordedAt: at},
	} {
		if _, err := a.Record(run.Operation.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := OpenMeasurementArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := loaded.Analyze(run.Operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Wave.Count != 2 || math.Abs(analysis.Wave.Mean-0.2) > 1e-9 || analysis.ForceCount != 1 || analysis.ForceMean != 12 {
		t.Fatalf("analysis did not reflect supplied values after reload: %+v", analysis)
	}
	runs := loaded.List()
	if len(runs) != 1 || len(runs[0].Evidence) != 3 || !runs[0].Evidence[0].Durable || runs[0].Drill {
		t.Fatalf("persisted evidence missing or mislabeled: %+v", runs)
	}
	if runs[0].Evidence[0].ClientID != "frame-1" {
		t.Fatalf("client frame id not persisted: %+v", runs[0].Evidence[0])
	}
}

func TestOperatorMeasurementRejectsInvalidAndUnknownRun(t *testing.T) {
	a, err := OpenMeasurementArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create(CreateRunInput{Name: "test B", ModelName: "hull X", Section: "section-a", Carriage: "carriage-1"})
	if err != nil {
		t.Fatal(err)
	}
	input := MeasurementInput{ClientID: "frame-x", Kind: model.EvidenceWaveTime, SourceID: "left", Value: 1, RecordedAt: time.Now().UTC()}
	if _, err := a.Record("missing", input); err == nil {
		t.Fatal("unknown run accepted")
	}
	input.Value = math.NaN()
	if _, err := a.Record(run.Operation.ID, input); err == nil {
		t.Fatal("NaN accepted")
	}
	if len(a.List()[0].Evidence) != 0 {
		t.Fatal("rejected values changed archive")
	}
}

func conflictReason(t *testing.T, err error) ConflictReason {
	t.Helper()
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected ConflictError, got %v", err)
	}
	return conflict.Reason
}

func TestRecordIdempotentRetryReturnsDuplicateFrame(t *testing.T) {
	a, _ := OpenMeasurementArchive(t.TempDir())
	run, err := a.Create(CreateRunInput{Name: "retry", ModelName: "hull", Section: "s", Carriage: "c"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UTC()
	input := MeasurementInput{ClientID: "frame-r1", Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 0.42, RecordedAt: at}
	first, err := a.Record(run.Operation.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	// Connection dropped before the client saw the response: exact retry.
	again, err := a.Record(run.Operation.ID, input)
	if err == nil {
		t.Fatal("idempotent retry must be reported as a conflict, not stored twice")
	}
	if reason := conflictReason(t, err); reason != ConflictDuplicateFrame {
		t.Fatalf("reason = %s, want duplicate_frame", reason)
	}
	if again.ID != first.ID {
		t.Fatal("retry must return the originally stored evidence")
	}
	if got := a.List()[0].Evidence; len(got) != 1 {
		t.Fatalf("retry duplicated the frame: %d stored", len(got))
	}
}

func TestRecordDetectsFrameContentMismatchAndObservationClash(t *testing.T) {
	a, _ := OpenMeasurementArchive(t.TempDir())
	run, err := a.Create(CreateRunInput{Name: "clash", ModelName: "hull", Section: "s", Carriage: "c"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UTC()

	if _, err := a.Record(run.Operation.ID, MeasurementInput{
		ClientID: "f1", Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 0.42, RecordedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	// Same frame id, different reading.
	_, err = a.Record(run.Operation.ID, MeasurementInput{
		ClientID: "f1", Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 0.99, RecordedAt: at,
	})
	if reason := conflictReason(t, err); reason != ConflictFrameContentMismatch {
		t.Fatalf("reason = %s, want frame_content_mismatch", reason)
	}
	// Different frame id but the same probe/kind/observation-time slot.
	_, err = a.Record(run.Operation.ID, MeasurementInput{
		ClientID: "f2", Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 0.42, RecordedAt: at,
	})
	if reason := conflictReason(t, err); reason != ConflictDuplicateObservation {
		t.Fatalf("reason = %s, want duplicate_observation", reason)
	}
	if got := a.List()[0].Evidence; len(got) != 1 {
		t.Fatalf("conflicting frames changed the archive: %d stored", len(got))
	}
}

func TestRecordBatchPartiallyAcceptsAndReportsConflicts(t *testing.T) {
	a, _ := OpenMeasurementArchive(t.TempDir())
	run, err := a.Create(CreateRunInput{Name: "batch", ModelName: "hull", Section: "s", Carriage: "c"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UTC()
	frames := []MeasurementInput{
		{ClientID: "b1", Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 1, RecordedAt: at},
		{ClientID: "b1", Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 1, RecordedAt: at},         // duplicate inside batch
		{ClientID: "b2", Kind: model.EvidenceForce, SourceID: "load-cell", Value: math.NaN(), RecordedAt: at}, // invalid
		{ClientID: "b3", Kind: model.EvidenceForce, SourceID: "load-cell", Value: 5, RecordedAt: at},
	}
	result, err := a.RecordBatch(run.Operation.ID, frames)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accepted) != 2 {
		t.Fatalf("accepted = %d, want 2: %+v", len(result.Accepted), result)
	}
	if len(result.Conflicts) != 2 {
		t.Fatalf("conflicts = %d, want 2: %+v", len(result.Conflicts), result.Conflicts)
	}
	want := map[int]ConflictReason{1: ConflictDuplicateFrame, 2: ConflictInvalidFrame}
	for _, c := range result.Conflicts {
		if want[c.Index] != c.Reason {
			t.Fatalf("conflict at %d = %s, want %s", c.Index, c.Reason, want[c.Index])
		}
	}
	if got := a.List()[0].Evidence; len(got) != 2 {
		t.Fatalf("archive should hold the two accepted frames, got %d", len(got))
	}
	// Replaying the whole batch after an ambiguous response stores nothing new.
	replay, err := a.RecordBatch(run.Operation.ID, frames)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Accepted) != 0 || len(replay.Conflicts) != 4 {
		t.Fatalf("replay should be fully idempotent: %+v", replay)
	}
}

func TestRecordBatchUnknownRun(t *testing.T) {
	a, _ := OpenMeasurementArchive(t.TempDir())
	if _, err := a.RecordBatch("missing", nil); err == nil {
		t.Fatal("unknown run accepted")
	}
}

func TestCreateRunOfflineIDIsIdempotentAndGuardsMetadata(t *testing.T) {
	a, _ := OpenMeasurementArchive(t.TempDir())
	id := model.NewRunID()
	input := CreateRunInput{ID: id, Name: "offline", ModelName: "hull", Section: "s", Carriage: "c", Drill: true}
	first, err := a.Create(input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Operation.ID != id || !first.Drill {
		t.Fatalf("client id/drill not honored: %+v", first)
	}
	// Re-confirming the same offline run after reconnect is idempotent.
	again, err := a.Create(input)
	if err != nil || again.Operation.Revision != first.Operation.Revision {
		t.Fatalf("idempotent re-create failed: %v", err)
	}
	// Same id with different metadata must be a content mismatch.
	_, err = a.Create(CreateRunInput{ID: id, Name: "renamed", ModelName: "hull", Section: "s", Carriage: "c"})
	if reason := conflictReason(t, err); reason != ConflictRunContentMismatch {
		t.Fatalf("reason = %s, want run_content_mismatch", reason)
	}
}

func TestCreateRunRejectsMalformedUUID(t *testing.T) {
	a, _ := OpenMeasurementArchive(t.TempDir())
	if _, err := a.Create(CreateRunInput{ID: "not-a-uuid", Name: "x", ModelName: "y", Section: "s", Carriage: "c"}); err == nil {
		t.Fatal("malformed run id accepted")
	}
}

func TestDrillRunPersistsAndAnalyzesSeparately(t *testing.T) {
	directory := t.TempDir()
	a, _ := OpenMeasurementArchive(directory)
	drill, err := a.Create(CreateRunInput{Name: "drill run", ModelName: "hull", Section: "s", Carriage: "c", Drill: true})
	if err != nil {
		t.Fatal(err)
	}
	live, err := a.Create(CreateRunInput{Name: "live run", ModelName: "hull", Section: "s", Carriage: "c"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UTC()
	if _, err := a.Record(drill.Operation.ID, MeasurementInput{ClientID: "d1", Kind: model.EvidenceForce, SourceID: "p", Value: 1, RecordedAt: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Record(live.Operation.ID, MeasurementInput{ClientID: "l1", Kind: model.EvidenceForce, SourceID: "p", Value: 2, RecordedAt: at}); err != nil {
		t.Fatal(err)
	}
	loaded, err := OpenMeasurementArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range loaded.List() {
		analysis, err := loaded.Analyze(run.Operation.ID)
		if err != nil {
			t.Fatal(err)
		}
		if analysis.Drill != run.Drill {
			t.Fatalf("drill flag lost in analysis for %s", run.Operation.Name)
		}
	}
}
