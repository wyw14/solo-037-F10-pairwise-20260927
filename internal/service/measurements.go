package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/probe"
)

// MeasuredRun contains observations supplied by an operator, unlike the
// deterministic diagnostic scenario used by the legacy equipment page.
type MeasuredRun struct {
	Operation model.Operation  `json:"operation"`
	Evidence  []model.Evidence `json:"evidence"`
}

// MeasurementInput is one reading frame. FrameID is the client-generated
// idempotency key that lets the archive recognise retransmitted frames after
// a network drop; frames without one are always appended (legacy behaviour).
type MeasurementInput struct {
	FrameID    string             `json:"frame_id"`
	Kind       model.EvidenceKind `json:"kind"`
	SourceID   string             `json:"source_id"`
	Value      float64            `json:"value"`
	RecordedAt time.Time          `json:"recorded_at"`
}

// FrameStatus is the per-frame outcome of a submission.
type FrameStatus string

const (
	FrameRecorded  FrameStatus = "recorded"
	FrameDuplicate FrameStatus = "duplicate"
	FrameConflict  FrameStatus = "conflict"
	FrameRejected  FrameStatus = "rejected"
)

// FrameResult reports what happened to one submitted frame. On duplicate and
// conflict outcomes Evidence holds the frame already stored, so the caller
// can compare it against what was sent.
type FrameResult struct {
	FrameID  string          `json:"frame_id"`
	Status   FrameStatus     `json:"status"`
	Reason   string          `json:"reason,omitempty"`
	Evidence *model.Evidence `json:"evidence,omitempty"`
}

// BatchResult summarises one confirmed upload batch.
type BatchResult struct {
	Run        model.RunID   `json:"run"`
	Results    []FrameResult `json:"results"`
	Recorded   int           `json:"recorded"`
	Duplicates int           `json:"duplicates"`
	Conflicts  int           `json:"conflicts"`
	Rejected   int           `json:"rejected"`
}

var ErrRunNotFound = errors.New("run not found")

const maxFrameIDLength = 128

type MeasurementAnalysis struct {
	Operation  model.Operation `json:"operation"`
	Wave       probe.Analysis  `json:"wave"`
	ForceCount int             `json:"force_count"`
	ForceMean  float64         `json:"force_mean"`
	Evaluated  time.Time       `json:"evaluated_at"`
}

type MeasurementArchive struct {
	mu   sync.Mutex
	path string
	runs map[model.RunID]MeasuredRun
}

func OpenMeasurementArchive(directory string) (*MeasurementArchive, error) {
	a := &MeasurementArchive{runs: make(map[model.RunID]MeasuredRun)}
	if directory == "" {
		return a, nil
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, err
	}
	a.path = filepath.Join(directory, "operator-measurements.json")
	data, err := os.ReadFile(a.path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &a.runs); err != nil {
		return nil, fmt.Errorf("read measurement archive: %w", err)
	}
	if a.runs == nil {
		return nil, errors.New("measurement archive must contain a run map")
	}
	// Everything in this archive is operator-supplied; mark runs archived
	// before the origin field existed so pages can keep drill data apart.
	for id, run := range a.runs {
		if run.Operation.Origin == "" {
			run.Operation.Origin = model.OriginMeasured
			a.runs[id] = run
		}
	}
	return a, nil
}

func cloneMeasuredRun(value MeasuredRun) MeasuredRun {
	value.Evidence = append([]model.Evidence(nil), value.Evidence...)
	return value
}

func (a *MeasurementArchive) commit(next map[model.RunID]MeasuredRun) error {
	if a.path != "" {
		data, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(a.path), "measurements-*.tmp")
		if err != nil {
			return err
		}
		name := file.Name()
		defer os.Remove(name)
		if _, err = file.Write(data); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Rename(name, a.path); err != nil {
			return err
		}
	}
	a.runs = next
	return nil
}

func (a *MeasurementArchive) Create(name, modelName, section, carriage string) (MeasuredRun, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(modelName) == "" || strings.TrimSpace(section) == "" || strings.TrimSpace(carriage) == "" {
		return MeasuredRun{}, errors.New("name, model, section and carriage are required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	operation := model.NewOperation(name, modelName, section, carriage, time.Now().UTC())
	operation.Origin = model.OriginMeasured
	value := MeasuredRun{Operation: operation, Evidence: []model.Evidence{}}
	next := make(map[model.RunID]MeasuredRun, len(a.runs)+1)
	for id, run := range a.runs {
		next[id] = run
	}
	next[operation.ID] = value
	if err := a.commit(next); err != nil {
		return MeasuredRun{}, err
	}
	return value, nil
}

func validateMeasurement(input MeasurementInput) error {
	if input.Kind != model.EvidenceWaveTime && input.Kind != model.EvidenceForce {
		return errors.New("measurement kind must be wave_time or force")
	}
	if len(input.FrameID) > maxFrameIDLength {
		return errors.New("frame id too long")
	}
	if strings.TrimSpace(input.SourceID) == "" || math.IsNaN(input.Value) || math.IsInf(input.Value, 0) || input.RecordedAt.IsZero() || input.RecordedAt.After(time.Now().Add(time.Minute)) {
		return errors.New("source, finite value and valid observation time are required")
	}
	return nil
}

// dedupeFrame reports whether the run already holds a frame with this
// idempotency key. An identical payload is a retry of an already committed
// frame; a different payload under the same key is a conflict and nothing is
// committed either way.
func dedupeFrame(run MeasuredRun, input MeasurementInput) (FrameResult, bool) {
	if input.FrameID == "" {
		return FrameResult{}, false
	}
	for i := range run.Evidence {
		existing := run.Evidence[i]
		if existing.FrameID != input.FrameID {
			continue
		}
		candidate := model.NewEvidence(run.Operation.ID, run.Operation.Revision, input.SourceID, input.Kind, input.Value, input.RecordedAt)
		if existing.SameFramePayload(candidate) {
			return FrameResult{FrameID: input.FrameID, Status: FrameDuplicate, Reason: "frame already recorded", Evidence: &existing}, true
		}
		reason := fmt.Sprintf("frame %s already recorded as %s %s=%g at %s", input.FrameID, existing.Kind, existing.SourceID, existing.Value, existing.RecordedAt.Format(time.RFC3339Nano))
		return FrameResult{FrameID: input.FrameID, Status: FrameConflict, Reason: reason, Evidence: &existing}, true
	}
	return FrameResult{}, false
}

func newFrameEvidence(run MeasuredRun, input MeasurementInput, durable bool) model.Evidence {
	return model.NewEvidence(run.Operation.ID, run.Operation.Revision, input.SourceID, input.Kind, input.Value, input.RecordedAt).
		WithDurability(durable).
		WithFrameID(input.FrameID)
}

func (a *MeasurementArchive) replaceRun(id model.RunID, run MeasuredRun) error {
	next := make(map[model.RunID]MeasuredRun, len(a.runs))
	for key, value := range a.runs {
		next[key] = value
	}
	next[id] = run
	return a.commit(next)
}

func (a *MeasurementArchive) Record(id model.RunID, input MeasurementInput) (FrameResult, error) {
	if err := validateMeasurement(input); err != nil {
		return FrameResult{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	run, exists := a.runs[id]
	if !exists {
		return FrameResult{}, ErrRunNotFound
	}
	if result, handled := dedupeFrame(run, input); handled {
		return result, nil
	}
	evidence := newFrameEvidence(run, input, a.path != "")
	run = cloneMeasuredRun(run)
	run.Evidence = append(run.Evidence, evidence)
	if err := a.replaceRun(id, run); err != nil {
		return FrameResult{}, err
	}
	return FrameResult{FrameID: input.FrameID, Status: FrameRecorded, Evidence: &evidence}, nil
}

// RecordBatch commits one confirmed upload batch. Every frame gets its own
// result so a dropped connection can be retried wholesale: retransmitted
// frames come back as duplicates, mismatched keys as conflicts, and invalid
// readings as rejections, without blocking the valid frames. All accepted
// frames are committed to the archive in a single write.
func (a *MeasurementArchive) RecordBatch(id model.RunID, inputs []MeasurementInput) (BatchResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run, exists := a.runs[id]
	if !exists {
		return BatchResult{}, ErrRunNotFound
	}
	working := cloneMeasuredRun(run)
	result := BatchResult{Run: id, Results: make([]FrameResult, 0, len(inputs))}
	for _, input := range inputs {
		if err := validateMeasurement(input); err != nil {
			result.Results = append(result.Results, FrameResult{FrameID: input.FrameID, Status: FrameRejected, Reason: err.Error()})
			result.Rejected++
			continue
		}
		if frame, handled := dedupeFrame(working, input); handled {
			result.Results = append(result.Results, frame)
			if frame.Status == FrameDuplicate {
				result.Duplicates++
			} else {
				result.Conflicts++
			}
			continue
		}
		evidence := newFrameEvidence(working, input, a.path != "")
		working.Evidence = append(working.Evidence, evidence)
		result.Results = append(result.Results, FrameResult{FrameID: input.FrameID, Status: FrameRecorded, Evidence: &evidence})
		result.Recorded++
	}
	if result.Recorded > 0 {
		if err := a.replaceRun(id, working); err != nil {
			return BatchResult{}, err
		}
	}
	return result, nil
}

func (a *MeasurementArchive) Get(id model.RunID) (MeasuredRun, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run, exists := a.runs[id]
	return cloneMeasuredRun(run), exists
}

func (a *MeasurementArchive) List() []MeasuredRun {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]MeasuredRun, 0, len(a.runs))
	for _, run := range a.runs {
		result = append(result, cloneMeasuredRun(run))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Operation.CreatedAt.Before(result[j].Operation.CreatedAt) })
	return result
}

func (a *MeasurementArchive) Analyze(id model.RunID) (MeasurementAnalysis, error) {
	run, exists := a.Get(id)
	if !exists {
		return MeasurementAnalysis{}, ErrRunNotFound
	}
	waves := make([]model.Evidence, 0)
	result := MeasurementAnalysis{Operation: run.Operation, Evaluated: time.Now().UTC()}
	for _, evidence := range run.Evidence {
		if evidence.Kind == model.EvidenceWaveTime {
			waves = append(waves, evidence)
		} else if evidence.Kind == model.EvidenceForce {
			result.ForceCount++
			result.ForceMean += evidence.Value
		}
	}
	result.Wave = probe.Analyze(waves)
	if result.ForceCount > 0 {
		result.ForceMean /= float64(result.ForceCount)
	}
	return result, nil
}

func (r *Runtime) CreateMeasuredRun(name, modelName, section, carriage string) (MeasuredRun, error) {
	return r.Measurements.Create(name, modelName, section, carriage)
}

func (r *Runtime) RecordMeasurement(id model.RunID, input MeasurementInput) (FrameResult, error) {
	return r.Measurements.Record(id, input)
}

func (r *Runtime) RecordMeasurementBatch(id model.RunID, inputs []MeasurementInput) (BatchResult, error) {
	return r.Measurements.RecordBatch(id, inputs)
}

func (r *Runtime) MeasuredRuns() []MeasuredRun { return r.Measurements.List() }

func (r *Runtime) MeasuredRun(id model.RunID) (MeasuredRun, bool) { return r.Measurements.Get(id) }

func (r *Runtime) AnalyzeMeasuredRun(id model.RunID) (MeasurementAnalysis, error) {
	return r.Measurements.Analyze(id)
}
