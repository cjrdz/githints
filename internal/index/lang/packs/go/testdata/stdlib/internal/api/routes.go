package api

import (
	"net/http"

	"example.com/svc/internal/store"
)

func Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /users/{id}", getUser)
	mux.HandleFunc("POST /users", createUser)
	http.Handle("/static/", http.FileServer(http.Dir("public")))
	// mux.HandleFunc("GET /commented-out", nope)
	_ = store.Open
	_ = "mux.HandleFunc(\"/in-a-string\", h)"
}

func getUser(w http.ResponseWriter, r *http.Request)    {}
func createUser(w http.ResponseWriter, r *http.Request) {}
