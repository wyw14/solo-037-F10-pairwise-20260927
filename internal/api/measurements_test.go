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
	input := `{"kind":"wave_time","source_id":"probe-a","value":0.24,"recorded_at":"` + stamp + `"}`
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

func TestMeasuredRunBatchUploadIdentifiesRetriesAndConflicts(t *testing.T) {
	runtime, err := service.OpenRuntime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := NewRouter(runtime).Handler()
	created := httptest.NewRecorder()
	server.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/api/measured-runs", strings.NewReader(`{"name":"tow B","model_name":"hull B","section":"section-a","carriage":"carriage-1"}`)))
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	var run struct {
		Data service.MeasuredRun `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	id := string(run.Data.Operation.ID)
	stamp := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	batch := `{"frames":[` +
		`{"frame_id":"f-1","kind":"wave_time","source_id":"probe-a","value":0.2,"recorded_at":"` + stamp + `"},` +
		`{"frame_id":"f-2","kind":"force","source_id":"load-cell","value":12,"recorded_at":"` + stamp + `"}` +
		`]}`
	first := httptest.NewRecorder()
	server.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+id+"/evidence/batch", strings.NewReader(batch)))
	if first.Code != http.StatusOK {
		t.Fatalf("first batch: %d %s", first.Code, first.Body)
	}
	var firstResult struct {
		Data service.BatchResult `json:"data"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &firstResult); err != nil {
		t.Fatal(err)
	}
	if firstResult.Data.Recorded != 2 || len(firstResult.Data.Results) != 2 {
		t.Fatalf("first batch not fully recorded: %+v", firstResult.Data)
	}
	// 断网重传:同帧号同读数算重复帧,同帧号不同读数算冲突并给出原因。
	retry := `{"frames":[` +
		`{"frame_id":"f-1","kind":"wave_time","source_id":"probe-a","value":0.2,"recorded_at":"` + stamp + `"},` +
		`{"frame_id":"f-2","kind":"force","source_id":"load-cell","value":99,"recorded_at":"` + stamp + `"},` +
		`{"frame_id":"f-3","kind":"wave_time","source_id":"probe-b","value":0.4,"recorded_at":"` + stamp + `"}` +
		`]}`
	second := httptest.NewRecorder()
	server.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+id+"/evidence/batch", strings.NewReader(retry)))
	if second.Code != http.StatusOK {
		t.Fatalf("retry batch: %d %s", second.Code, second.Body)
	}
	var secondResult struct {
		Data service.BatchResult `json:"data"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondResult); err != nil {
		t.Fatal(err)
	}
	got := secondResult.Data
	if got.Recorded != 1 || got.Duplicates != 1 || got.Conflicts != 1 {
		t.Fatalf("retry batch counts: %+v", got)
	}
	statuses := []service.FrameStatus{got.Results[0].Status, got.Results[1].Status, got.Results[2].Status}
	want := []service.FrameStatus{service.FrameDuplicate, service.FrameConflict, service.FrameRecorded}
	for i := range want {
		if statuses[i] != want[i] {
			t.Fatalf("frame %d: got %s want %s (%+v)", i, statuses[i], want[i], got)
		}
	}
	if got.Results[1].Reason == "" {
		t.Fatalf("conflict must carry a reason: %+v", got.Results[1])
	}
	// 单帧冲突走 409,响应里带冲突原因。
	single := httptest.NewRecorder()
	server.ServeHTTP(single, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+id+"/evidence", strings.NewReader(
		`{"frame_id":"f-1","kind":"wave_time","source_id":"probe-a","value":7.7,"recorded_at":"`+stamp+`"}`)))
	if single.Code != http.StatusConflict {
		t.Fatalf("single conflict: %d %s", single.Code, single.Body)
	}
	var conflictBody struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(single.Body.Bytes(), &conflictBody); err != nil {
		t.Fatal(err)
	}
	if conflictBody.Status != "conflict" || conflictBody.Reason == "" {
		t.Fatalf("conflict response missing reason: %s", single.Body)
	}
	// 单帧重试是幂等成功。
	duplicate := httptest.NewRecorder()
	server.ServeHTTP(duplicate, httptest.NewRequest(http.MethodPost, "/api/measured-runs/"+id+"/evidence", strings.NewReader(
		`{"frame_id":"f-1","kind":"wave_time","source_id":"probe-a","value":0.2,"recorded_at":"`+stamp+`"}`)))
	if duplicate.Code != http.StatusOK || !strings.Contains(duplicate.Body.String(), `"duplicate"`) {
		t.Fatalf("single retry: %d %s", duplicate.Code, duplicate.Body)
	}
	// 服务器只持有 3 帧:重试与冲突均未落库。
	loaded := httptest.NewRecorder()
	server.ServeHTTP(loaded, httptest.NewRequest(http.MethodGet, "/api/measured-runs/"+id, nil))
	if loaded.Code != http.StatusOK {
		t.Fatalf("get run: %d", loaded.Code)
	}
	var finalRun struct {
		Data service.MeasuredRun `json:"data"`
	}
	if err := json.Unmarshal(loaded.Body.Bytes(), &finalRun); err != nil {
		t.Fatal(err)
	}
	if len(finalRun.Data.Evidence) != 3 {
		t.Fatalf("server must hold exactly the accepted frames: %+v", finalRun.Data.Evidence)
	}
	for _, evidence := range finalRun.Data.Evidence {
		if evidence.FrameID == "" {
			t.Fatalf("stored frame lost its idempotency key: %+v", evidence)
		}
	}
	unknown := httptest.NewRecorder()
	server.ServeHTTP(unknown, httptest.NewRequest(http.MethodPost, "/api/measured-runs/nope/evidence/batch", strings.NewReader(batch)))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown run batch: %d %s", unknown.Code, unknown.Body)
	}
}
