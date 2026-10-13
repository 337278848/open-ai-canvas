package handler

import (
	"testing"

	"github.com/gin-gonic/gin"
	"yingce/backend/internal/service"
)

func TestCanvasHistoryRoutesRetainOperationsSync(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterUserDataRoutes(router.Group("/api"), &service.Service{})
	wanted := map[string]bool{
		"GET /api/canvas-projects/:id/history":                      false,
		"GET /api/canvas-projects/:id/history/:snapshotId":          false,
		"POST /api/canvas-projects/:id/history/:snapshotId/restore": false,
		"PATCH /api/canvas-projects/:id/operations":                 false,
	}
	for _, route := range router.Routes() {
		key := route.Method + " " + route.Path
		if key == "PUT /api/canvas-projects/:id" {
			t.Fatal("history restoration reintroduced full-document PUT sync")
		}
		if _, exists := wanted[key]; exists {
			wanted[key] = true
		}
	}
	for route, found := range wanted {
		if !found {
			t.Errorf("route %s is not registered", route)
		}
	}
}
