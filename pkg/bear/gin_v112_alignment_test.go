package bear

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/gin-gonic/gin"
)

type ginV112TextValue string

func (v *ginV112TextValue) UnmarshalText(text []byte) error {
	value := string(text)
	if !strings.HasPrefix(value, "value:") {
		return fmt.Errorf("invalid text value %q", value)
	}
	*v = ginV112TextValue(strings.TrimPrefix(value, "value:"))
	return nil
}

func TestBindingSupportsGinV112ExplicitTextUnmarshaler(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/items?q=value:query", nil)
	ctx.Params = gin.Params{{Key: "id", Value: "value:path"}}

	var request struct {
		Query ginV112TextValue  `query:"q,parser=encoding.TextUnmarshaler"`
		ID    *ginV112TextValue `uri:"id,parser=encoding.TextUnmarshaler"`
	}
	if err := bindRequest(ctx, &request); err != nil {
		t.Fatal(err)
	}
	if request.Query != "query" || request.ID == nil || *request.ID != "path" {
		t.Fatalf("text values = query:%q id:%v", request.Query, request.ID)
	}

	ctx.Request = httptest.NewRequest(http.MethodGet, "/items?q=invalid", nil)
	if err := bindRequest(ctx, &request); err == nil || !strings.Contains(err.Error(), "invalid text value") {
		t.Fatalf("invalid text binding error = %v", err)
	}
}

func TestRecoveryTreatsConnectionFailuresAsRequestAborts(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
	}{
		{name: "abort handler", err: http.ErrAbortHandler},
		{name: "broken pipe", err: syscall.EPIPE},
		{name: "connection reset", err: syscall.ECONNRESET},
	} {
		t.Run(failure.name, func(t *testing.T) {
			var logs bytes.Buffer
			runtime := &Runtime{Logger: slog.New(slog.NewJSONHandler(&logs, nil))}
			for _, middleware := range []struct {
				name    string
				handler gin.HandlerFunc
			}{
				{name: "runtime", handler: runtimeRecoveryMiddleware(runtime)},
				{name: "compatibility", handler: RecoveryMiddleware()},
			} {
				t.Run(middleware.name, func(t *testing.T) {
					router := gin.New()
					router.Use(middleware.handler)
					router.GET("/abort", func(ctx *gin.Context) {
						ctx.Status(http.StatusNoContent)
						panic(fmt.Errorf("wrapped connection abort: %w", failure.err))
					})

					response := performRequest(router, httptest.NewRequest(http.MethodGet, "/abort", nil))
					if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
						t.Fatalf("response = %d %q", response.Code, response.Body.String())
					}
				})
			}
			if strings.Contains(logs.String(), "Panic recovered") {
				t.Fatalf("connection abort was logged as a framework panic: %s", logs.String())
			}
		})
	}
}
