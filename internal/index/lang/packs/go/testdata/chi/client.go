package main

// No chi import: a client whose methods share chi's names is not a router.
func fetch(c *apiClient) {
	c.Get("/users")
	c.Post("/users", nil)
}
