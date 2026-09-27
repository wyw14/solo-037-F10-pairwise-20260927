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
	run, err := a.Create("test A", "hull X", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UTC()
	for _, input := range []MeasurementInput{
		{Kind: model.EvidenceWaveTime, SourceID: "left", Value: 0.1, RecordedAt: at},
		{Kind: model.EvidenceWaveTime, SourceID: "right", Value: 0.3, RecordedAt: at.Add(time.Second)},
		{Kind: model.EvidenceForce, SourceID: "load-cell", Value: 12, RecordedAt: at},
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
	if len(loaded.List()) != 1 || len(loaded.List()[0].Evidence) != 3 || !loaded.List()[0].Evidence[0].Durable {
		t.Fatalf("persisted evidence missing: %+v", loaded.List())
	}
}

func TestOperatorMeasurementRejectsInvalidAndUnknownRun(t *testing.T) {
	a, err := OpenMeasurementArchive(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create("test B", "hull X", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	input := MeasurementInput{Kind: model.EvidenceWaveTime, SourceID: "left", Value: 1, RecordedAt: time.Now().UTC()}
	if _, err := a.Record("missing", input); !errors.Is(err, ErrRunNotFound) {
		t.Fatal("unknown run accepted", err)
	}
	input.Value = math.NaN()
	if _, err := a.Record(run.Operation.ID, input); err == nil {
		t.Fatal("NaN accepted")
	}
	if len(a.List()[0].Evidence) != 0 {
		t.Fatal("rejected values changed archive")
	}
}

func TestOperatorMeasurementDeduplicatesRetryFrames(t *testing.T) {
	directory := t.TempDir()
	a, err := OpenMeasurementArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create("test C", "hull X", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UTC()
	frame := MeasurementInput{FrameID: "frame-1", Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 0.24, RecordedAt: at}
	first, err := a.Record(run.Operation.ID, frame)
	if err != nil || first.Status != FrameRecorded {
		t.Fatalf("first record: %+v %v", first, err)
	}
	retry, err := a.Record(run.Operation.ID, frame)
	if err != nil || retry.Status != FrameDuplicate {
		t.Fatalf("retry should be a duplicate: %+v %v", retry, err)
	}
	if retry.Evidence == nil || first.Evidence == nil || retry.Evidence.ID != first.Evidence.ID {
		t.Fatalf("duplicate must return the originally stored frame: %+v vs %+v", retry.Evidence, first.Evidence)
	}
	changed := frame
	changed.Value = 0.31
	conflict, err := a.Record(run.Operation.ID, changed)
	if err != nil || conflict.Status != FrameConflict || conflict.Reason == "" {
		t.Fatalf("changed payload must conflict with a reason: %+v %v", conflict, err)
	}
	if conflict.Evidence == nil || conflict.Evidence.Value != 0.24 {
		t.Fatalf("conflict must expose the stored frame: %+v", conflict.Evidence)
	}
	if got := len(a.List()[0].Evidence); got != 1 {
		t.Fatalf("duplicate/conflict frames must not be committed, got %d", got)
	}
	reloaded, err := OpenMeasurementArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reloaded.Record(run.Operation.ID, frame)
	if err != nil || again.Status != FrameDuplicate {
		t.Fatalf("retry after reload should still be a duplicate: %+v %v", again, err)
	}
}

func TestOperatorMeasurementBatchResults(t *testing.T) {
	directory := t.TempDir()
	a, err := OpenMeasurementArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	run, err := a.Create("test D", "hull X", "section-a", "carriage-1")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute).UTC()
	if _, err := a.Record(run.Operation.ID, MeasurementInput{FrameID: "f-1", Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 0.2, RecordedAt: at}); err != nil {
		t.Fatal(err)
	}
	batch := []MeasurementInput{
		{FrameID: "f-2", Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 0.3, RecordedAt: at.Add(time.Second)},
		{FrameID: "f-1", Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 0.2, RecordedAt: at},
		{FrameID: "f-1", Kind: model.EvidenceWaveTime, SourceID: "probe-a", Value: 9.9, RecordedAt: at},
		{FrameID: "f-3", Kind: model.EvidenceForce, SourceID: "load-cell", Value: math.NaN(), RecordedAt: at},
		{Kind: model.EvidenceForce, SourceID: "load-cell", Value: 11, RecordedAt: at},
	}
	result, err := a.RecordBatch(run.Operation.ID, batch)
	if err != nil {
		t.Fatal(err)
	}
	want := []FrameStatus{FrameRecorded, FrameDuplicate, FrameConflict, FrameRejected, FrameRecorded}
	if len(result.Results) != len(want) {
		t.Fatalf("missing per-frame results: %+v", result)
	}
	for i, status := range want {
		if result.Results[i].Status != status {
			t.Fatalf("frame %d: got %s want %s (%+v)", i, result.Results[i].Status, status, result)
		}
	}
	if result.Recorded != 2 || result.Duplicates != 1 || result.Conflicts != 1 || result.Rejected != 1 {
		t.Fatalf("batch counts wrong: %+v", result)
	}
	if result.Results[2].Reason == "" || result.Results[3].Reason == "" {
		t.Fatalf("conflict and rejection must carry reasons: %+v", result.Results)
	}
	reloaded, err := OpenMeasurementArchive(directory)
	if err != nil {
		t.Fatal(err)
	}
	loaded, ok := reloaded.Get(run.Operation.ID)
	if !ok || len(loaded.Evidence) != 3 {
		t.Fatalf("batch commit did not persist exactly the accepted frames: %+v", loaded)
	}
	if loaded.Evidence[1].FrameID != "f-2" {
		t.Fatalf("frame id not persisted: %+v", loaded.Evidence[1])
	}
	if _, err := a.RecordBatch("missing", batch); !errors.Is(err, ErrRunNotFound) {
		t.Fatal("batch against unknown run accepted", err)
	}
}
