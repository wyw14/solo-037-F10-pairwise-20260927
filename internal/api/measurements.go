package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/wyw14/solo-037/internal/model"
	"github.com/wyw14/solo-037/internal/service"
)

type MeasurementService interface {
	CreateMeasuredRun(name, modelName, section, carriage string) (service.MeasuredRun, error)
	RecordMeasurement(model.RunID, service.MeasurementInput) (service.FrameResult, error)
	RecordMeasurementBatch(model.RunID, []service.MeasurementInput) (service.BatchResult, error)
	MeasuredRuns() []service.MeasuredRun
	MeasuredRun(model.RunID) (service.MeasuredRun, bool)
	AnalyzeMeasuredRun(model.RunID) (service.MeasurementAnalysis, error)
}

type createMeasuredRunRequest struct {
	Name      string `json:"name"`
	ModelName string `json:"model_name"`
	Section   string `json:"section"`
	Carriage  string `json:"carriage"`
}

// recordBatchRequest is one confirmed upload batch from the entry console.
// The bound keeps a reconnecting client from posting an unbounded backlog in
// a single request; the console chunks its queue below it.
type recordBatchRequest struct {
	Frames []service.MeasurementInput `json:"frames"`
}

const maxBatchFrames = 500

func decodeMeasurementBody(request *http.Request, value any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return io.ErrUnexpectedEOF
		}
		return err
	}
	return nil
}

func (r *Router) measuredRuns(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, r.dependencies.MeasuredRuns())
}

func (r *Router) createMeasuredRun(w http.ResponseWriter, request *http.Request) {
	var input createMeasuredRunRequest
	if err := decodeMeasurementBody(request, &input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	value, err := r.dependencies.CreateMeasuredRun(input.Name, input.ModelName, input.Section, input.Carriage)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, value)
}

func (r *Router) measuredRun(w http.ResponseWriter, request *http.Request) {
	value, ok := r.dependencies.MeasuredRun(model.RunID(chi.URLParam(request, "id")))
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	writeJSON(w, value)
}

func (r *Router) recordMeasurement(w http.ResponseWriter, request *http.Request) {
	var input service.MeasurementInput
	if err := decodeMeasurementBody(request, &input); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := r.dependencies.RecordMeasurement(model.RunID(chi.URLParam(request, "id")), input)
	if err != nil {
		writeMeasurementError(w, err)
		return
	}
	if result.Status == service.FrameConflict {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "conflict", "reason": result.Reason, "data": result})
		return
	}
	writeJSON(w, result)
}

func (r *Router) recordMeasurementBatch(w http.ResponseWriter, request *http.Request) {
	var input recordBatchRequest
	if err := decodeMeasurementBody(request, &input); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(input.Frames) == 0 {
		writeErrorJSON(w, http.StatusBadRequest, "frames must not be empty")
		return
	}
	if len(input.Frames) > maxBatchFrames {
		writeErrorJSON(w, http.StatusBadRequest, "batch too large")
		return
	}
	result, err := r.dependencies.RecordMeasurementBatch(model.RunID(chi.URLParam(request, "id")), input.Frames)
	if err != nil {
		writeMeasurementError(w, err)
		return
	}
	writeJSON(w, result)
}

func writeMeasurementError(w http.ResponseWriter, err error) {
	if errors.Is(err, service.ErrRunNotFound) {
		writeErrorJSON(w, http.StatusNotFound, err.Error())
		return
	}
	writeErrorJSON(w, http.StatusBadRequest, err.Error())
}

func writeErrorJSON(w http.ResponseWriter, code int, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "reason": reason})
}

func (r *Router) measurementAnalysis(w http.ResponseWriter, request *http.Request) {
	value, err := r.dependencies.AnalyzeMeasuredRun(model.RunID(chi.URLParam(request, "id")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, value)
}
