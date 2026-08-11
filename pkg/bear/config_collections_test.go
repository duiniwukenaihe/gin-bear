package bear

import (
	"encoding/json"
	"reflect"
	"testing"

	"gopkg.in/yaml.v2"
)

func TestAuthConfigPublicPathsAccessorsUseDefensiveCopies(t *testing.T) {
	input := []string{"/public"}
	var config AuthConfig
	config.SetPublicPaths(input)
	input[0] = "/mutated-input"

	got := config.GetPublicPaths()
	if !reflect.DeepEqual(got, []string{"/public"}) {
		t.Fatalf("GetPublicPaths() = %v", got)
	}
	got[0] = "/mutated-output"
	if current := config.GetPublicPaths(); !reflect.DeepEqual(current, []string{"/public"}) {
		t.Fatalf("GetPublicPaths exposed internal storage: %v", current)
	}

	config.SetPublicPaths(nil)
	if got := config.GetPublicPaths(); got != nil {
		t.Fatalf("nil PublicPaths became %#v", got)
	}
	config.SetPublicPaths([]string{})
	if got := config.GetPublicPaths(); got == nil || len(got) != 0 {
		t.Fatalf("empty PublicPaths lost explicit empty value: %#v", got)
	}
}

func TestWebSocketConfigAllowedOriginsAccessorsUseDefensiveCopies(t *testing.T) {
	input := []string{"https://app.example.com"}
	var config WebSocketConfig
	config.SetAllowedOrigins(input)
	input[0] = "https://mutated.example.com"

	got := config.GetAllowedOrigins()
	if !reflect.DeepEqual(got, []string{"https://app.example.com"}) {
		t.Fatalf("GetAllowedOrigins() = %v", got)
	}
	got[0] = "https://output.example.com"
	if current := config.GetAllowedOrigins(); !reflect.DeepEqual(current, []string{"https://app.example.com"}) {
		t.Fatalf("GetAllowedOrigins exposed internal storage: %v", current)
	}

	config.SetAllowedOrigins(nil)
	if got := config.GetAllowedOrigins(); got != nil {
		t.Fatalf("nil AllowedOrigins became %#v", got)
	}
	config.SetAllowedOrigins([]string{})
	if got := config.GetAllowedOrigins(); got == nil || len(got) != 0 {
		t.Fatalf("empty AllowedOrigins lost explicit empty value: %#v", got)
	}
}

func TestCollectionConfigYAMLAndJSONPreserveNilAndEmpty(t *testing.T) {
	type document struct {
		Auth AuthConfig      `yaml:"auth" json:"auth"`
		WS   WebSocketConfig `yaml:"ws" json:"ws"`
	}
	for _, test := range []struct {
		name       string
		contents   string
		decode     func([]byte, any) error
		wantNil    bool
		wantValues bool
	}{
		{name: "YAML omitted", contents: "{}\n", decode: yaml.Unmarshal, wantNil: true},
		{name: "YAML null", contents: "auth:\n  public_paths: null\nws:\n  allowed_origins: null\n", decode: yaml.Unmarshal, wantNil: true},
		{name: "YAML empty", contents: "auth:\n  public_paths: []\nws:\n  allowed_origins: []\n", decode: yaml.Unmarshal},
		{name: "YAML values", contents: "auth:\n  public_paths: [/public]\nws:\n  allowed_origins: [https://app.example.com]\n", decode: yaml.Unmarshal, wantValues: true},
		{name: "JSON omitted", contents: `{}`, decode: json.Unmarshal, wantNil: true},
		{name: "JSON null", contents: `{"auth":{"public_paths":null},"ws":{"allowed_origins":null}}`, decode: json.Unmarshal, wantNil: true},
		{name: "JSON empty", contents: `{"auth":{"public_paths":[]},"ws":{"allowed_origins":[]}}`, decode: json.Unmarshal},
		{name: "JSON values", contents: `{"auth":{"public_paths":["/public"]},"ws":{"allowed_origins":["https://app.example.com"]}}`, decode: json.Unmarshal, wantValues: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got document
			if err := test.decode([]byte(test.contents), &got); err != nil {
				t.Fatal(err)
			}
			publicPaths := got.Auth.GetPublicPaths()
			allowedOrigins := got.WS.GetAllowedOrigins()
			if test.wantNil {
				if publicPaths != nil || allowedOrigins != nil {
					t.Fatalf("nil collections decoded as public=%#v origins=%#v", publicPaths, allowedOrigins)
				}
				return
			}
			if publicPaths == nil || allowedOrigins == nil {
				t.Fatalf("explicit collections decoded as public=%#v origins=%#v", publicPaths, allowedOrigins)
			}
			if test.wantValues && (!reflect.DeepEqual(publicPaths, []string{"/public"}) || !reflect.DeepEqual(allowedOrigins, []string{"https://app.example.com"})) {
				t.Fatalf("decoded values public=%#v origins=%#v", publicPaths, allowedOrigins)
			}
		})
	}
}

func TestNewSysConfigCollectionDefaultsUseDefensiveGetters(t *testing.T) {
	config := NewSysConfig()
	paths := config.Auth.GetPublicPaths()
	if len(paths) == 0 {
		t.Fatal("default public paths are empty")
	}
	paths[0] = "/mutated"
	if config.Auth.GetPublicPaths()[0] == "/mutated" {
		t.Fatal("default public paths getter exposed internal storage")
	}
	if origins := config.WS.GetAllowedOrigins(); origins != nil {
		t.Fatalf("default allowed origins = %#v, want nil", origins)
	}
}
