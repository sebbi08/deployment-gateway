// Command testapp is a tiny HTTP service for trying out deploy-gateway.
//
// It exercises what the gateway enforces: it runs as a non-root user on a
// read-only root filesystem without capabilities, keeps a visit counter in a
// persistent volume (DATA_DIR), echoes GREETING literally, and ships its own
// healthcheck (`/testapp -healthcheck`) because the image has no shell, curl
// or wget.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var version = "dev"

func main() {
	healthcheck := flag.Bool("healthcheck", false, "probe /healthz on PORT and exit 0 if healthy")
	flag.Parse()
	port := envOr("PORT", "8080")
	if *healthcheck {
		os.Exit(probe("http://127.0.0.1:" + port + "/healthz"))
	}
	h := newHandler(envOr("GREETING", "hello from the deploy-gateway test app"), envOr("DATA_DIR", "/data"))
	srv := &http.Server{Addr: ":" + port, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	log.Printf("testapp %s listening on :%s (data dir %s)", version, port, h.dataDir)
	log.Fatal(srv.ListenAndServe())
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func probe(url string) int {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

type handler struct {
	mux      *http.ServeMux
	greeting string
	dataDir  string
	mu       sync.Mutex
}

type info struct {
	App        string `json:"app"`
	Version    string `json:"version"`
	Greeting   string `json:"greeting"`
	Hostname   string `json:"hostname"`
	Visits     int    `json:"visits"`
	Persistent bool   `json:"persistent"`
	DataError  string `json:"data_error,omitempty"`
}

func newHandler(greeting, dataDir string) *handler {
	h := &handler{mux: http.NewServeMux(), greeting: greeting, dataDir: dataDir}
	h.mux.HandleFunc("GET /{$}", h.index)
	h.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func (h *handler) index(w http.ResponseWriter, _ *http.Request) {
	host, _ := os.Hostname()
	res := info{App: "testapp", Version: version, Greeting: h.greeting, Hostname: host}
	visits, err := h.countVisit()
	if err != nil {
		res.DataError = err.Error()
	} else {
		res.Visits, res.Persistent = visits, true
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(res)
}

// countVisit increments the counter in DATA_DIR/visits atomically.
func (h *handler) countVisit() (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	path := filepath.Join(h.dataDir, "visits")
	n := 0
	if b, err := os.ReadFile(path); err == nil {
		n, _ = strconv.Atoi(strings.TrimSpace(string(b)))
	} else if !os.IsNotExist(err) {
		return 0, err
	}
	n++
	tmp, err := os.CreateTemp(h.dataDir, ".visits-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(strconv.Itoa(n) + "\n"); err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	return n, os.Rename(tmp.Name(), path)
}
