package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthExampleReturnsServiceUnavailableWhenRevocationStorageIsMissing(t *testing.T) {
	app := newApp()
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/logout", strings.NewReader(`{"token":"token-to-revoke"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /logout status = %d body = %s, want %d", response.Code, response.Body.String(), http.StatusServiceUnavailable)
	}
	if !strings.Contains(response.Body.String(), "token revocation is unavailable") {
		t.Fatalf("POST /logout body = %s, want typed revocation availability message", response.Body.String())
	}
}
