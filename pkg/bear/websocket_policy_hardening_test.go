package bear

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestProductionRejectsWildcardWebSocketOrigin(t *testing.T) {
	tests := []struct {
		name    string
		origins []string
	}{
		{name: "wildcard only", origins: []string{"*"}},
		{name: "wildcard after explicit origin", origins: []string{"https://app.example.com", "*"}},
		{name: "wildcard with whitespace", origins: []string{"  *  "}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewSysConfig()
			config.WS.SetAllowedOrigins(tt.origins)

			err := validateProductionWebSocketPolicy(config)
			if err == nil || !strings.Contains(err.Error(), "websocket.allowed_origins") {
				t.Fatalf("validateProductionWebSocketPolicy() error = %v, want wildcard origin rejection", err)
			}
		})
	}
}

func TestProductionRejectsUnsafeTrustedProxy(t *testing.T) {
	tests := []struct {
		name    string
		proxies []string
		wantErr bool
	}{
		{name: "empty list"},
		{name: "IPv4 address", proxies: []string{"127.0.0.1"}},
		{name: "IPv4 prefix", proxies: []string{"10.0.0.0/8"}},
		{name: "IPv6 address", proxies: []string{"2001:db8::1"}},
		{name: "IPv6 prefix", proxies: []string{"2001:db8::/32"}},
		{name: "IPv4 zero prefix", proxies: []string{"0.0.0.0/0"}, wantErr: true},
		{name: "normalized IPv4 zero prefix", proxies: []string{"192.0.2.42/0"}, wantErr: true},
		{name: "IPv6 zero prefix", proxies: []string{"::/0"}, wantErr: true},
		{name: "normalized IPv6 zero prefix", proxies: []string{"2001:db8::42/0"}, wantErr: true},
		{name: "invalid proxy", proxies: []string{"not-an-address"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewSysConfig()
			config.Server.TrustedProxies = tt.proxies

			err := validateProductionTrustedProxies(config)
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "server.trusted_proxies") {
					t.Fatalf("validateProductionTrustedProxies() error = %v, want trusted proxy rejection", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateProductionTrustedProxies() error = %v", err)
			}
		})
	}
}

func TestProductionWebSocketPolicyBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		apply   func(*SysConfig)
		wantErr string
	}{
		{
			name:  "defaults",
			apply: func(*SysConfig) {},
		},
		{
			name:  "handshake lower boundary",
			apply: func(config *SysConfig) { config.WS.HandshakeTimeout = 100 },
		},
		{
			name:  "handshake upper boundary",
			apply: func(config *SysConfig) { config.WS.HandshakeTimeout = 30000 },
		},
		{
			name:    "handshake below lower boundary",
			apply:   func(config *SysConfig) { config.WS.HandshakeTimeout = 99 },
			wantErr: "websocket.handshake_timeout_ms",
		},
		{
			name:    "handshake above upper boundary",
			apply:   func(config *SysConfig) { config.WS.HandshakeTimeout = 30001 },
			wantErr: "websocket.handshake_timeout_ms",
		},
		{
			name:  "message lower boundary",
			apply: func(config *SysConfig) { config.Config["websocket.max_message_bytes"] = int64(1) },
		},
		{
			name:  "message upper boundary",
			apply: func(config *SysConfig) { config.Config["websocket.max_message_bytes"] = int64(16 << 20) },
		},
		{
			name:    "message below lower boundary",
			apply:   func(config *SysConfig) { config.Config["websocket.max_message_bytes"] = int64(0) },
			wantErr: "websocket.max_message_bytes",
		},
		{
			name:    "message above upper boundary",
			apply:   func(config *SysConfig) { config.Config["websocket.max_message_bytes"] = int64((16 << 20) + 1) },
			wantErr: "websocket.max_message_bytes",
		},
		{
			name:  "read upper boundary",
			apply: func(config *SysConfig) { config.Config["websocket.read_timeout"] = "5m" },
		},
		{
			name:    "read below lower boundary",
			apply:   func(config *SysConfig) { config.Config["websocket.read_timeout"] = "999ms" },
			wantErr: "websocket.read_timeout",
		},
		{
			name:    "read above upper boundary",
			apply:   func(config *SysConfig) { config.Config["websocket.read_timeout"] = "5m1ms" },
			wantErr: "websocket.read_timeout",
		},
		{
			name:  "write lower boundary",
			apply: func(config *SysConfig) { config.Config["websocket.write_timeout"] = "100ms" },
		},
		{
			name:  "write upper boundary",
			apply: func(config *SysConfig) { config.Config["websocket.write_timeout"] = "1m" },
		},
		{
			name:    "write below lower boundary",
			apply:   func(config *SysConfig) { config.Config["websocket.write_timeout"] = "99ms" },
			wantErr: "websocket.write_timeout",
		},
		{
			name:    "write above upper boundary",
			apply:   func(config *SysConfig) { config.Config["websocket.write_timeout"] = "1m1ms" },
			wantErr: "websocket.write_timeout",
		},
		{
			name: "ping near effective upper boundary",
			apply: func(config *SysConfig) {
				config.Config["websocket.read_timeout"] = "5m"
				config.Config["websocket.ping_interval"] = "4m59s"
			},
		},
		{
			name:    "ping below lower boundary",
			apply:   func(config *SysConfig) { config.Config["websocket.ping_interval"] = "999ms" },
			wantErr: "websocket.ping_interval",
		},
		{
			name:    "ping above upper boundary",
			apply:   func(config *SysConfig) { config.Config["websocket.ping_interval"] = "5m1ms" },
			wantErr: "websocket.ping_interval",
		},
		{
			name:  "connection lower boundary",
			apply: func(config *SysConfig) { config.Config["websocket.max_connections"] = int64(1) },
		},
		{
			name:  "connection upper boundary",
			apply: func(config *SysConfig) { config.Config["websocket.max_connections"] = int64(100000) },
		},
		{
			name:    "connection below lower boundary",
			apply:   func(config *SysConfig) { config.Config["websocket.max_connections"] = int64(0) },
			wantErr: "websocket.max_connections",
		},
		{
			name:    "connection above upper boundary",
			apply:   func(config *SysConfig) { config.Config["websocket.max_connections"] = int64(100001) },
			wantErr: "websocket.max_connections",
		},
		{
			name: "ping equals read timeout",
			apply: func(config *SysConfig) {
				config.Config["websocket.read_timeout"] = "30s"
				config.Config["websocket.ping_interval"] = "30s"
			},
			wantErr: "websocket.ping_interval",
		},
		{
			name: "ping exceeds read timeout",
			apply: func(config *SysConfig) {
				config.Config["websocket.read_timeout"] = "30s"
				config.Config["websocket.ping_interval"] = "31s"
			},
			wantErr: "websocket.ping_interval",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := NewSysConfig()
			tt.apply(config)

			err := validateProductionWebSocketPolicy(config)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateProductionWebSocketPolicy() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateProductionWebSocketPolicy() error = %v, want %s rejection", err, tt.wantErr)
			}
		})
	}
}

func TestWebSocketPolicyConnectionDefaultsAndOverride(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	t.Setenv("GIN_MODE", "")

	development := NewSysConfig()
	assertWebSocketConnectionLimit(t, development, 0)

	strict := NewSysConfig()
	strict.SetFrameworkStrict(true)
	assertWebSocketConnectionLimit(t, strict, 1024)

	production := NewSysConfig()
	production.Server.Mode = "release"
	assertWebSocketConnectionLimit(t, production, 1024)

	overridden := NewSysConfig()
	overridden.Config["websocket.max_connections"] = int64(7)
	assertWebSocketConnectionLimit(t, overridden, 7)

	defaults := webSocketPolicyForConfig(development)
	if defaults.maxMessageBytes != 1<<20 ||
		defaults.readTimeout != 60*time.Second ||
		defaults.writeTimeout != 10*time.Second ||
		defaults.pingInterval != 30*time.Second {
		t.Fatalf("websocket defaults = %+v", defaults)
	}
}

func TestWebSocketMaxConnectionsValidationAppliesInDevelopment(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	t.Setenv("GIN_MODE", "")

	for _, value := range []any{0, -1, 100001, "invalid"} {
		t.Run(fmt.Sprint(value), func(t *testing.T) {
			config := NewSysConfig()
			config.Config["websocket.max_connections"] = value

			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), "websocket.max_connections") {
				t.Fatalf("Validate() error = %v for value %#v, want max_connections rejection", err, value)
			}
		})
	}
}

func assertWebSocketConnectionLimit(t *testing.T, config *SysConfig, want int64) {
	t.Helper()
	if got := webSocketPolicyForConfig(config).maxConnections; got != want {
		t.Fatalf("maxConnections = %d, want %d", got, want)
	}
}
