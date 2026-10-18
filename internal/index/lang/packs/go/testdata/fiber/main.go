package main

import "github.com/gofiber/fiber/v2"

func main() {
	app := fiber.New()
	app.Get("/", func(c *fiber.Ctx) error { return c.SendString("hi") })
	api := app.Group("/api")
	api.Put("/items/:id", updateItem)
	app.Listen(":3000")
}

func updateItem(c *fiber.Ctx) error { return nil }
