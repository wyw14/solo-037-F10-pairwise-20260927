package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wyw14/solo-037/internal/service"
)

func TestMeasuredRunHTTPUsesOperatorEvidence(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	created := httptest.NewRecorder()
	server.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(`{"name":"tow A","model_name":"hull A","section":"section-a","carriage":"carriage-1"}`)))
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var response struct {
		Data service.MeasuredRun `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	id := response.Data.Operation.ID
	if id == "" {
		t.Fatal("missing ID")
	}
	stamp := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	input := `{"client_id":"frame-a","kind":"wave_time","source_id":"probe-a","value":0.24,"recorded_at":"` + stamp + `"}`
	recorded := httptest.NewRecorder()
	server.ServeHTTP(recorded, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+string(id)+"/evidence", strings.NewReader(input)))
	if recorded.Code != http.StatusOK {
		t.Fatalf("record: %d %s", recorded.Code, recorded.Body)
	}
	analysis := httptest.NewRecorder()
	server.ServeHTTP(analysis, httptest.NewRequest(http.MethodGet, "/api/measured-runs/"+string(id)+"/analysis", nil))
	if analysis.Code != http.StatusOK || !strings.Contains(analysis.Body.String(), `"mean":0.24`) {
		t.Fatalf("analysis: %d %s", analysis.Code, analysis.Body)
	}
	reloaded, err := service.OpenRuntime(t.TempDir())
	if err != nil || len(reloaded.MeasuredRuns()) != 0 {
		t.Fatal("archive separation failed", err)
	}
}

func TestMeasurementHTTPIdempotentRetryReturns409WithReason(t *testing.T) {
	runtime, _ := service.OpenRuntime(t.TempDir())
	server := NewRouter(runtime).Handler()
	id := createRun(t, server, `{"name":"tow B","model_name":"hull","section":"section-a","carriage":"carriage-1","drill":true}`)

	stamp := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	frame := `{"client_id":"retry-1","kind":"wave_time","source_id":"probe-a","value":0.5,"recorded_at":"` + stamp + `"}`
	first := httptest.NewRecorder()
	server.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+id+"/evidence", strings.NewReader(frame)))
	if first.Code != http.StatusOK {
		t.Fatalf("first record: %d %s", first.Code, first.Body)
	}
	retry := httptest.NewRecorder()
	server.ServeHTTP(retry, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+id+"/evidence", strings.NewReader(frame)))
	if retry.Code != http.StatusConflict {
		t.Fatalf("retry: code = %d, want 409; body %s", retry.Code, retry.Body)
	}
	var envelope struct {
		Error struct {
			Reason   string `json:"reason"`
			Existing struct {
				ID       string  `json:"id"`
				ClientID string  `json:"client_id"`
				Value    float64 `json:"value"`
			} `json:"existing"`
		} `json:"error"`
	}
	if err := json.Unmarshal(retry.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Reason != "duplicate_frame" || envelope.Error.Existing.ClientID != "retry-1" || envelope.Error.Existing.Value != 0.5 {
		t.Fatalf("unexpected conflict payload: %s", retry.Body)
	}
}

func TestMeasurementHTTPBatchReportsMixedOutcomes(t *testing.T) {
	runtime, _ := service.OpenRuntime(t.TempDir())
	server := NewRouter(runtime).Handler()
	id := createRun(t, server, `{"name":"tow C","model_name":"hull","section":"section-a","carriage":"carriage-1"}`)
	stamp := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	body := `{"frames":[
	  {"client_id":"b1","kind":"wave_time","source_id":"probe-a","value":0.1,"recorded_at":"` + stamp + `"},
	  {"client_id":"b2","kind":"force","source_id":"load-cell","value":7,"recorded_at":"` + stamp + `"}
	]}`
	first := httptest.NewRecorder()
	server.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+id+"/evidence/batch", strings.NewReader(body)))
	if first.Code != http.StatusOK {
		t.Fatalf("batch: %d %s", first.Code, first.Body)
	}
	replay := httptest.NewRecorder()
	server.ServeHTTP(replay, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+id+"/evidence/batch", strings.NewReader(body)))
	if replay.Code != http.StatusOK {
		t.Fatalf("replay batch: %d %s", replay.Code, replay.Body)
	}
	var envelope struct {
		Data service.BatchResult `json:"data"`
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Accepted) != 0 || len(envelope.Data.Conflicts) != 2 {
		t.Fatalf("replayed batch should be all duplicate_frame conflicts: %+v", envelope.Data)
	}
	for _, c := range envelope.Data.Conflicts {
		if c.Reason != service.ConflictDuplicateFrame {
			t.Fatalf("unexpected replay conflict: %+v", c)
		}
	}
}

func TestCreateMeasuredRunHTTPClientIDIdempotent(t *testing.T) {
	runtime, _ := service.OpenRuntime(t.TempDir())
	server := NewRouter(runtime).Handler()
	body := `{"id":"11111111-1111-4111-8111-111111111111","name":"offline run","model_name":"hull","section":"section-a","carriage":"carriage-1","drill":true}`
	first := httptest.NewRecorder()
	server.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(body)))
	if first.Code != http.StatusOK {
		t.Fatalf("first create: %d %s", first.Code, first.Body)
	}
	again := httptest.NewRecorder()
	server.ServeHTTP(again, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(body)))
	if again.Code != http.StatusOK {
		t.Fatalf("idempotent create: %d %s", again.Code, again.Body)
	}
	mismatch := httptest.NewRecorder()
	server.ServeHTTP(mismatch, httptest.NewRequest(http.MethodPost, "/api/measured-runs",
		strings.NewReader(`{"id":"11111111-1111-4111-8111-111111111111","name":"changed","model_name":"hull","section":"section-a","carriage":"carriage-1"}`)))
	if mismatch.Code != http.StatusConflict || !strings.Contains(mismatch.Body.String(), "run_content_mismatch") {
		t.Fatalf("mismatch create: %d %s", mismatch.Code, mismatch.Body)
	}
}

func createRun(t *testing.T, server http.Handler, body string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("create run: %d %s", rec.Code, rec.Body)
	}
	var response struct {
		Data service.MeasuredRun `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return string(response.Data.Operation.ID)
}
