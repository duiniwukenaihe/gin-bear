package bear

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestOpenGormAdapterHonorsStartupContext(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- connection
		}
	}()
	t.Cleanup(func() {
		select {
		case connection := <-accepted:
			_ = connection.Close()
		default:
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	adapter, err := OpenGormAdapter(ctx, &DBConfig{
		Enabled: true,
		Type:    "mysql",
		Host:    "127.0.0.1",
		Port:    strconv.Itoa(listener.Addr().(*net.TCPAddr).Port),
		User:    "test",
		DBName:  "test",
	})
	if adapter != nil {
		_ = adapter.Shutdown()
		t.Fatal("OpenGormAdapter() returned an adapter for an unresponsive database")
	}
	if err == nil {
		t.Fatal("OpenGormAdapter() error = nil, want context deadline failure")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("OpenGormAdapter() elapsed = %s, want startup context to bound the connection", elapsed)
	}
}
