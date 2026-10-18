package main

import (
	"net/http"

	"github.com/gorilla/mux"
)

func router() *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc("/", home).Methods("GET")
	api := r.PathPrefix("/api").Subrouter()
	api.HandleFunc("/orders/{id}", func(w http.ResponseWriter, r *http.Request) {}).Methods("PATCH")
	api.Handle("/metrics", http.NotFoundHandler())
	return r
}

func home(w http.ResponseWriter, r *http.Request) {}
