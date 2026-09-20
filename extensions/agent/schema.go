package agent

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/cloudwego/eino/schema"
	jsonschema "github.com/eino-contrib/jsonschema"
)

// einoParamMirror is the enforceable subset of a tool's parameter contract:
// scalars, string enums, nested objects, and array element constraints.
// Both eino declarations (Params maps and raw JSON Schemas) are normalized
// to this shape, so execution-side validation and vendor serialization share
// one contract. Anything outside the subset is rejected, never downgraded.
type einoParamMirror struct {
	Type      string                     `json:"Type"`
	Desc      string                     `json:"Desc"`
	Enum      []string                   `json:"Enum"`
	Required  bool                       `json:"Required"`
	SubParams map[string]einoParamMirror `json:"SubParams"`
	ElemInfo  *einoParamMirror           `json:"ElemInfo"`
}

// declaredParams extracts parameter declarations, or nil when the tool
// declares none (object shape is then the only check). Both eino forms go
// through ParamsOneOf.ToJSONSchema, so a JSON-Schema declaration enforces
// exactly what the equivalent Params declaration enforces. Schemas outside
// the enforceable subset (combinators, references, non-string enums, tuple
// or untyped shapes) return an error: callers must refuse the tool instead
// of validating against a silent subset.
func declaredParams(info *schema.ToolInfo) (map[string]einoParamMirror, error) {
	if info == nil || info.ParamsOneOf == nil {
		return nil, nil
	}
	contract, err := info.ParamsOneOf.ToJSONSchema()
	if err != nil {
		return nil, fmt.Errorf("encode tool schema: %w", err)
	}
	if contract == nil {
		return nil, nil
	}
	if err := rejectCombinators(contract, "tool"); err != nil {
		return nil, err
	}
	if contract.Properties == nil {
		if rootType := strings.ToLower(strings.TrimSpace(contract.Type)); rootType != "" && rootType != "object" {
			return nil, fmt.Errorf("tool declares non-object root type %q, which tools cannot take", contract.Type)
		}
		return nil, nil
	}
	required := map[string]bool{}
	for _, name := range contract.Required {
		required[name] = true
	}
	params := make(map[string]einoParamMirror, contract.Properties.Len())
	for pair := contract.Properties.Oldest(); pair != nil; pair = pair.Next() {
		mirror, err := mirrorFromJSONSchema(pair.Value, pair.Key)
		if err != nil {
			return nil, err
		}
		mirror.Required = required[pair.Key]
		params[pair.Key] = mirror
	}
	return params, nil
}

// rejectCombinators refuses schema shapes outside the enforceable subset:
// logic combinators, references, conditional and tuple forms have no
// representation in the mirror, so accepting them would silently downgrade
// the contract.
func rejectCombinators(contract *jsonschema.Schema, path string) error {
	if contract.Ref != "" || contract.DynamicRef != "" || contract.Definitions != nil ||
		len(contract.AllOf) > 0 || len(contract.AnyOf) > 0 || len(contract.OneOf) > 0 || contract.Not != nil ||
		contract.If != nil || contract.Then != nil || contract.Else != nil ||
		len(contract.DependentSchemas) > 0 || len(contract.PatternProperties) > 0 ||
		contract.PropertyNames != nil || len(contract.PrefixItems) > 0 || contract.Contains != nil {
		return fmt.Errorf("parameter %q uses an unsupported schema combinator; express it with scalar, enum, object, or array shapes", path)
	}
	return nil
}

// mirrorFromJSONSchema normalizes one property schema to the enforceable
// subset. The path identifies the offending declaration in errors.
func mirrorFromJSONSchema(contract *jsonschema.Schema, path string) (einoParamMirror, error) {
	var mirror einoParamMirror
	if contract == nil {
		return mirror, fmt.Errorf("parameter %q declares no schema", path)
	}
	if err := rejectCombinators(contract, path); err != nil {
		return mirror, err
	}
	dataType := contract.Type
	if len(contract.TypeEnhanced) > 1 {
		return mirror, fmt.Errorf("parameter %q declares a type union, which is not enforceable", path)
	}
	if len(contract.TypeEnhanced) == 1 {
		dataType = contract.TypeEnhanced[0]
	}
	switch normalized := strings.ToLower(strings.TrimSpace(dataType)); normalized {
	case "":
		if contract.Properties == nil {
			return mirror, fmt.Errorf("parameter %q declares no type", path)
		}
		mirror.Type = "object"
	case "string", "number", "integer", "boolean", "array", "object", "null":
		mirror.Type = normalized
	default:
		return mirror, fmt.Errorf("parameter %q declares unknown type %q", path, dataType)
	}
	mirror.Desc = contract.Description
	if contract.Const != nil {
		text, ok := contract.Const.(string)
		if !ok {
			return mirror, fmt.Errorf("parameter %q uses a non-string const, which is not enforceable", path)
		}
		mirror.Enum = []string{text}
	}
	for _, option := range contract.Enum {
		text, ok := option.(string)
		if !ok {
			return mirror, fmt.Errorf("parameter %q uses a non-string enum, which is not enforceable", path)
		}
		mirror.Enum = append(mirror.Enum, text)
	}
	if mirror.Type == "object" && contract.Properties != nil {
		required := map[string]bool{}
		for _, name := range contract.Required {
			required[name] = true
		}
		mirror.SubParams = make(map[string]einoParamMirror, contract.Properties.Len())
		for pair := contract.Properties.Oldest(); pair != nil; pair = pair.Next() {
			child, err := mirrorFromJSONSchema(pair.Value, path+"."+pair.Key)
			if err != nil {
				return mirror, err
			}
			child.Required = required[pair.Key]
			mirror.SubParams[pair.Key] = child
		}
	}
	if mirror.Type == "array" && contract.Items != nil {
		element, err := mirrorFromJSONSchema(contract.Items, path+"[]")
		if err != nil {
			return mirror, err
		}
		mirror.ElemInfo = &element
	}
	return mirror, nil
}

// validateToolArgs enforces the declared parameter contract before
// authorization: JSON types, required fields, enum membership, nested object
// shapes, array element constraints, and a closed world (unknown fields are
// rejected). Undeclared tools keep the legacy object-shape check.
func validateToolArgs(info *schema.ToolInfo, args map[string]any) error {
	params, err := declaredParams(info)
	if err != nil {
		return err
	}
	if params == nil {
		return nil
	}
	return checkParams(params, args, "")
}

func checkParams(params map[string]einoParamMirror, args map[string]any, prefix string) error {
	for name, param := range params {
		value, ok := args[name]
		if !ok {
			if param.Required {
				return fmt.Errorf("missing required parameter %q", prefix+name)
			}
			continue
		}
		if err := checkValue(param, value, prefix+name); err != nil {
			return err
		}
	}
	for name := range args {
		if _, ok := params[name]; !ok {
			return fmt.Errorf("unknown parameter %q", prefix+name)
		}
	}
	return nil
}

func checkValue(param einoParamMirror, value any, path string) error {
	switch strings.ToLower(param.Type) {
	case "", "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("parameter %q must be a string", path)
		}
		if len(param.Enum) > 0 && !containsString(param.Enum, text) {
			return fmt.Errorf("parameter %q must be one of %s", path, strings.Join(param.Enum, ", "))
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("parameter %q must be a number", path)
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || math.Trunc(number) != number {
			return fmt.Errorf("parameter %q must be an integer", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("parameter %q must be a boolean", path)
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("parameter %q must be an array", path)
		}
		if param.ElemInfo != nil {
			for index, item := range items {
				if err := checkValue(*param.ElemInfo, item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
					return err
				}
			}
		}
	case "object":
		nested, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("parameter %q must be an object", path)
		}
		if len(param.SubParams) > 0 {
			if err := checkParams(param.SubParams, nested, path+"."); err != nil {
				return err
			}
		}
	case "null":
		if value != nil {
			return fmt.Errorf("parameter %q must be null", path)
		}
	default:
		return fmt.Errorf("parameter %q declares unknown type %q", path, param.Type)
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// vendorParameters renders the declared contract as a JSON-Schema object for
// the vendor. It returns nil when the tool declares no parameters, and an
// error when the declaration is outside the enforceable subset: the vendor
// must never receive a silent schemaless tool.
func vendorParameters(info *schema.ToolInfo) (map[string]any, error) {
	params, err := declaredParams(info)
	if err != nil {
		return nil, err
	}
	if len(params) == 0 {
		return nil, nil
	}
	return renderObjectSchema(params), nil
}

func renderObjectSchema(params map[string]einoParamMirror) map[string]any {
	properties := make(map[string]any, len(params))
	required := []string{}
	for _, name := range sortedKeys(params) {
		param := params[name]
		properties[name] = renderParamSchema(param)
		if param.Required {
			required = append(required, name)
		}
	}
	return map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
}

func renderParamSchema(param einoParamMirror) map[string]any {
	property := map[string]any{"type": vendorType(param.Type)}
	if param.Desc != "" {
		property["description"] = param.Desc
	}
	if len(param.Enum) > 0 {
		property["enum"] = append([]string(nil), param.Enum...)
	}
	if len(param.SubParams) > 0 {
		nested := renderObjectSchema(param.SubParams)
		property["properties"] = nested["properties"]
		property["required"] = nested["required"]
		property["additionalProperties"] = false
	}
	if param.ElemInfo != nil {
		property["items"] = renderParamSchema(*param.ElemInfo)
	}
	return property
}

func vendorType(dataType string) string {
	switch strings.ToLower(strings.TrimSpace(dataType)) {
	case "number", "integer", "boolean", "array", "object", "null":
		return strings.ToLower(strings.TrimSpace(dataType))
	default:
		return "string"
	}
}

func sortedKeys(params map[string]einoParamMirror) []string {
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
