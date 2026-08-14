// Package main implements the release-manager dev fixture: a minimal static
// file server with health and version endpoints. It is the sample workload
// installed into dev clusters to exercise the release pipeline (REQ-065).
package main

import (
	"flag"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("addr", ":8088", "HTTP listen address")
	staticDir := flag.String("dir", "/static", "static file directory served for all other paths")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"fixture-v2"}`))
	})
	mux.Handle("/", http.FileServer(http.Dir(*staticDir)))

	log.Printf("fixture listening on %s, serving %s", *addr, *staticDir)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}
