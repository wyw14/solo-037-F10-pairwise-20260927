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

	"github.com/google/uuid"

	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/probe"
)

// MeasuredRun contains observations supplied by an operator, unlike the
// deterministic diagnostic scenario used by the legacy equipment page.
// Drill marks rehearsal data so drill runs can always be told apart from
// live measurement runs on the console and in the archive.
type MeasuredRun struct {
	Operation model.Operation  `json:"operation"`
	Drill     bool             `json:"drill"`
	Evidence  []model.Evidence `json:"evidence"`
}

// CreateRunInput carries both the operator metadata and the optional
// client-supplied run identifier the console reserves while offline.
type CreateRunInput struct {
	Name      string
	ModelName string
	Section   string
	Carriage  string
	ID        model.RunID
	Drill     bool
}

type MeasurementInput struct {
	ClientID   string             `json:"client_id"`
	Kind       model.EvidenceKind `json:"kind"`
	SourceID   string             `json:"source_id"`
	Value      float64            `json:"value"`
	RecordedAt time.Time          `json:"recorded_at"`
}

type MeasurementAnalysis struct {
	Operation  model.Operation `json:"operation"`
	Drill      bool            `json:"drill"`
	Wave       probe.Analysis  `json:"wave"`
	ForceCount int             `json:"force_count"`
	ForceMean  float64         `json:"force_mean"`
	Evaluated  time.Time       `json:"evaluated_at"`
}

// ConflictReason explains why the server refused to store a frame as new.
type ConflictReason string

const (
	// ConflictDuplicateFrame is an idempotent retry: the same client frame ID
	// was already accepted with identical content.
	ConflictDuplicateFrame ConflictReason = "duplicate_frame"
	// ConflictFrameContentMismatch means a client frame ID was reused for a
	// frame whose readings no longer match the ones already stored under it.
	ConflictFrameContentMismatch ConflictReason = "frame_content_mismatch"
	// ConflictDuplicateObservation means the same probe/source/kind/timestamp
	// slot already holds a reading submitted under a different frame ID.
	ConflictDuplicateObservation ConflictReason = "duplicate_observation"
	// ConflictInvalidFrame means the frame payload failed validation.
	ConflictInvalidFrame ConflictReason = "invalid_frame"
	// ConflictRunContentMismatch means a run ID created offline was confirmed
	// with metadata that does not match the run already stored under that ID.
	ConflictRunContentMismatch ConflictReason = "run_content_mismatch"
)

// ConflictError lets the HTTP layer answer retried or clashing frames with a
// structured 409 response instead of a generic bad-request message.
type ConflictError struct {
	Reason   ConflictReason
	Message  string
	Existing any
}

func (e *ConflictError) Error() string {
	if e.Message != "" {
		return string(e.Reason) + ": " + e.Message
	}
	return string(e.Reason)
}

// FrameConflict reports the per-frame outcome inside a batch upload.
type FrameConflict struct {
	Index    int             `json:"index"`
	ClientID string          `json:"client_id,omitempty"`
	Reason   ConflictReason  `json:"reason"`
	Message  string          `json:"message"`
	Existing *model.Evidence `json:"existing,omitempty"`
}

// BatchResult reports which staged frames were accepted and which clashed,
// so a batch can partially succeed without the client losing the reasons.
type BatchResult struct {
	Run       model.RunID      `json:"run"`
	Drill     bool             `json:"drill"`
	Accepted  []model.Evidence `json:"accepted"`
	Conflicts []FrameConflict  `json:"conflicts"`
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

func validateRunMetadata(input CreateRunInput) error {
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.ModelName) == "" || strings.TrimSpace(input.Section) == "" || strings.TrimSpace(input.Carriage) == "" {
		return errors.New("name, model, section and carriage are required")
	}
	if input.ID != "" {
		if _, err := uuid.Parse(string(input.ID)); err != nil {
			return fmt.Errorf("run id must be a UUID: %w", err)
		}
	}
	return nil
}

func validateMeasurementInput(input MeasurementInput) error {
	if input.Kind != model.EvidenceWaveTime && input.Kind != model.EvidenceForce {
		return errors.New("measurement kind must be wave_time or force")
	}
	clientID := strings.TrimSpace(input.ClientID)
	if clientID != "" && (len(clientID) > 128 || strings.ContainsAny(clientID, " \t\r\n")) {
		return errors.New("client frame id must be 1-128 characters without whitespace")
	}
	if strings.TrimSpace(input.SourceID) == "" || math.IsNaN(input.Value) || math.IsInf(input.Value, 0) || input.RecordedAt.IsZero() || input.RecordedAt.After(time.Now().Add(time.Minute)) {
		return errors.New("source, finite value and valid observation time are required")
	}
	return nil
}

func framesEqual(evidence model.Evidence, input MeasurementInput) bool {
	return evidence.Kind == input.Kind &&
		evidence.SourceID == input.SourceID &&
		evidence.Value == input.Value &&
		evidence.RecordedAt.Equal(input.RecordedAt.UTC())
}

// classifyFrame checks a frame against the frames already visible for the run
// (committed plus accepted earlier in the same batch) and either builds new
// evidence or describes the conflict.
func classifyFrame(existing []model.Evidence, run model.RunID, revision model.RevisionID, input MeasurementInput) (model.Evidence, *FrameConflict) {
	clientID := strings.TrimSpace(input.ClientID)
	for _, evidence := range existing {
		if clientID != "" && evidence.ClientID == clientID {
			if framesEqual(evidence, input) {
				return evidence, &FrameConflict{
					ClientID: clientID,
					Reason:   ConflictDuplicateFrame,
					Message:  "frame already accepted; treated as idempotent retry",
					Existing: &evidence,
				}
			}
			return evidence, &FrameConflict{
				ClientID: clientID,
				Reason:   ConflictFrameContentMismatch,
				Message:  "frame id was already submitted with different kind, source, value or observation time",
				Existing: &evidence,
			}
		}
		if evidence.SourceID == input.SourceID &&
			evidence.Kind == input.Kind &&
			evidence.RecordedAt.Equal(input.RecordedAt.UTC()) {
			return evidence, &FrameConflict{
				ClientID: clientID,
				Reason:   ConflictDuplicateObservation,
				Message:  "the same probe, kind and observation time were already recorded under another frame id",
				Existing: &evidence,
			}
		}
	}
	evidence := model.NewEvidence(run, revision, input.SourceID, input.Kind, input.Value, input.RecordedAt).
		WithClientID(clientID)
	return evidence, nil
}

func (a *MeasurementArchive) Create(input CreateRunInput) (MeasuredRun, error) {
	if err := validateRunMetadata(input); err != nil {
		return MeasuredRun{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if input.ID != "" {
		if prior, exists := a.runs[input.ID]; exists {
			if prior.Operation.Name == input.Name && prior.Operation.ModelName == input.ModelName &&
				prior.Operation.Section == input.Section && prior.Operation.Carriage == input.Carriage &&
				prior.Drill == input.Drill {
				return prior, nil
			}
			return MeasuredRun{}, &ConflictError{
				Reason:   ConflictRunContentMismatch,
				Message:  "run id already exists with different name, model, section, carriage or drill flag",
				Existing: prior,
			}
		}
	}
	var operation model.Operation
	if input.ID != "" {
		operation = model.NewOperation(input.Name, input.ModelName, input.Section, input.Carriage, time.Now().UTC())
		operation.ID = input.ID
	} else {
		operation = model.NewOperation(input.Name, input.ModelName, input.Section, input.Carriage, time.Now().UTC())
	}
	value := MeasuredRun{Operation: operation, Drill: input.Drill, Evidence: []model.Evidence{}}
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

func (a *MeasurementArchive) Record(id model.RunID, input MeasurementInput) (model.Evidence, error) {
	if err := validateMeasurementInput(input); err != nil {
		return model.Evidence{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	run, exists := a.runs[id]
	if !exists {
		return model.Evidence{}, errors.New("run not found")
	}
	evidence, conflict := classifyFrame(run.Evidence, id, run.Operation.Revision, input)
	if conflict != nil {
		return evidence, &ConflictError{
			Reason:   conflict.Reason,
			Message:  conflict.Message,
			Existing: conflict.Existing,
		}
	}
	evidence = evidence.WithDurability(a.path != "")
	run = cloneMeasuredRun(run)
	run.Evidence = append(run.Evidence, evidence)
	next := make(map[model.RunID]MeasuredRun, len(a.runs))
	for key, value := range a.runs {
		next[key] = value
	}
	next[id] = run
	if err := a.commit(next); err != nil {
		return model.Evidence{}, err
	}
	return evidence, nil
}

// RecordBatch accepts a batch of frames staged by the operator console. Valid
// frames are committed together; retried, clashing and invalid frames come
// back as per-frame conflicts without poisoning the rest of the batch.
func (a *MeasurementArchive) RecordBatch(id model.RunID, inputs []MeasurementInput) (BatchResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	run, exists := a.runs[id]
	if !exists {
		return BatchResult{}, errors.New("run not found")
	}
	result := BatchResult{Run: id, Drill: run.Drill, Accepted: []model.Evidence{}, Conflicts: []FrameConflict{}}
	visible := append([]model.Evidence(nil), run.Evidence...)
	for index, input := range inputs {
		if err := validateMeasurementInput(input); err != nil {
			result.Conflicts = append(result.Conflicts, FrameConflict{
				Index:    index,
				ClientID: strings.TrimSpace(input.ClientID),
				Reason:   ConflictInvalidFrame,
				Message:  err.Error(),
			})
			continue
		}
		evidence, conflict := classifyFrame(visible, id, run.Operation.Revision, input)
		if conflict != nil {
			conflict.Index = index
			result.Conflicts = append(result.Conflicts, *conflict)
			continue
		}
		evidence = evidence.WithDurability(a.path != "")
		visible = append(visible, evidence)
		result.Accepted = append(result.Accepted, evidence)
	}
	if len(result.Accepted) > 0 {
		updated := cloneMeasuredRun(run)
		updated.Evidence = visible
		next := make(map[model.RunID]MeasuredRun, len(a.runs))
		for key, value := range a.runs {
			next[key] = value
		}
		next[id] = updated
		if err := a.commit(next); err != nil {
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
		return MeasurementAnalysis{}, errors.New("run not found")
	}
	waves := make([]model.Evidence, 0)
	result := MeasurementAnalysis{Operation: run.Operation, Drill: run.Drill, Evaluated: time.Now().UTC()}
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

func (r *Runtime) CreateMeasuredRun(input CreateRunInput) (MeasuredRun, error) {
	return r.Measurements.Create(input)
}

func (r *Runtime) RecordMeasurement(id model.RunID, input MeasurementInput) (model.Evidence, error) {
	return r.Measurements.Record(id, input)
}

func (r *Runtime) RecordMeasurements(id model.RunID, inputs []MeasurementInput) (BatchResult, error) {
	return r.Measurements.RecordBatch(id, inputs)
}

func (r *Runtime) MeasuredRuns() []MeasuredRun { return r.Measurements.List() }

func (r *Runtime) MeasuredRun(id model.RunID) (MeasuredRun, bool) { return r.Measurements.Get(id) }

func (r *Runtime) AnalyzeMeasuredRun(id model.RunID) (MeasurementAnalysis, error) {
	return r.Measurements.Analyze(id)
}
