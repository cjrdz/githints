package main

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// The REST example from chi's README, as written there.
func main() {
	r := chi.NewRouter()

	// A good base middleware stack
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Set a timeout value on the request context (ctx), that will signal
	// through ctx.Done() that the request has timed out and further
	// processing should be stopped.
	r.Use(middleware.Timeout(60 * time.Second))

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hi"))
	})

	// RESTy routes for "articles" resource
	r.Route("/articles", func(r chi.Router) {
		r.With(paginate).Get("/", listArticles)                           // GET /articles
		r.With(paginate).Get("/{month}-{day}-{year}", listArticlesByDate) // GET /articles/01-16-2017

		r.Post("/", createArticle)       // POST /articles
		r.Get("/search", searchArticles) // GET /articles/search

		// Regexp url parameters:
		r.Get("/{articleSlug:[a-z-]+}", getArticleBySlug) // GET /articles/home-is-toronto

		// Subrouters:
		r.Route("/{articleID}", func(r chi.Router) {
			r.Use(ArticleCtx)
			r.Get("/", getArticle)       // GET /articles/123
			r.Put("/", updateArticle)    // PUT /articles/123
			r.Delete("/", deleteArticle) // DELETE /articles/123
		})
	})

	// Mount the admin sub-router
	r.Mount("/admin", adminRouter())

	http.ListenAndServe(":3333", r)
}

// The rest of the Router interface the README lists.
func more(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(AdminOnly)
		r.Patch("/profile", updateProfile)
	})
	r.Method("GET", "/health", healthHandler)
	r.MethodFunc(http.MethodPost, "/hooks/{name}", hook)
	r.Handle("/static/*", http.FileServer(http.Dir("public")))
	r.HandleFunc("/ping", ping)
	r.Head("/status", status)
	r.Options("/cors", cors)
	r.Query("/search", queryArticles)
	r.Route("/reports", reportRoutes)
	r.NotFound(notFound)
	r.MethodNotAllowed(notAllowed)
}

// Calls shaped like routes that are not: headers, query values, an HTTP
// client, a string mentioning a route.
func articleHandler(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("Authorization")
	page := r.URL.Query().Get("page")
	resp, _ := http.Get("https://example.com/feed")
	doc := `r.Get("/in-a-string", h)`
	_, _, _, _ = token, page, resp, doc
}

func reportRoutes(r chi.Router) {}
