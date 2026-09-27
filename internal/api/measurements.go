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
	CreateMeasuredRun(service.CreateRunInput) (service.MeasuredRun, error)
	RecordMeasurement(model.RunID, service.MeasurementInput) (model.Evidence, error)
	RecordMeasurements(model.RunID, []service.MeasurementInput) (service.BatchResult, error)
	MeasuredRuns() []service.MeasuredRun
	MeasuredRun(model.RunID) (service.MeasuredRun, bool)
	AnalyzeMeasuredRun(model.RunID) (service.MeasurementAnalysis, error)
}

type createMeasuredRunRequest struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ModelName string `json:"model_name"`
	Section   string `json:"section"`
	Carriage  string `json:"carriage"`
	Drill     bool   `json:"drill"`
}

type recordMeasurementsRequest struct {
	Frames []service.MeasurementInput `json:"frames"`
}

func decodeMeasurementBody(request *http.Request, value any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 4<<20))
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

// writeConflict answers retried or clashing submissions with a structured
// body the operator console can render as a reconcilable conflict instead of
// a transport failure to retry blindly.
func writeConflict(w http.ResponseWriter, conflict *service.ConflictError) {
	writeErrorJSON(w, http.StatusConflict, map[string]any{
		"reason":   conflict.Reason,
		"message":  conflict.Message,
		"existing": conflict.Existing,
	})
}

func writeErrorJSON(w http.ResponseWriter, status int, detail any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": detail})
}

func (r *Router) measuredRuns(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, r.dependencies.MeasuredRuns())
}

func (r *Router) createMeasuredRun(w http.ResponseWriter, request *http.Request) {
	var input createMeasuredRunRequest
	if err := decodeMeasurementBody(request, &input); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, map[string]any{"reason": service.ConflictInvalidFrame, "message": err.Error()})
		return
	}
	value, err := r.dependencies.CreateMeasuredRun(service.CreateRunInput{
		ID:        model.RunID(input.ID),
		Name:      input.Name,
		ModelName: input.ModelName,
		Section:   input.Section,
		Carriage:  input.Carriage,
		Drill:     input.Drill,
	})
	if err != nil {
		var conflict *service.ConflictError
		if errors.As(err, &conflict) {
			writeConflict(w, conflict)
			return
		}
		writeErrorJSON(w, http.StatusBadRequest, map[string]any{"reason": service.ConflictInvalidFrame, "message": err.Error()})
		return
	}
	writeJSON(w, value)
}

func (r *Router) measuredRun(w http.ResponseWriter, request *http.Request) {
	value, ok := r.dependencies.MeasuredRun(model.RunID(chi.URLParam(request, "id")))
	if !ok {
		writeErrorJSON(w, http.StatusNotFound, map[string]any{"reason": "run_not_found", "message": "run not found"})
		return
	}
	writeJSON(w, value)
}

func (r *Router) recordMeasurement(w http.ResponseWriter, request *http.Request) {
	var input service.MeasurementInput
	if err := decodeMeasurementBody(request, &input); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, map[string]any{"reason": service.ConflictInvalidFrame, "message": err.Error()})
		return
	}
	value, err := r.dependencies.RecordMeasurement(model.RunID(chi.URLParam(request, "id")), input)
	if err != nil {
		var conflict *service.ConflictError
		if errors.As(err, &conflict) {
			writeConflict(w, conflict)
			return
		}
		if err.Error() == "run not found" {
			writeErrorJSON(w, http.StatusNotFound, map[string]any{"reason": "run_not_found", "message": err.Error()})
			return
		}
		writeErrorJSON(w, http.StatusBadRequest, map[string]any{"reason": service.ConflictInvalidFrame, "message": err.Error()})
		return
	}
	writeJSON(w, value)
}

// recordMeasurements confirms a staged batch. The response is always 200 for
// an existing run: accepted frames are stored while duplicate and clashing
// frames are reported per index so the console can reconcile them locally.
func (r *Router) recordMeasurements(w http.ResponseWriter, request *http.Request) {
	var input recordMeasurementsRequest
	if err := decodeMeasurementBody(request, &input); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, map[string]any{"reason": service.ConflictInvalidFrame, "message": err.Error()})
		return
	}
	result, err := r.dependencies.RecordMeasurements(model.RunID(chi.URLParam(request, "id")), input.Frames)
	if err != nil {
		if err.Error() == "run not found" {
			writeErrorJSON(w, http.StatusNotFound, map[string]any{"reason": "run_not_found", "message": err.Error()})
			return
		}
		writeErrorJSON(w, http.StatusBadRequest, map[string]any{"reason": service.ConflictInvalidFrame, "message": err.Error()})
		return
	}
	writeJSON(w, result)
}

func (r *Router) measurementAnalysis(w http.ResponseWriter, request *http.Request) {
	value, err := r.dependencies.AnalyzeMeasuredRun(model.RunID(chi.URLParam(request, "id")))
	if err != nil {
		writeErrorJSON(w, http.StatusNotFound, map[string]any{"reason": "run_not_found", "message": err.Error()})
		return
	}
	writeJSON(w, value)
}
