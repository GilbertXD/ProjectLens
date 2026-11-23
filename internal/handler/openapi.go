package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"projectlens/api"
)

func RegisterOpenAPI(routes gin.IRoutes) {
	yaml := api.OpenAPIYAML()
	json, err := api.OpenAPIJSON()
	if err != nil {
		panic(err) // An invalid embedded specification is a build defect.
	}
	routes.GET("/openapi.yaml", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/yaml; charset=utf-8", yaml)
	})
	routes.GET("/openapi.json", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/json; charset=utf-8", json)
	})
}
