// Package main implements the release-manager dev fixture: a minimal static
// file server with health and version endpoints. It is the sample workload
// installed into dev clusters to exercise the release pipeline (REQ-065).
package main

import (
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", ":8088", "HTTP listen address")
	staticDir := flag.String("dir", "/static", "static file directory served for all other paths")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"status":"ok"}`)); err != nil {
			log.Printf("health write error: %v", err)
		}
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"version":"fixture-v2"}`)); err != nil {
			log.Printf("version write error: %v", err)
		}
	})
	mux.Handle("/", http.FileServer(http.Dir(*staticDir)))

	log.Printf("fixture listening on %s, serving %s", *addr, *staticDir)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
