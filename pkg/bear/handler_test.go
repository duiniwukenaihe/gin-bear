package bear

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

type receiverHandler struct {
	value string
}

func (h *receiverHandler) Get() string {
	return h.value
}

type recordingFairing struct {
	BaseFairing
	name        string
	events      *[]string
	requestErr  error
	responseErr error
}

func (f *recordingFairing) OnRequest(*gin.Context) error {
	*f.events = append(*f.events, "request:"+f.name)
	return f.requestErr
}

func (f *recordingFairing) OnResponse(result interface{}) (interface{}, error) {
	*f.events = append(*f.events, "response:"+f.name)
	if f.responseErr != nil {
		return nil, f.responseErr
	}
	return fmt.Sprintf("%s(%v)", f.name, result), nil
}

type errorFairing struct {
	BaseFairing
	err error
}

func (f errorFairing) OnRequest(*gin.Context) error {
	return f.err
}

type authoritativeIDRequest struct {
	ID int64 `uri:"id" json:"id" form:"id"`
}

func TestHandleKeepsBoundReceiverIdentity(t *testing.T) {
	a := &receiverHandler{value: "a"}
	b := &receiverHandler{value: "b"}
	app := Ignite(NewSysConfig())
	app.Handle(http.MethodGet, "/a", a.Get)
	app.Handle(http.MethodGet, "/b", b.Get)
	assertJSONValue(t, app, "/a", "a")
	assertJSONValue(t, app, "/b", "b")
}

func TestConvertKeepsBoundReceiverIdentity(t *testing.T) {
	a := &receiverHandler{value: "a"}
	b := &receiverHandler{value: "b"}
	app := Ignite(NewSysConfig())
	app.GET("/a", Convert(a.Get))
	app.GET("/b", Convert(b.Get))
	assertJSONValue(t, app, "/a", "a")
	assertJSONValue(t, app, "/b", "b")
}

func TestFairingPipelineWritesTransformedResultOnce(t *testing.T) {
	var events []string
	app := Ignite(NewSysConfig())
	app.Attach(&recordingFairing{name: "global", events: &events})
	app.HandleWithFairing(http.MethodGet, "/value", func() (string, error) {
		return "handler", nil
	}, &recordingFairing{name: "route", events: &events})

	response := performRequest(app, httptest.NewRequest(http.MethodGet, "/value", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	assertDecodedJSON(t, response.Body.Bytes(), "route(global(handler))")
	wantEvents := []string{"request:route", "request:global", "response:global", "response:route"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("events = %#v, want %#v", events, wantEvents)
	}
	assertSingleJSONValue(t, response.Body.Bytes())
}

func TestFairingUnauthorizedUsesHTTP401(t *testing.T) {
	app := Ignite(NewSysConfig())
	app.Attach(&errorFairing{err: NewStatusError(401, 401, "error_unauthorized", nil)})
	app.Handle(http.MethodGet, "/private", func() string { return "secret" })

	response := performRequest(app, httptest.NewRequest(http.MethodGet, "/private", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	var body Response
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != http.StatusUnauthorized || body.Message != "Unauthorized" {
		t.Fatalf("body = %#v", body)
	}
	assertSingleJSONValue(t, response.Body.Bytes())
}

func TestFairingHandlerLegacyOnResponseContinuesAfterError(t *testing.T) {
	var events []string
	handler := NewFairingHandler()
	handler.AddFairing(
		&recordingFairing{name: "first", events: &events, responseErr: errors.New("ignored")},
		&recordingFairing{name: "second", events: &events},
	)

	result := handler.OnResponse("handler")
	if result != "second(handler)" {
		t.Fatalf("result = %#v", result)
	}
	want := []string{"response:first", "response:second"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestBindingRejectsSecondJSONValue(t *testing.T) {
	app := newBindingTestApp()
	request := newJSONRequest("/users", `{"name":"a"}{"name":"b"}`)
	response := performRequest(app, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	assertSingleJSONValue(t, response.Body.Bytes())
}

func TestBindingRejectsTrailingNonWhitespace(t *testing.T) {
	app := newBindingTestApp()
	request := newJSONRequest("/users", `{"name":"a"} trailing`)
	response := performRequest(app, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestBindingAllowsTrailingJSONWhitespace(t *testing.T) {
	app := newBindingTestApp()
	request := newJSONRequest("/users", "{\"name\":\"a\"}\n\t ")
	response := performRequest(app, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	assertDecodedJSON(t, response.Body.Bytes(), "a")
}

func TestURIValueWins(t *testing.T) {
	app := Ignite(NewSysConfig())
	app.Handle(http.MethodPost, "/users/:id", func(request authoritativeIDRequest) int64 {
		return request.ID
	})

	request := newJSONRequest("/users/41?id=42", `{"id":43}`)
	response := performRequest(app, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	assertDecodedJSON(t, response.Body.Bytes(), float64(41))
}

func TestBufferedSuccessReturnsInternalServerErrorBeforeCommit(t *testing.T) {
	app := Ignite(NewSysConfig())
	app.Handle(http.MethodGet, "/unencodable", func() interface{} {
		return map[string]interface{}{"bad": func() {}}
	})

	response := performRequest(app, httptest.NewRequest(http.MethodGet, "/unencodable", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	assertSingleJSONValue(t, response.Body.Bytes())
}

func TestResponseModeEnvelopeWrapsOrdinaryValues(t *testing.T) {
	config := NewSysConfig()
	if err := config.SetResponseMode("envelope"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		result  any
		status  int
		message string
	}{
		{name: "ok", result: "value", status: http.StatusOK, message: "OK"},
		{name: "created", result: WithStatus(http.StatusCreated, "value"), status: http.StatusCreated, message: "Created"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/value", nil)

			writeSuccessWithConfig(ctx, config, tt.result)

			if response.Code != tt.status {
				t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
			}
			var body Response
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != tt.status || body.Message != tt.message {
				t.Fatalf("body = %#v", body)
			}
			data, ok := body.Data.(string)
			if !ok || data != "value" {
				t.Fatalf("body data = %#v", body.Data)
			}
		})
	}
}

func TestStatusResponseWritesStatus(t *testing.T) {
	app := Ignite(NewSysConfig())
	app.Handle(http.MethodPost, "/users", func() StatusResponse {
		return WithStatus(http.StatusCreated, "created")
	})

	response := performRequest(app, httptest.NewRequest(http.MethodPost, "/users", nil))
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	assertDecodedJSON(t, response.Body.Bytes(), "created")
}

func TestStatusResponseRejectsInvalidStatus(t *testing.T) {
	for _, status := range []int{199, 600} {
		t.Run(fmt.Sprintf("status %d", status), func(t *testing.T) {
			app := Ignite(NewSysConfig())
			app.Handle(http.MethodGet, "/value", func() StatusResponse {
				return WithStatus(status, "ignored")
			})

			response := performRequest(app, httptest.NewRequest(http.MethodGet, "/value", nil))
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
			}
			assertSingleJSONValue(t, response.Body.Bytes())
		})
	}
}

func TestEnvelopeResponseValueAndPointerAreNotWrappedAgain(t *testing.T) {
	config := NewSysConfig()
	if err := config.SetResponseMode("envelope"); err != nil {
		t.Fatal(err)
	}
	bundle := i18n.NewBundle(language.English)
	if err := bundle.AddMessages(language.English, &i18n.Message{ID: "custom", Other: "translated"}); err != nil {
		t.Fatal(err)
	}

	for _, pointer := range []bool{false, true} {
		name := "value"
		if pointer {
			name = "pointer"
		}
		t.Run(name, func(t *testing.T) {
			original := &Response{Code: 701, Message: "custom", Data: "value"}
			var result any = *original
			if pointer {
				result = original
			}
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/value", nil)
			ctx.Set(LocalizerKey, i18n.NewLocalizer(bundle, "en"))

			writeSuccessWithConfig(ctx, config, result)

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
			}
			var body Response
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != original.Code || body.Message != "translated" || body.Data != original.Data {
				t.Fatalf("body = %#v", body)
			}
			if original.Message != "custom" {
				t.Fatalf("caller response was mutated: %#v", original)
			}
		})
	}
}

func TestEnvelopeNilResponsePointerUsesDefaultEnvelope(t *testing.T) {
	config := NewSysConfig()
	if err := config.SetResponseMode("envelope"); err != nil {
		t.Fatal(err)
	}
	var result *Response
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/value", nil)

	writeSuccessWithConfig(ctx, config, result)

	var body Response
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != http.StatusOK || body.Message != "OK" || body.Data != nil {
		t.Fatalf("body = %#v", body)
	}
}

func TestBufferedSuccessSkipsBodiesWhenHTTPForbidsThem(t *testing.T) {
	tests := []struct {
		name   string
		method string
		status int
	}{
		{name: "no content", method: http.MethodGet, status: http.StatusNoContent},
		{name: "not modified", method: http.MethodGet, status: http.StatusNotModified},
		{name: "head", method: http.MethodHead, status: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := Ignite(NewSysConfig())
			app.Handle(tt.method, "/value", func() StatusResponse {
				return WithStatus(tt.status, "ignored")
			})

			response := performRequest(app, httptest.NewRequest(tt.method, "/value", nil))
			if response.Code != tt.status {
				t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
			}
			if response.Body.Len() != 0 {
				t.Fatalf("body = %q, want empty", response.Body.String())
			}
		})
	}
}

func TestNoBodyResponsesValidatePayloadBeforeCommit(t *testing.T) {
	tests := []struct {
		name   string
		method string
		status int
	}{
		{name: "no content", method: http.MethodGet, status: http.StatusNoContent},
		{name: "not modified", method: http.MethodGet, status: http.StatusNotModified},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := Ignite(NewSysConfig())
			app.Handle(tt.method, "/value", func() StatusResponse {
				return WithStatus(tt.status, func() {})
			})

			response := performRequest(app, httptest.NewRequest(tt.method, "/value", nil))
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
			}
			assertSingleJSONValue(t, response.Body.Bytes())
		})
	}
}

func TestHEADSerializationFailureWritesEmpty500(t *testing.T) {
	app := Ignite(NewSysConfig())
	app.Handle(http.MethodHead, "/value", func() StatusResponse {
		return WithStatus(http.StatusOK, func() {})
	})

	response := performRequest(app, httptest.NewRequest(http.MethodHead, "/value", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if response.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty", response.Body.String())
	}
}

func TestHandleERejectsInvalidSignaturesDuringConstruction(t *testing.T) {
	tests := []struct {
		name    string
		handler interface{}
	}{
		{name: "nil", handler: nil},
		{name: "non-function", handler: 42},
		{name: "unsupported argument", handler: func(chan int) string { return "" }},
		{name: "duplicate context", handler: func(*gin.Context, *gin.Context) string { return "" }},
		{name: "second result is not error", handler: func() (string, string) { return "", "" }},
		{name: "too many results", handler: func() (string, string, string) { return "", "", "" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := Ignite(NewSysConfig())
			before := len(app.Routes())
			err := app.HandleE(http.MethodGet, "/invalid", tt.handler)
			if err == nil {
				t.Fatal("HandleE returned nil error")
			}
			if after := len(app.Routes()); after != before {
				t.Fatalf("invalid handler registered a route: before=%d after=%d", before, after)
			}
		})
	}
}

func TestHandlePanicsForInvalidSignature(t *testing.T) {
	app := Ignite(NewSysConfig())
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("Handle did not panic")
		}
	}()
	app.Handle(http.MethodGet, "/invalid", func(chan int) string { return "" })
}

func TestHandleWithFairingPanicsForInvalidSignature(t *testing.T) {
	app := Ignite(NewSysConfig())
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("HandleWithFairing did not panic")
		}
	}()
	app.HandleWithFairing(http.MethodGet, "/invalid", 42)
}

func TestFairingRequestErrorStopsPipeline(t *testing.T) {
	var events []string
	called := false
	app := Ignite(NewSysConfig())
	app.Attach(&recordingFairing{name: "global", events: &events})
	app.HandleWithFairing(http.MethodGet, "/value", func() string {
		called = true
		return "handler"
	}, &recordingFairing{
		name:       "route",
		events:     &events,
		requestErr: NewStatusError(http.StatusForbidden, 403, "error_forbidden", errors.New("policy detail")),
	})

	response := performRequest(app, httptest.NewRequest(http.MethodGet, "/value", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if called {
		t.Fatal("handler ran after request Fairing error")
	}
	if want := []string{"request:route"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	if strings.Contains(response.Body.String(), "policy detail") {
		t.Fatalf("response leaked cause: %s", response.Body.String())
	}
}

func TestFairingResponseErrorStopsPipeline(t *testing.T) {
	var events []string
	app := Ignite(NewSysConfig())
	app.Attach(&recordingFairing{
		name:        "global",
		events:      &events,
		responseErr: NewStatusError(http.StatusConflict, 409, "error_conflict", errors.New("storage detail")),
	})
	app.HandleWithFairing(http.MethodGet, "/value", func() string {
		return "handler"
	}, &recordingFairing{name: "route", events: &events})

	response := performRequest(app, httptest.NewRequest(http.MethodGet, "/value", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	want := []string{"request:route", "request:global", "response:global"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	if strings.Contains(response.Body.String(), "storage detail") {
		t.Fatalf("response leaked cause: %s", response.Body.String())
	}
	assertSingleJSONValue(t, response.Body.Bytes())
}

func TestGinHandlerFuncIsOpaqueToResponseFairings(t *testing.T) {
	var events []string
	app := Ignite(NewSysConfig())
	app.Attach(&recordingFairing{name: "global", events: &events})
	app.HandleWithFairing(http.MethodGet, "/opaque", gin.HandlerFunc(func(ctx *gin.Context) {
		ctx.String(http.StatusCreated, "opaque")
	}), &recordingFairing{name: "route", events: &events})

	response := performRequest(app, httptest.NewRequest(http.MethodGet, "/opaque", nil))
	if response.Code != http.StatusCreated || response.Body.String() != "opaque" {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
	want := []string{"request:route", "request:global"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestWriteErrorFallsBackFromUnsafeStatusAndHidesCause(t *testing.T) {
	cause := errors.New("password=secret")
	app := Ignite(NewSysConfig())
	app.Handle(http.MethodGet, "/unsafe", func() (string, error) {
		return "", NewStatusError(99, 0, "", cause)
	})

	response := performRequest(app, httptest.NewRequest(http.MethodGet, "/unsafe", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "password=secret") {
		t.Fatalf("response leaked cause: %s", response.Body.String())
	}
	var body Response
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != http.StatusInternalServerError || body.Message != "Internal server error" {
		t.Fatalf("body = %#v", body)
	}
}

func TestWriteErrorHandlesContextWithoutRequest(t *testing.T) {
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)

	WriteError(ctx, NewStatusError(http.StatusUnauthorized, 401, "error_unauthorized", nil))

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestUnexpectedErrorDetailsRemainOutOfResponse(t *testing.T) {
	app := Ignite(NewSysConfig())
	app.Handle(http.MethodGet, "/boom", func() (string, error) {
		return "", errors.New("sql: password=secret")
	})

	response := performRequest(app, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "password=secret") {
		t.Fatalf("response leaked unexpected error: %s", response.Body.String())
	}
}

func newBindingTestApp() *Bear {
	type createRequest struct {
		Name string `json:"name" binding:"required"`
	}
	app := Ignite(NewSysConfig())
	app.Handle(http.MethodPost, "/users", func(request *createRequest) string {
		return request.Name
	})
	return app
}

func newJSONRequest(path, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func performRequest(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertJSONValue(t *testing.T, handler http.Handler, path, want string) {
	t.Helper()
	response := performRequest(handler, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("%s status = %d body = %s", path, response.Code, response.Body.String())
	}
	assertDecodedJSON(t, response.Body.Bytes(), want)
}

func assertDecodedJSON(t *testing.T, body []byte, want interface{}) {
	t.Helper()
	var got interface{}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded response = %#v, want %#v", got, want)
	}
}

func assertSingleJSONValue(t *testing.T, body []byte) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(body))
	var value interface{}
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode first JSON value from %q: %v", body, err)
	}
	if err := decoder.Decode(&value); !errors.Is(err, io.EOF) {
		t.Fatalf("response contains more than one JSON value: %q (second decode: %v)", body, err)
	}
}
