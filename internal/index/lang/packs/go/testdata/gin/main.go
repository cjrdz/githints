package main

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"example.com/shop/models"
)

func main() {
	r := gin.Default()
	r.Use(gin.Recovery())
	r.GET("/health", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	api := r.Group("/api")
	v1 := api.Group("/v1")
	v1.GET("/products/:id", getProduct)
	v1.POST("/products", createProduct)

	// net/http calls in a gin file are not reported a second time.
	http.HandleFunc("/debug", nil)
	_ = models.Product{}
}

func getProduct(c *gin.Context)    {}
func createProduct(c *gin.Context) {}
