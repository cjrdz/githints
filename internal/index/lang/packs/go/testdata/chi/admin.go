package main

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// A completely separate router for administrator routes, mounted at /admin
// by main: its paths are reported as written here, without the mount point.
func adminRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(AdminOnly)
	r.Get("/", adminIndex)
	r.Get("/accounts", adminListAccounts)
	return r
}
