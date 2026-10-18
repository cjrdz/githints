package main

import (
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func main() {
	e := echo.New()
	e.Use(middleware.Logger())
	e.GET("/", home)
	admin := e.Group("/admin")
	admin.DELETE("/posts/:id", deletePost)
	e.Logger.Fatal(e.Start(":1323"))
}

func home(c echo.Context) error       { return nil }
func deletePost(c echo.Context) error { return nil }
