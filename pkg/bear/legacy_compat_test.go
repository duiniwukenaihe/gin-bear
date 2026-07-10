package bear

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
)

type legacyService struct{}

func (s *legacyService) Name() string {
	return "legacyService"
}

type legacyController struct{}

func (c *legacyController) Name() string {
	return "legacyController"
}

func (c *legacyController) Build(*Bear) {}

func TestLegacyV091SurfaceStillCompiles(t *testing.T) {
	resetTestInjector()
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	app := Ignite(cfg)
	app.Beans(&legacyService{})
	app.Mount("/api", &legacyController{})
	app.Attach(NewAuthFairing())
	app.EnableHealth().EnableMetrics().EnableGzip()
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCompatibilityWarningsAreLoggedOnceDuringIgnite(t *testing.T) {
	resetTestInjector()
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Waf.Enabled = true
	cfg.GeoIP.Enabled = true
	cfg.BigQuery.Enabled = true
	cfg.MQ.Enabled = true
	cfg.Kafka.Enabled = true
	cfg.RocketMQ.Enabled = true
	cfg.Pulsar.Enabled = true
	cfg.Schema.Enabled = true
	cfg.CircuitBreaker.Enabled = true
	cfg.ConfigCenter.Enabled = true

	output := captureStandardOutput(t, func() {
		Ignite(cfg)
	})

	for _, warning := range []string{
		"waf is compatibility-only and is not started",
		"geoip is compatibility-only and is not loaded",
		"bigquery is compatibility-only and is not started",
		"mq is compatibility-only and is not started",
		"kafka is compatibility-only and is not started",
		"rocketmq is compatibility-only and is not started",
		"pulsar is compatibility-only and is not started",
		"schema is compatibility-only and is not loaded",
		"circuit_breaker is compatibility-only and is not started",
		"config_center is compatibility-only and is not loaded",
	} {
		if count := strings.Count(output, `"msg":"`+warning+`"`); count != 1 {
			t.Errorf("warning %q logged %d times; output: %s", warning, count, output)
		}
	}
}

func captureStandardOutput(t *testing.T, fn func()) string {
	t.Helper()
	previous := os.Stdout
	previousLogger := slog.Default()
	previousLog := Log
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() {
		os.Stdout = previous
		slog.SetDefault(previousLogger)
		Log = previousLog
	})

	output := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		output <- data
	}()

	fn()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := <-output
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return string(bytes.TrimSpace(data))
}
