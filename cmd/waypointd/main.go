// waypointd is the Waypoint core daemon: config store, stack supervisor,
// hardware operations, and the REST/WebSocket API that serves the web UI.
//
// Phase 0: skeleton only — a health endpoint and version plumbing so the
// build, test, and release pipelines are real from the first commit.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"time"
)

// Version is stamped by the release build (-ldflags "-X main.Version=...").
var Version = "dev"

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Time    string `json:"time"`
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(healthResponse{
		Status:  "ok",
		Version: Version,
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8073", "listen address for the API and UI")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", healthHandler)

	log.Printf("waypointd %s listening on http://%s", Version, *addr)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
