package bear

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

type terminalRequestFairing struct {
	BaseFairing
	name   string
	events *[]string
	stop   func(*gin.Context)
}

func (f *terminalRequestFairing) OnRequest(ctx *gin.Context) error {
	*f.events = append(*f.events, "request:"+f.name)
	f.stop(ctx)
	return nil
}

func TestWriteErrorRedacts5xxBeforeLocalization(t *testing.T) {
	bundle := i18n.NewBundle(language.English)
	if err := bundle.AddMessages(language.English, &i18n.Message{
		ID:    "storage_failure",
		Other: "storage failed: {{index . 0}}",
	}); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/storage", nil)
	ctx.Set(LocalizerKey, i18n.NewLocalizer(bundle, "en"))
	err := NewStatusError(http.StatusInternalServerError, 500, "storage_failure", errors.New("database unavailable")).WithArgs("password=secret")

	localized, localizeErr := GetLocalizer(ctx).Localize(&i18n.LocalizeConfig{
		MessageID:    err.Key,
		TemplateData: err.Args,
	})
	if localizeErr != nil || !strings.Contains(localized, "password=secret") {
		t.Fatalf("test localizer did not expose the argument: message=%q err=%v", localized, localizeErr)
	}

	WriteError(ctx, err)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "password=secret") {
		t.Fatalf("response leaked localized 5xx argument: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "Internal server error") {
		t.Fatalf("response missing safe 5xx message: %s", response.Body.String())
	}
}

func TestWriteErrorAbortsCommittedInterceptorPipeline(t *testing.T) {
	engine := gin.New()
	businessCalled := false
	engine.Use(func(ctx *gin.Context) {
		ctx.String(http.StatusOK, "already committed")
		WriteError(ctx, errors.New("interceptor failure"))
	})
	engine.GET("/value", func(*gin.Context) {
		businessCalled = true
	})

	response := performRequest(engine, httptest.NewRequest(http.MethodGet, "/value", nil))

	if businessCalled {
		t.Fatal("business handler ran after committed interceptor error")
	}
	if response.Body.String() != "already committed" {
		t.Fatalf("body = %q", response.Body.String())
	}
}

func TestHandleERejectsTypedNilFunctionsDuringConstruction(t *testing.T) {
	var business func() string
	var ginHandler gin.HandlerFunc
	var contextHandler func(*gin.Context)

	tests := []struct {
		name    string
		handler interface{}
	}{
		{name: "business function", handler: business},
		{name: "gin handler", handler: ginHandler},
		{name: "context handler", handler: contextHandler},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := Ignite(NewSysConfig())
			before := len(app.Routes())

			err := app.HandleE(http.MethodGet, "/typed-nil", tt.handler)

			if err == nil {
				t.Fatal("HandleE accepted a typed-nil handler")
			}
			if after := len(app.Routes()); after != before {
				t.Fatalf("typed-nil handler registered a route: before=%d after=%d", before, after)
			}
		})
	}
}

func TestRequestFairingTerminalStateStopsPipeline(t *testing.T) {
	tests := []struct {
		name       string
		routeStop  func(*gin.Context)
		globalStop func(*gin.Context)
		wantEvents []string
		wantStatus int
	}{
		{
			name:       "route abort",
			routeStop:  func(ctx *gin.Context) { ctx.AbortWithStatus(http.StatusNoContent) },
			wantEvents: []string{"request:route-terminal"},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "route write",
			routeStop:  func(ctx *gin.Context) { ctx.String(http.StatusTeapot, "route response") },
			wantEvents: []string{"request:route-terminal"},
			wantStatus: http.StatusTeapot,
		},
		{
			name:       "global abort",
			globalStop: func(ctx *gin.Context) { ctx.AbortWithStatus(http.StatusNoContent) },
			wantEvents: []string{"request:global-terminal"},
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "global write",
			globalStop: func(ctx *gin.Context) { ctx.String(http.StatusTeapot, "global response") },
			wantEvents: []string{"request:global-terminal"},
			wantStatus: http.StatusTeapot,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var events []string
			businessCalled := false
			app := Ignite(NewSysConfig())
			if tt.globalStop != nil {
				app.Attach(&terminalRequestFairing{name: "global-terminal", events: &events, stop: tt.globalStop})
			}
			app.Attach(&recordingFairing{name: "global-after", events: &events})

			handler := func() string {
				businessCalled = true
				return "handler"
			}
			if tt.routeStop != nil {
				app.HandleWithFairing(http.MethodGet, "/value", handler, &terminalRequestFairing{name: "route-terminal", events: &events, stop: tt.routeStop})
			} else {
				app.Handle(http.MethodGet, "/value", handler)
			}

			response := performRequest(app, httptest.NewRequest(http.MethodGet, "/value", nil))

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
			}
			if businessCalled {
				t.Fatal("business handler ran after terminal request Fairing")
			}
			if !reflect.DeepEqual(events, tt.wantEvents) {
				t.Fatalf("events = %#v, want %#v", events, tt.wantEvents)
			}
		})
	}
}
