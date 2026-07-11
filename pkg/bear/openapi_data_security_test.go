package bear

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type openAPIMetadataController struct{}

func (c *openAPIMetadataController) Name() string { return "OpenAPIMetadataController" }

func (c *openAPIMetadataController) Build(b *Bear) {
	b.Handle(http.MethodGet, "/metadata", c.Get)
}

func (c *openAPIMetadataController) Get() string { return "ok" }

func (c *openAPIMetadataController) OpenAPI() map[string]OpenAPIInfo {
	return map[string]OpenAPIInfo{
		"/metadata": {
			Summary:     "metadata summary",
			Description: "metadata description",
			Tags:        []string{"metadata"},
		},
	}
}

type nilOpenAPIProvider struct {
	summary string
}

type groupedOpenAPIController struct {
	summary string
}

func (c *groupedOpenAPIController) OpenAPI() map[string]OpenAPIInfo {
	return map[string]OpenAPIInfo{"/item": {Summary: c.summary}}
}

func (p *nilOpenAPIProvider) OpenAPI() map[string]OpenAPIInfo {
	return map[string]OpenAPIInfo{"/nil-provider": {Summary: p.summary}}
}

type recursiveOpenAPINode struct {
	Value    string                 `json:"value"`
	Next     *recursiveOpenAPINode  `json:"next"`
	Children []recursiveOpenAPINode `json:"children"`
}

func recursiveOpenAPIHandler() *recursiveOpenAPINode { return nil }

func TestRouteMetadataRetainsLegacyFiveFieldShape(t *testing.T) {
	_ = RouteMetadata{http.MethodGet, "/shape", "api", reflect.TypeOf(func() string { return "ok" }), "shapeHandler"}
	routeType := reflect.TypeOf(RouteMetadata{})
	if routeType.NumField() != 5 {
		t.Fatalf("RouteMetadata field count = %d, want 5", routeType.NumField())
	}
}

func TestGenerateOpenAPISecurityRequiresActualAuthFairing(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Auth.JWTSecret = "replace-with-at-least-32-random-characters"

	configuredOnly := Ignite(cfg)
	configuredOnly.Handle(http.MethodGet, "/configured-only", func() string { return "ok" })
	configuredSpec := decodeOpenAPIDocument(t, configuredOnly)
	if _, ok := configuredSpec["security"]; ok {
		t.Fatalf("auth config alone declared global security: %#v", configuredSpec["security"])
	}
	configuredComponents := configuredSpec["components"].(map[string]interface{})
	if _, ok := configuredComponents["securitySchemes"]; ok {
		t.Fatalf("auth config alone declared security scheme: %#v", configuredComponents)
	}

	withGlobalAuth := Ignite(cfg)
	withGlobalAuth.Attach(NewAuthFairing())
	withGlobalAuth.Handle(http.MethodGet, "/secured", func() string { return "ok" })
	securedSpec := decodeOpenAPIDocument(t, withGlobalAuth)
	if _, ok := securedSpec["security"]; !ok {
		t.Fatal("attached AuthFairing did not declare global security")
	}
	securedComponents := securedSpec["components"].(map[string]interface{})
	if _, ok := securedComponents["securitySchemes"]; !ok {
		t.Fatal("attached AuthFairing did not declare BearerAuth scheme")
	}
}

func TestGenerateOpenAPISecuritySupportsRouteAuthFairing(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)
	app.Handle(http.MethodGet, "/public", func() string { return "public" })
	auth := NewAuthFairing()
	app.HandleWithFairing(http.MethodGet, "/private", func() string { return "private" }, auth)
	privateRoute := app.routeRegistry[len(app.routeRegistry)-1]
	app.setOpenAPIRouteMetadata(privateRoute, "/private", nil, auth)

	spec := decodeOpenAPIDocument(t, app)
	if _, ok := spec["security"]; ok {
		t.Fatalf("route-only AuthFairing declared global security: %#v", spec["security"])
	}
	paths := spec["paths"].(map[string]interface{})
	publicOp := paths["/public"].(map[string]interface{})["get"].(map[string]interface{})
	if _, ok := publicOp["security"]; ok {
		t.Fatalf("unprotected route declared security: %#v", publicOp)
	}
	privateOp := paths["/private"].(map[string]interface{})["get"].(map[string]interface{})
	security, ok := privateOp["security"].([]interface{})
	if !ok || len(security) != 1 {
		t.Fatalf("route AuthFairing security = %#v, want BearerAuth requirement", privateOp["security"])
	}
}

func TestGenerateOpenAPIRouteSecurityUsesEffectiveFairingsMetadata(t *testing.T) {
	app := &Bear{routeRegistry: []RouteMetadata{
		{
			Method:      http.MethodGet,
			Path:        "/public",
			GroupName:   "/api",
			HandlerType: reflect.TypeOf(func() string { return "public" }),
			HandlerName: "publicHandler",
		},
		{
			Method:      http.MethodGet,
			Path:        "/private",
			GroupName:   "/api",
			HandlerType: reflect.TypeOf(func() string { return "private" }),
			HandlerName: "privateHandler",
		},
	}}
	app.setOpenAPIRouteMetadata(app.routeRegistry[0], "/api/public", nil)
	app.setOpenAPIRouteMetadata(app.routeRegistry[1], "/api/private", nil, NewAuthFairing())

	spec := decodeOpenAPIDocument(t, app)
	paths := spec["paths"].(map[string]interface{})
	if _, ok := paths["/api/public"].(map[string]interface{})["get"].(map[string]interface{})["security"]; ok {
		t.Fatal("route without effective AuthFairing declared security")
	}
	private := paths["/api/private"].(map[string]interface{})["get"].(map[string]interface{})
	if security, ok := private["security"].([]interface{}); !ok || len(security) != 1 {
		t.Fatalf("effective AuthFairing security = %#v", private["security"])
	}
}

func TestGenerateOpenAPIDoesNotInferEffectiveFairingsFromRouteTree(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	app := newOpenAPITestApp()
	app.HandleWithFairing(http.MethodGet, "/missing-effective-metadata", func() string { return "private" }, NewAuthFairing())

	spec := decodeOpenAPIDocument(t, app)
	if _, ok := spec["security"]; ok {
		t.Fatalf("routeTree fairing was guessed as global security: %#v", spec["security"])
	}
	components := spec["components"].(map[string]interface{})
	if _, ok := components["securitySchemes"]; ok {
		t.Fatalf("routeTree fairing was guessed without effective metadata: %#v", components)
	}
	op := spec["paths"].(map[string]interface{})["/missing-effective-metadata"].(map[string]interface{})["get"].(map[string]interface{})
	if _, ok := op["security"]; ok {
		t.Fatalf("routeTree fairing was guessed on operation: %#v", op)
	}
}

func TestGenerateOpenAPIFindsControllerMetadataByControllerBean(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)
	controller := &openAPIMetadataController{}
	app.Mount("/api", controller)
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("apply controller: %v", err)
	}
	app.setOpenAPIRouteMetadata(app.routeRegistry[0], "/api/metadata", controller)

	spec := decodeOpenAPIDocument(t, app)
	op := spec["paths"].(map[string]interface{})["/api/metadata"].(map[string]interface{})["get"].(map[string]interface{})
	if op["summary"] != "metadata summary" || op["description"] != "metadata description" {
		t.Fatalf("controller OpenAPI metadata missing: %#v", op)
	}
}

func TestGenerateOpenAPIUsesControllerIdentityForSameRelativePath(t *testing.T) {
	controllerA := &groupedOpenAPIController{summary: "group-a"}
	controllerB := &groupedOpenAPIController{summary: "group-b"}
	handlerType := reflect.TypeOf(func() string { return "ok" })
	app := &Bear{
		routeRegistry: []RouteMetadata{
			{
				Method:      http.MethodGet,
				Path:        "/item",
				GroupName:   "/a",
				HandlerType: handlerType,
				HandlerName: "groupAItem",
			},
			{
				Method:      http.MethodGet,
				Path:        "/item",
				GroupName:   "/b",
				HandlerType: handlerType,
				HandlerName: "groupBItem",
			},
		},
	}
	app.setOpenAPIRouteMetadata(app.routeRegistry[0], "/a/item", controllerA)
	app.setOpenAPIRouteMetadata(app.routeRegistry[1], "/b/item", controllerB)

	spec := decodeOpenAPIDocument(t, app)
	paths := spec["paths"].(map[string]interface{})
	if summary := paths["/a/item"].(map[string]interface{})["get"].(map[string]interface{})["summary"]; summary != "group-a" {
		t.Fatalf("group A summary = %v", summary)
	}
	if summary := paths["/b/item"].(map[string]interface{})["get"].(map[string]interface{})["summary"]; summary != "group-b" {
		t.Fatalf("group B summary = %v", summary)
	}
}

func TestGenerateOpenAPIOmitsControllerMetadataWithoutIdentity(t *testing.T) {
	app := &Bear{
		routeRegistry: []RouteMetadata{{
			Method:      http.MethodGet,
			Path:        "/item",
			GroupName:   "/anonymous",
			HandlerType: reflect.TypeOf(func() string { return "ok" }),
			HandlerName: "anonymousItem",
		}},
	}
	app.setOpenAPIRouteMetadata(app.routeRegistry[0], "/anonymous/item", nil)

	spec := decodeOpenAPIDocument(t, app)
	op := spec["paths"].(map[string]interface{})["/anonymous/item"].(map[string]interface{})["get"].(map[string]interface{})
	if _, ok := op["summary"]; ok {
		t.Fatalf("route without controller identity guessed metadata: %#v", op)
	}
}

func TestGenerateOpenAPIRecursiveDTOUsesComponentRefs(t *testing.T) {
	if os.Getenv("GIN_BEAR_RECURSIVE_OPENAPI_CHILD") == "1" {
		app := &Bear{routeRegistry: []RouteMetadata{{
			Method:      http.MethodGet,
			Path:        "/recursive",
			HandlerType: reflect.TypeOf(recursiveOpenAPIHandler),
			HandlerName: "recursiveOpenAPIHandler",
		}}}
		doc, err := app.GenerateOpenAPI()
		if err != nil {
			t.Fatalf("generate recursive OpenAPI: %v", err)
		}
		var spec map[string]interface{}
		if err := json.Unmarshal(doc, &spec); err != nil {
			t.Fatalf("decode recursive OpenAPI: %v", err)
		}
		schemas := spec["components"].(map[string]interface{})["schemas"].(map[string]interface{})
		node := schemas["recursiveOpenAPINode"].(map[string]interface{})
		properties := node["properties"].(map[string]interface{})
		if properties["next"].(map[string]interface{})["$ref"] != "#/components/schemas/recursiveOpenAPINode" {
			t.Fatalf("recursive next schema = %#v", properties["next"])
		}
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test executable: %v", err)
	}
	cmd := exec.Command(executable, "-test.run=^TestGenerateOpenAPIRecursiveDTOUsesComponentRefs$")
	cmd.Env = append(os.Environ(), "GIN_BEAR_RECURSIVE_OPENAPI_CHILD=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recursive OpenAPI subprocess failed: %v\n%s", err, output)
	}
}

func TestEnableSwaggerCachesPerBearInstance(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	first := newOpenAPITestApp()
	first.Handle(http.MethodGet, "/first", func() string { return "first" })
	first.EnableSwagger()

	firstDoc := serveOpenAPIPath(t, first, "/swagger/doc.json")
	first.Handle(http.MethodGet, "/late", func() string { return "late" })
	secondRead := serveOpenAPIPath(t, first, "/swagger/doc.json")
	if firstDoc != secondRead {
		t.Fatal("Swagger document changed after its first frozen generation")
	}

	second := newOpenAPITestApp()
	second.Handle(http.MethodGet, "/second", func() string { return "second" })
	second.EnableSwagger()
	secondDoc := serveOpenAPIPath(t, second, "/swagger/doc.json")
	if strings.Contains(firstDoc, "/second") || !strings.Contains(secondDoc, "/second") {
		t.Fatalf("Swagger cache leaked across Bear instances\nfirst=%s\nsecond=%s", firstDoc, secondDoc)
	}
}

func TestEnableSwaggerConcurrentFirstRequestCachesPerInstance(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	first := newOpenAPITestApp()
	first.Handle(http.MethodGet, "/first-concurrent", func() string { return "first" })
	first.EnableSwagger()
	second := newOpenAPITestApp()
	second.Handle(http.MethodGet, "/second-concurrent", func() string { return "second" })
	second.EnableSwagger()

	const requestsPerInstance = 32
	type response struct {
		status int
		body   string
	}
	results := make(chan response, requestsPerInstance*2)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, app := range []*Bear{first, second} {
		for range requestsPerInstance {
			workers.Add(1)
			go func(app *Bear) {
				defer workers.Done()
				<-start
				recorder := httptest.NewRecorder()
				app.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/swagger/doc.json", nil))
				results <- response{status: recorder.Code, body: recorder.Body.String()}
			}(app)
		}
	}
	close(start)
	workers.Wait()
	close(results)

	firstCount := 0
	secondCount := 0
	for result := range results {
		if result.status != http.StatusOK {
			t.Fatalf("concurrent Swagger status = %d body=%s", result.status, result.body)
		}
		switch {
		case strings.Contains(result.body, "/first-concurrent") && !strings.Contains(result.body, "/second-concurrent"):
			firstCount++
		case strings.Contains(result.body, "/second-concurrent") && !strings.Contains(result.body, "/first-concurrent"):
			secondCount++
		default:
			t.Fatalf("concurrent Swagger cache leaked or returned invalid document: %s", result.body)
		}
	}
	if firstCount != requestsPerInstance || secondCount != requestsPerInstance {
		t.Fatalf("concurrent result counts = first:%d second:%d", firstCount, secondCount)
	}
}

func TestEnableSwaggerUsesImmutableAssetsSRIAndCSP(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	app := newOpenAPITestApp()
	app.EnableSwagger()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/swagger", nil)
	app.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("Swagger UI status = %d, want 200", recorder.Code)
	}
	html := recorder.Body.String()
	for _, required := range []string{
		"swagger-ui-dist@5.17.14/swagger-ui.css",
		"swagger-ui-dist@5.17.14/swagger-ui-bundle.js",
		"integrity=\"sha384-",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("Swagger HTML missing %q: %s", required, html)
		}
	}
	if strings.Contains(html, "swagger-ui-dist@3") {
		t.Fatalf("Swagger HTML still uses mutable @3 assets: %s", html)
	}
	if csp := recorder.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("Swagger CSP = %q, want restrictive policy", csp)
	}
}

func TestEnableSwaggerIsDisabledInProduction(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Server.Mode = "release"
	cfg.Auth.JWTSecret = "replace-with-at-least-32-random-characters"
	app := Ignite(cfg)
	app.EnableSwagger()

	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/swagger", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("production Swagger status = %d, want 404", recorder.Code)
	}
}

func TestGenerateOpenAPIHandlesInputAndMetadataBoundaries(t *testing.T) {
	app := &Bear{routeRegistry: []RouteMetadata{{
		Method:      http.MethodPost,
		Path:        "/boundary/:id",
		HandlerType: reflect.TypeOf(func(any, map[string]interface{}) (interface{}, error) { return nil, nil }),
		HandlerName: "boundaryHandler",
	}}}
	if _, err := app.GenerateOpenAPI(); err != nil {
		t.Fatalf("boundary handler should produce valid OpenAPI: %v", err)
	}

	if _, err := (&Bear{routeRegistry: []RouteMetadata{{Method: http.MethodGet, Path: "/nil", HandlerName: "nilHandler"}}}).GenerateOpenAPI(); err != nil {
		t.Fatalf("nil handler metadata should remain valid: %v", err)
	}
}

func TestOpenAPISchemaBuilderKeepsReservedComponentName(t *testing.T) {
	reserved := map[string]interface{}{
		"recursiveOpenAPINode": map[string]interface{}{"type": "string"},
	}
	builder := newOpenAPISchemaBuilder(reserved)
	schema := builder.schemaForType(reflect.TypeOf(recursiveOpenAPINode{}), 0)
	if schema["$ref"] != "#/components/schemas/recursiveOpenAPINode_2" {
		t.Fatalf("colliding DTO ref = %#v", schema)
	}
	if _, ok := reserved["recursiveOpenAPINode_2"]; !ok {
		t.Fatalf("colliding DTO schema was not disambiguated: %#v", reserved)
	}
	if reserved["recursiveOpenAPINode"].(map[string]interface{})["type"] != "string" {
		t.Fatalf("reserved component was replaced: %#v", reserved)
	}
}

func TestGenerateOpenAPISkipsTypedNilMetadataProvider(t *testing.T) {
	var provider *nilOpenAPIProvider
	app := &Bear{
		routeRegistry: []RouteMetadata{{
			Method:      http.MethodGet,
			Path:        "/nil-provider",
			HandlerType: reflect.TypeOf(func() string { return "ok" }),
			HandlerName: "nilProviderHandler",
		}},
	}
	app.setOpenAPIRouteMetadata(app.routeRegistry[0], "/nil-provider", provider)
	if _, err := app.GenerateOpenAPI(); err != nil {
		t.Fatalf("typed nil metadata provider should be ignored: %v", err)
	}
}

func newOpenAPITestApp() *Bear {
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	return Ignite(cfg)
}

func decodeOpenAPIDocument(t *testing.T, app *Bear) map[string]interface{} {
	t.Helper()
	doc, err := app.GenerateOpenAPI()
	if err != nil {
		t.Fatalf("generate OpenAPI: %v", err)
	}
	var spec map[string]interface{}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatalf("decode OpenAPI: %v\n%s", err, doc)
	}
	return spec
}

func serveOpenAPIPath(t *testing.T, app *Bear, path string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d body=%s", path, recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}
