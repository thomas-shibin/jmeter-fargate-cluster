package main

import (
	"github.com/BenchmarkManager/core/internal"
	"github.com/gorilla/mux"
	"log"
	"net/http"
	"time"
)

// Route declaration
func router(handler *internal.RequestHandler) *mux.Router {

	r := mux.NewRouter()
	r.HandleFunc("/cluster/create", handler.CreateCluster)
	r.HandleFunc("/cluster/test/start", handler.StartTest)
	r.HandleFunc("/cluster/download/result", handler.DownloadResults)
	r.HandleFunc("/cluster/prometheus/memory", handler.GetSystemPerformanceReport)
	r.HandleFunc("/cluster/clear", handler.ClearCluster)
	return r
}

func main() {
	handler := internal.NewRequestHandler()
	handler.CleanupScheduler()

	router := router(handler)
	srv := &http.Server{
		Handler:      router,
		Addr:         ":9080",
		WriteTimeout: 10 * time.Minute,
		ReadTimeout:  10 * time.Minute,
	}

	log.Fatal(srv.ListenAndServe())
}


