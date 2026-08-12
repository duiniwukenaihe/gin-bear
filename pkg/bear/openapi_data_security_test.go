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

type mountedOpenAPIMetadataController struct {
	controllerAuth Fairing
	routeAuth      Fairing
}

func (c *mountedOpenAPIMetadataController) Name() string { return "MountedOpenAPIMetadataController" }

func (c *mountedOpenAPIMetadataController) Build(b *Bear) {
	b.Handle(http.MethodGet, "/controller-private", func() string { return "controller" })
	b.HandleWithFairing(http.MethodGet, "/route-private", func() string { return "route" }, c.routeAuth)
}

func (c *mountedOpenAPIMetadataController) Interceptors() []Fairing {
	return []Fairing{c.controllerAuth}
}

func (c *mountedOpenAPIMetadataController) OpenAPI() map[string]OpenAPIInfo {
	return map[string]OpenAPIInfo{
		"/controller-private": {
			Summary:     "controller auth summary",
			Description: "controller auth description",
		},
		"/route-private": {
			Summary:     "route auth summary",
			Description: "route auth description",
		},
	}
}

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
	name    string
	summary string
}

type mountedAuthIsolationController struct {
	name    string
	path    string
	auth    Fairing
	summary string
}

type nestedOpenAPIController struct {
	name       string
	path       string
	summary    string
	fairings   []Fairing
	buildInner IClass
}

func (c *nestedOpenAPIController) Name() string { return c.name }

func (c *nestedOpenAPIController) Build(b *Bear) {
	if c.buildInner == nil {
		b.Handle(http.MethodGet, c.path, func() string { return c.name })
		return
	}
	b.Handle(http.MethodGet, "/before", func() string { return "before" })
	b.Group("/inner", c.buildInner)
	b.Handle(http.MethodGet, "/after", func() string { return "after" })
}

func (c *nestedOpenAPIController) Interceptors() []Fairing { return c.fairings }

func (c *nestedOpenAPIController) OpenAPI() map[string]OpenAPIInfo {
	if c.buildInner == nil {
		return map[string]OpenAPIInfo{c.path: {Summary: c.summary}}
	}
	return map[string]OpenAPIInfo{
		"/before": {Summary: "outer before"},
		"/after":  {Summary: "outer after"},
	}
}

func (c *mountedAuthIsolationController) Name() string { return c.name }

func (c *mountedAuthIsolationController) Build(b *Bear) {
	b.Handle(http.MethodGet, c.path, func() string { return c.name })
}

func (c *mountedAuthIsolationController) Interceptors() []Fairing {
	if c.auth == nil {
		return nil
	}
	return []Fairing{c.auth}
}

func (c *mountedAuthIsolationController) OpenAPI() map[string]OpenAPIInfo {
	return map[string]OpenAPIInfo{c.path: {Summary: c.summary}}
}

func (c *groupedOpenAPIController) Name() string { return c.name }

func (c *groupedOpenAPIController) Build(b *Bear) {
	b.Handle(http.MethodGet, "/item", func() string { return "ok" })
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
	cfg.Auth.JWTSecret = randomProductionJWTKey(t)

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

func TestMountIsolatesControllerFairingsAndOpenAPISecurity(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)
	privateController := &mountedAuthIsolationController{
		name:    "private-controller",
		path:    "/private",
		auth:    NewAuthFairing(),
		summary: "private",
	}
	publicController := &mountedAuthIsolationController{
		name:    "public-controller",
		path:    "/public",
		summary: "public",
	}
	app.Mount("/api", privateController, publicController)
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("apply mounted controllers: %v", err)
	}

	privateResponse := httptest.NewRecorder()
	app.ServeHTTP(privateResponse, httptest.NewRequest(http.MethodGet, "/api/private", nil))
	if privateResponse.Code != http.StatusUnauthorized {
		t.Fatalf("private response status = %d, want %d", privateResponse.Code, http.StatusUnauthorized)
	}
	publicResponse := httptest.NewRecorder()
	app.ServeHTTP(publicResponse, httptest.NewRequest(http.MethodGet, "/api/public", nil))
	if publicResponse.Code != http.StatusOK {
		t.Fatalf("public response status = %d, want %d; controller fairing leaked: %s", publicResponse.Code, http.StatusOK, publicResponse.Body.String())
	}

	spec := decodeOpenAPIDocument(t, app)
	paths := spec["paths"].(map[string]interface{})
	privateOp := paths["/api/private"].(map[string]interface{})["get"].(map[string]interface{})
	if security, ok := privateOp["security"].([]interface{}); !ok || len(security) != 1 {
		t.Fatalf("private OpenAPI security = %#v, want BearerAuth", privateOp["security"])
	}
	publicOp := paths["/api/public"].(map[string]interface{})["get"].(map[string]interface{})
	if _, ok := publicOp["security"]; ok {
		t.Fatalf("public OpenAPI operation declared security: %#v", publicOp)
	}
}

func TestNestedGroupRestoresOpenAPIRegistrationContext(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)
	outerFairing := &BaseFairing{}
	innerAuth := NewAuthFairing()
	inner := &nestedOpenAPIController{
		name:     "inner-controller",
		path:     "/item",
		summary:  "inner item",
		fairings: []Fairing{innerAuth},
	}
	outer := &nestedOpenAPIController{
		name:       "outer-controller",
		summary:    "outer",
		fairings:   []Fairing{outerFairing},
		buildInner: inner,
	}
	app.Mount("/api", outer)
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("apply nested controllers: %v", err)
	}

	wants := []struct {
		path       string
		controller IOpenAPI
		fairings   []Fairing
	}{
		{path: "/api/before", controller: outer, fairings: []Fairing{outerFairing}},
		{path: "/api/inner/item", controller: inner, fairings: []Fairing{outerFairing, innerAuth}},
		{path: "/api/after", controller: outer, fairings: []Fairing{outerFairing}},
	}
	if len(app.routeRegistry) != len(wants) {
		t.Fatalf("nested route count = %d, want %d", len(app.routeRegistry), len(wants))
	}
	for index, want := range wants {
		metadata, ok := app.openAPIRouteMetadata(app.routeRegistry[index])
		if !ok {
			t.Fatalf("nested route %d has no OpenAPI registration metadata", index)
		}
		if metadata.fullPath != want.path {
			t.Fatalf("nested route %d full path = %q, want %q", index, metadata.fullPath, want.path)
		}
		if metadata.controller != want.controller {
			t.Fatalf("nested route %d controller = %T, want %T", index, metadata.controller, want.controller)
		}
		if !reflect.DeepEqual(metadata.fairings, want.fairings) {
			t.Fatalf("nested route %d fairings = %#v, want %#v", index, metadata.fairings, want.fairings)
		}
	}
	innerResponse := httptest.NewRecorder()
	app.ServeHTTP(innerResponse, httptest.NewRequest(http.MethodGet, "/api/inner/item", nil))
	if innerResponse.Code != http.StatusUnauthorized {
		t.Fatalf("inner response status = %d, want %d", innerResponse.Code, http.StatusUnauthorized)
	}
	afterResponse := httptest.NewRecorder()
	app.ServeHTTP(afterResponse, httptest.NewRequest(http.MethodGet, "/api/after", nil))
	if afterResponse.Code != http.StatusOK {
		t.Fatalf("outer response after nested group = %d, want %d; inner fairing leaked: %s", afterResponse.Code, http.StatusOK, afterResponse.Body.String())
	}

	spec := decodeOpenAPIDocument(t, app)
	paths := spec["paths"].(map[string]interface{})
	for path, summary := range map[string]string{
		"/api/before":     "outer before",
		"/api/inner/item": "inner item",
		"/api/after":      "outer after",
	} {
		op := paths[path].(map[string]interface{})["get"].(map[string]interface{})
		if got := op["summary"]; got != summary {
			t.Fatalf("OpenAPI %s summary = %v, want %q", path, got, summary)
		}
	}
	innerOp := paths["/api/inner/item"].(map[string]interface{})["get"].(map[string]interface{})
	if security, ok := innerOp["security"].([]interface{}); !ok || len(security) != 1 {
		t.Fatalf("inner OpenAPI security = %#v, want BearerAuth", innerOp["security"])
	}
	for _, path := range []string{"/api/before", "/api/after"} {
		op := paths[path].(map[string]interface{})["get"].(map[string]interface{})
		if _, ok := op["security"]; ok {
			t.Fatalf("outer OpenAPI operation %s declared inner security: %#v", path, op)
		}
	}
}

func TestGenerateOpenAPIUsesMetadataCapturedByMountedRouteRegistration(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)
	controller := &mountedOpenAPIMetadataController{
		controllerAuth: NewAuthFairing(),
		routeAuth:      NewAuthFairing(),
	}
	app.Mount("/api", controller)
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("apply mounted controller: %v", err)
	}
	if len(app.routeRegistry) != 2 {
		t.Fatalf("mounted route count = %d, want 2", len(app.routeRegistry))
	}
	for index, want := range []struct {
		path     string
		fairings []Fairing
	}{
		{path: "/api/controller-private", fairings: []Fairing{controller.controllerAuth}},
		{path: "/api/route-private", fairings: []Fairing{controller.controllerAuth, controller.routeAuth}},
	} {
		metadata, ok := app.openAPIRouteMetadata(app.routeRegistry[index])
		if !ok {
			t.Fatalf("mounted route %d did not capture private OpenAPI metadata", index)
		}
		if metadata.fullPath != want.path {
			t.Fatalf("mounted route %d full path = %q, want %q", index, metadata.fullPath, want.path)
		}
		if metadata.controller != controller {
			t.Fatalf("mounted route %d controller = %p, want %p", index, metadata.controller, controller)
		}
		if !reflect.DeepEqual(metadata.fairings, want.fairings) {
			t.Fatalf("mounted route %d fairings = %#v, want %#v", index, metadata.fairings, want.fairings)
		}
	}

	spec := decodeOpenAPIDocument(t, app)
	paths := spec["paths"].(map[string]interface{})
	for path, wantSummary := range map[string]string{
		"/api/controller-private": "controller auth summary",
		"/api/route-private":      "route auth summary",
	} {
		op, ok := paths[path].(map[string]interface{})["get"].(map[string]interface{})
		if !ok {
			t.Fatalf("mounted route %s missing from OpenAPI paths: %#v", path, paths)
		}
		if got := op["summary"]; got != wantSummary {
			t.Fatalf("mounted route %s summary = %v, want %q", path, got, wantSummary)
		}
		if _, ok := op["description"].(string); !ok {
			t.Fatalf("mounted route %s description missing: %#v", path, op)
		}
		security, ok := op["security"].([]interface{})
		if !ok || len(security) != 1 {
			t.Fatalf("mounted route %s security = %#v, want BearerAuth requirement", path, op["security"])
		}
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
	spec := decodeOpenAPIDocument(t, app)
	op := spec["paths"].(map[string]interface{})["/api/metadata"].(map[string]interface{})["get"].(map[string]interface{})
	if op["summary"] != "metadata summary" || op["description"] != "metadata description" {
		t.Fatalf("controller OpenAPI metadata missing: %#v", op)
	}
}

func TestGenerateOpenAPIUsesControllerIdentityForSameRelativePath(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	app := newOpenAPITestApp()
	controllerA := &groupedOpenAPIController{name: "group-a-controller", summary: "group-a"}
	controllerB := &groupedOpenAPIController{name: "group-b-controller", summary: "group-b"}
	app.Mount("/a", controllerA)
	app.Mount("/b", controllerB)
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("apply grouped controllers: %v", err)
	}

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
	cfg.SetFrameworkStrict(true)
	cfg.Auth.JWTSecret = randomProductionJWTKey(t)
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
	route := RouteMetadata{
		Method:      http.MethodGet,
		Path:        "/nil-provider",
		HandlerType: reflect.TypeOf(func() string { return "ok" }),
		HandlerName: "nilProviderHandler",
	}
	if info, ok := openAPIControllerInfo(openAPIRouteMetadata{controller: provider}, route, route.Path); ok {
		t.Fatalf("typed nil metadata provider returned info: %#v", info)
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
