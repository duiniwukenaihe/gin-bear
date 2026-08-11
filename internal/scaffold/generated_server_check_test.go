package scaffold

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type generatedServerCheckConfig struct {
	startupTimeout  time.Duration
	requestTimeout  time.Duration
	shutdownTimeout time.Duration
	cleanupTimeout  time.Duration
	closeTimeout    time.Duration
	retryInterval   time.Duration
	maxBodyBytes    int64
	maxOutputBytes  int64
}

func defaultGeneratedServerCheckConfig() generatedServerCheckConfig {
	return generatedServerCheckConfig{
		startupTimeout:  30 * time.Second,
		requestTimeout:  2 * time.Second,
		shutdownTimeout: 10 * time.Second,
		cleanupTimeout:  3 * time.Second,
		closeTimeout:    3 * time.Second,
		retryInterval:   100 * time.Millisecond,
		maxBodyBytes:    64 << 10,
		maxOutputBytes:  1 << 20,
	}
}

func checkGeneratedServer(cmd *exec.Cmd, url string, config generatedServerCheckConfig) (output string, err error) {
	stdout := newBoundedBuffer(config.maxOutputBytes)
	stderr := newBoundedBuffer(config.maxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start generated server: %w", err)
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	waited := false
	defer func() {
		if !waited {
			cleanupErr := terminateAndWait(cmd.Process, waitDone, config.cleanupTimeout)
			if cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
			waited = cleanupErr == nil
		}
		output = stdout.String() + stderr.String()
	}()

	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: config.requestTimeout}).DialContext,
		DisableKeepAlives:     true,
		ResponseHeaderTimeout: config.requestTimeout,
		TLSHandshakeTimeout:   config.requestTimeout,
	}
	client := &http.Client{Transport: transport, Timeout: config.requestTimeout}
	defer transport.CloseIdleConnections()

	startupDeadline := time.Now().Add(config.startupTimeout)
	for {
		remaining := time.Until(startupDeadline)
		if remaining <= 0 {
			return "", fmt.Errorf("generated server did not become healthy within %s", config.startupTimeout)
		}
		requestTimeout := minDuration(config.requestTimeout, remaining)
		status, body, requestErr := boundedHTTPGet(client, url, requestTimeout, config.maxBodyBytes)
		if requestErr == nil {
			if status != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
				return "", fmt.Errorf("GET health endpoint = %d %s", status, body)
			}
			break
		}

		select {
		case waitErr := <-waitDone:
			waited = true
			if waitErr != nil {
				return "", fmt.Errorf("generated server exited before becoming healthy: %w", waitErr)
			}
			return "", errors.New("generated server exited before becoming healthy")
		default:
		}
		if time.Now().Add(config.retryInterval).After(startupDeadline) {
			continue
		}
		timer := time.NewTimer(config.retryInterval)
		select {
		case waitErr := <-waitDone:
			timer.Stop()
			waited = true
			if waitErr != nil {
				return "", fmt.Errorf("generated server exited before becoming healthy: %w", waitErr)
			}
			return "", errors.New("generated server exited before becoming healthy")
		case <-timer.C:
		}
	}

	if err := cancelGeneratedProcess(cmd.Process.Pid); err != nil {
		return "", fmt.Errorf("cancel generated server: %w", err)
	}
	waitErr, ok := waitForProcess(waitDone, config.shutdownTimeout)
	if !ok {
		return "", fmt.Errorf("generated server did not exit within %s after cancellation", config.shutdownTimeout)
	}
	waited = true
	if waitErr != nil {
		return "", fmt.Errorf("generated server exited with an error after cancellation: %w", waitErr)
	}
	if serverOutput := stdout.String() + stderr.String(); !strings.Contains(serverOutput, "WhiteBear returning to ice") {
		return "", errors.New("generated server skipped graceful cancellation")
	}

	closeDeadline := time.Now().Add(config.closeTimeout)
	for {
		remaining := time.Until(closeDeadline)
		if remaining <= 0 {
			return "", fmt.Errorf("generated server still answers after cancellation: %s", url)
		}
		_, _, requestErr := boundedHTTPGet(client, url, minDuration(config.requestTimeout, remaining), config.maxBodyBytes)
		if requestErr != nil {
			return "", nil
		}
		timer := time.NewTimer(minDuration(config.retryInterval, remaining))
		<-timer.C
	}
}

func boundedHTTPGet(client *http.Client, url string, timeout time.Duration, maxBodyBytes int64) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Close = true
	resp, err := client.Do(req) //nolint:gosec // loopback smoke test
	if err != nil {
		return 0, nil, err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	cancel()
	closeErr := resp.Body.Close()
	if readErr != nil {
		return 0, nil, readErr
	}
	if closeErr != nil {
		return 0, nil, closeErr
	}
	if int64(len(body)) > maxBodyBytes {
		return 0, nil, fmt.Errorf("response body exceeds %d bytes", maxBodyBytes)
	}
	return resp.StatusCode, body, nil
}

func terminateAndWait(process *os.Process, waitDone <-chan error, timeout time.Duration) error {
	var killErr error
	if process != nil {
		if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			killErr = fmt.Errorf("kill generated server: %w", err)
		}
	}
	if _, ok := waitForProcess(waitDone, timeout); !ok {
		return errors.Join(killErr, fmt.Errorf("generated server was not reaped within %s", timeout))
	}
	return killErr
}

func waitForProcess(waitDone <-chan error, timeout time.Duration) (error, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-waitDone:
		return err, true
	case <-timer.C:
		return nil, false
	}
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

type boundedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	remaining int64
}

func newBoundedBuffer(limit int64) *boundedBuffer {
	return &boundedBuffer{remaining: limit}
}

func (buffer *boundedBuffer) Write(contents []byte) (int, error) {
	written := len(contents)
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.remaining <= 0 {
		return written, nil
	}
	if int64(len(contents)) > buffer.remaining {
		contents = contents[:buffer.remaining]
	}
	_, _ = buffer.buffer.Write(contents)
	buffer.remaining -= int64(len(contents))
	return written, nil
}

func (buffer *boundedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}
