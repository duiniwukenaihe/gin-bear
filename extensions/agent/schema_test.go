package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cloudwego/eino/schema"
	jsonschema "github.com/eino-contrib/jsonschema"
)

func declaredTool() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: "update_order",
		Desc: "update",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"order_id": {Type: schema.String, Desc: "order id", Required: true},
			"priority": {Type: schema.Integer, Desc: "priority"},
			"mode":     {Type: schema.String, Enum: []string{"fast", "slow"}},
		}),
	}
}

func TestValidateToolArgsEnforcesContract(t *testing.T) {
	tool := declaredTool()
	if err := validateToolArgs(tool, map[string]any{"order_id": "7"}); err != nil {
		t.Fatalf("valid args: %v", err)
	}
	for name, args := range map[string]map[string]any{
		"missing required": {},
		"wrong type":       {"order_id": 7.0},
		"unknown field":    {"order_id": "7", "admin": true},
		"enum violation":   {"order_id": "7", "mode": "warp"},
		"non-integer":      {"order_id": "7", "priority": 1.5},
	} {
		if err := validateToolArgs(tool, args); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if err := validateToolArgs(tool, map[string]any{"order_id": "7", "priority": 2.0, "mode": "fast"}); err != nil {
		t.Fatalf("full valid args: %v", err)
	}
}

func TestUndeclaredToolsKeepObjectCheck(t *testing.T) {
	bare := &schema.ToolInfo{Name: "x", Desc: "x"}
	if err := validateToolArgs(bare, map[string]any{"anything": 1.0}); err != nil {
		t.Fatalf("undeclared tool: %v", err)
	}
}

func TestVendorParametersRendersStrictSchema(t *testing.T) {
	rendered, err := vendorParameters(declaredTool())
	if err != nil {
		t.Fatalf("vendor parameters: %v", err)
	}
	if rendered["type"] != "object" || rendered["additionalProperties"] != false {
		t.Fatalf("schema = %v", rendered)
	}
	properties, ok := rendered["properties"].(map[string]any)
	if !ok || properties["order_id"] == nil {
		t.Fatalf("properties = %v", rendered["properties"])
	}
	required, ok := rendered["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "order_id" {
		t.Fatalf("required = %v", rendered["required"])
	}
	if bare, err := vendorParameters(&schema.ToolInfo{Name: "x"}); err != nil || bare != nil {
		t.Fatalf("undeclared tool rendered %v, %v", bare, err)
	}
}

// TestJSONSchemaDeclarationMatchesParamsForm is the V3 equivalence
// acceptance: the same contract written as raw JSON Schema validates exactly
// what the Params declaration validates, including nested objects and array
// element constraints.
func TestJSONSchemaDeclarationMatchesParamsForm(t *testing.T) {
	byParams := &schema.ToolInfo{
		Name: "ship_order",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"order_id": {Type: schema.String, Required: true},
			"address": {Type: schema.Object, Required: true, SubParams: map[string]*schema.ParameterInfo{
				"city": {Type: schema.String, Required: true},
				"zip":  {Type: schema.String},
			}},
			"tags":     {Type: schema.Array, ElemInfo: &schema.ParameterInfo{Type: schema.String}},
			"priority": {Type: schema.Integer},
		}),
	}
	canonical, err := byParams.ParamsOneOf.ToJSONSchema()
	if err != nil {
		t.Fatalf("canonical schema: %v", err)
	}
	byJSONSchema := &schema.ToolInfo{
		Name:        "ship_order",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(canonical),
	}
	valid := map[string]any{
		"order_id": "7",
		"address":  map[string]any{"city": "HZ", "zip": "310000"},
		"tags":     []any{"a", "b"},
		"priority": 2.0,
	}
	forms := map[string]*schema.ToolInfo{"params": byParams, "jsonschema": byJSONSchema}
	for _, tool := range forms {
		if err := validateToolArgs(tool, valid); err != nil {
			t.Fatalf("valid args: %v", err)
		}
	}
	for name, args := range map[string]map[string]any{
		"missing required":         {"address": map[string]any{"city": "HZ"}},
		"nested missing required":  {"order_id": "7", "address": map[string]any{"zip": "1"}},
		"nested wrong type":        {"order_id": "7", "address": map[string]any{"city": 7.0}},
		"nested unknown field":     {"order_id": "7", "address": map[string]any{"city": "HZ", "admin": true}},
		"array element wrong type": {"order_id": "7", "address": map[string]any{"city": "HZ"}, "tags": []any{"a", 1.0}},
		"array not array":          {"order_id": "7", "address": map[string]any{"city": "HZ"}, "tags": "a"},
		"integer not integer":      {"order_id": "7", "address": map[string]any{"city": "HZ"}, "priority": 1.5},
	} {
		for form, tool := range forms {
			if err := validateToolArgs(tool, args); err == nil {
				t.Fatalf("%s accepted by %s form", name, form)
			}
		}
	}
	for form, tool := range forms {
		rendered, err := vendorParameters(tool)
		if err != nil {
			t.Fatalf("%s vendor parameters: %v", form, err)
		}
		properties := rendered["properties"].(map[string]any)
		address := properties["address"].(map[string]any)
		if address["type"] != "object" || address["additionalProperties"] != false {
			t.Fatalf("nested address = %v", address)
		}
		tags := properties["tags"].(map[string]any)
		items, ok := tags["items"].(map[string]any)
		if !ok || items["type"] != "string" {
			t.Fatalf("array items = %v", tags["items"])
		}
	}
}

func mustJSONSchema(t *testing.T, raw string) *jsonschema.Schema {
	t.Helper()
	var contract jsonschema.Schema
	if err := json.Unmarshal([]byte(raw), &contract); err != nil {
		t.Fatalf("bad test schema: %v", err)
	}
	return &contract
}

func validRegistryTool() *Tool {
	return &Tool{Info: declaredTool(), Execute: okToolExecute}
}

func okToolExecute(context.Context, map[string]any) (string, error) { return "ok", nil }

// TestUnsupportedSchemaIsRejected proves combinators and other shapes
// outside the enforceable subset fail loudly at registration, validation,
// and vendor serialization — never as a silent schemaless tool.
func TestUnsupportedSchemaIsRejected(t *testing.T) {
	anyOf := &schema.ToolInfo{
		Name: "fuzzy_lookup",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(
			mustJSONSchema(t, `{"anyOf":[{"type":"string"},{"type":"integer"}]}`),
		),
	}
	if _, err := NewRegistry(validRegistryTool(), &Tool{Info: anyOf, Execute: okToolExecute}); err == nil {
		t.Fatal("registry accepted an anyOf tool")
	}
	if err := validateToolArgs(anyOf, map[string]any{"q": "x"}); err == nil {
		t.Fatal("validation accepted an anyOf tool")
	}
	if _, err := vendorParameters(anyOf); err == nil {
		t.Fatal("vendor serialization accepted an anyOf tool")
	}
	// The equivalent Params declaration keeps working, so the rejection is
	// about the shape, not the tool.
	if _, err := NewRegistry(validRegistryTool()); err != nil {
		t.Fatalf("registry: %v", err)
	}
}
