package bear

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/credentials"
)

const (
	maxGRPCMessageBytes      = 64 << 20
	maxGRPCConcurrentStreams = 65535
)

func validateGRPCConfig(config *SysConfig) error {
	if config == nil || config.GRPC == nil || !config.GRPC.Enabled {
		return nil
	}
	grpcConfig := config.GRPC
	if strings.TrimSpace(grpcConfig.Host) == "" {
		return fmt.Errorf("grpc.host must not be empty")
	}
	if grpcConfig.Port < 1 || grpcConfig.Port > 65535 {
		return fmt.Errorf("grpc.port must be between 1 and 65535")
	}
	if config.Server != nil && grpcConfig.Port == config.Server.Port {
		return fmt.Errorf("grpc.port must differ from server.port")
	}
	if grpcConfig.MaxRecvMessageBytes < 1 || grpcConfig.MaxRecvMessageBytes > maxGRPCMessageBytes {
		return fmt.Errorf("grpc.max_recv_message_bytes must be between 1 and %d", maxGRPCMessageBytes)
	}
	if grpcConfig.MaxSendMessageBytes < 1 || grpcConfig.MaxSendMessageBytes > maxGRPCMessageBytes {
		return fmt.Errorf("grpc.max_send_message_bytes must be between 1 and %d", maxGRPCMessageBytes)
	}
	if grpcConfig.MaxConcurrentStreams < 1 || grpcConfig.MaxConcurrentStreams > maxGRPCConcurrentStreams {
		return fmt.Errorf("grpc.max_concurrent_streams must be between 1 and %d", maxGRPCConcurrentStreams)
	}
	for _, value := range []struct {
		name string
		raw  string
		max  time.Duration
	}{
		{name: "grpc.keepalive_min_time", raw: grpcConfig.KeepaliveMinTime, max: 24 * time.Hour},
		{name: "grpc.keepalive_time", raw: grpcConfig.KeepaliveTime, max: 24 * time.Hour},
		{name: "grpc.keepalive_timeout", raw: grpcConfig.KeepaliveTimeout, max: 5 * time.Minute},
		{name: "grpc.max_connection_idle", raw: grpcConfig.MaxConnectionIdle, max: 24 * time.Hour},
		{name: "grpc.max_connection_age", raw: grpcConfig.MaxConnectionAge, max: 24 * time.Hour},
		{name: "grpc.max_connection_age_grace", raw: grpcConfig.MaxConnectionAgeGrace, max: 5 * time.Minute},
	} {
		if err := validatePositiveGRPCDuration(value.name, value.raw, value.max); err != nil {
			return err
		}
	}

	transport := strings.ToLower(strings.TrimSpace(grpcConfig.TransportSecurity))
	if transport == "" {
		if isProductionMode(config) {
			return fmt.Errorf("grpc.transport_security must be explicit in production")
		}
		transport = "plaintext"
	}
	switch transport {
	case "plaintext":
		if isProductionMode(config) && !isLoopbackHost(grpcConfig.Host) {
			return fmt.Errorf("production plaintext gRPC must bind to a loopback host")
		}
	case "tls", "mtls":
		if strings.TrimSpace(grpcConfig.TLSCertFile) == "" {
			return fmt.Errorf("grpc.tls_cert_file is required for %s", transport)
		}
		if strings.TrimSpace(grpcConfig.TLSKeyFile) == "" {
			return fmt.Errorf("grpc.tls_key_file is required for %s", transport)
		}
		if transport == "mtls" && strings.TrimSpace(grpcConfig.ClientCAFile) == "" {
			return fmt.Errorf("grpc.client_ca_file is required for mtls")
		}
	default:
		return fmt.Errorf("grpc.transport_security must be one of plaintext, tls, mtls")
	}
	return nil
}

func validatePositiveGRPCDuration(name, raw string, maximum time.Duration) error {
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("%s must be a valid positive duration: %w", name, err)
	}
	if duration <= 0 || duration > maximum {
		return fmt.Errorf("%s must be greater than zero and at most %s", name, maximum)
	}
	return nil
}

func grpcListenAddress(config *GRPCConfig) string {
	if config == nil {
		return ""
	}
	return net.JoinHostPort(config.Host, strconv.Itoa(int(config.Port)))
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(strings.ToLower(host)), "[]")
	if host == "localhost" {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func grpcTransportCredentials(config *SysConfig) (credentials.TransportCredentials, error) {
	if err := validateGRPCConfig(config); err != nil {
		return nil, err
	}
	grpcConfig := config.GRPC
	transport := strings.ToLower(strings.TrimSpace(grpcConfig.TransportSecurity))
	if transport == "" || transport == "plaintext" {
		return nil, nil
	}
	certificate, err := tls.LoadX509KeyPair(grpcConfig.TLSCertFile, grpcConfig.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load gRPC TLS certificate: %w", err)
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
	}
	if transport == "mtls" {
		clientCA, err := os.ReadFile(grpcConfig.ClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("read gRPC client CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(clientCA) {
			return nil, fmt.Errorf("parse gRPC client CA: no certificates found")
		}
		tlsConfig.ClientCAs = pool
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return credentials.NewTLS(tlsConfig), nil
}
