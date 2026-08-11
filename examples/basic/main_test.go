package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBasicExampleBuildsMountedRoute(t *testing.T) {
	app := newApp()
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/hello", nil)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/hello status = %d, want %d", response.Code, http.StatusOK)
	}
}
