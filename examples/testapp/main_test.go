package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestIndexCountsVisitsPersistently(t *testing.T) {
	dir := t.TempDir()
	h := newHandler("cost ${HOME} $5", dir)
	for want := 1; want <= 2; want++ {
		var got info
		if err := json.Unmarshal(get(t, h, "/").Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !got.Persistent || got.Visits != want || got.Greeting != "cost ${HOME} $5" {
			t.Fatalf("visit %d: %+v", want, got)
		}
	}
	// A new process (handler) continues from the stored counter.
	var got info
	json.Unmarshal(get(t, newHandler("x", dir), "/").Body.Bytes(), &got)
	if got.Visits != 3 {
		t.Fatalf("counter not persisted: %+v", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestIndexReportsUnwritableDataDir(t *testing.T) {
	var got info
	json.Unmarshal(get(t, newHandler("x", filepath.Join(t.TempDir(), "missing")), "/").Body.Bytes(), &got)
	if got.Persistent || got.DataError == "" {
		t.Fatalf("expected data error: %+v", got)
	}
}

func TestHealthzAndProbe(t *testing.T) {
	srv := httptest.NewServer(newHandler("x", t.TempDir()))
	defer srv.Close()
	if code := probe(srv.URL + "/healthz"); code != 0 {
		t.Fatalf("probe = %d", code)
	}
	if code := probe(srv.URL + "/nope"); code != 1 {
		t.Fatalf("probe of missing path = %d", code)
	}
}
