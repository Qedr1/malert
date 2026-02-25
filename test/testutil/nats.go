package testutil

import (
	"net"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// FreePort reserves a local TCP port and returns it to the caller.
// Params: none.
// Returns: free port number or error.
func FreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// StartLocalNATSServer starts local nats-server with JetStream enabled for tests.
// Params: test handle for lifecycle and failure reporting.
// Returns: server URL and stop callback.
func StartLocalNATSServer(tb testing.TB) (string, func()) {
	tb.Helper()

	port, err := FreePort()
	if err != nil {
		tb.Fatalf("free port: %v", err)
	}

	dataDir := tb.TempDir()
	cmd := exec.Command("nats-server", "-js", "-p", strconv.Itoa(port), "-sd", dataDir)
	if err := cmd.Start(); err != nil {
		tb.Skipf("nats-server is required for integration test: %v", err)
	}

	url := "nats://127.0.0.1:" + strconv.Itoa(port)
	WaitForNATSReady(tb, url, 8*time.Second)

	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			if cmd.Process == nil {
				return
			}
			_ = cmd.Process.Signal(syscall.SIGTERM)
			done := make(chan struct{})
			go func() {
				_, _ = cmd.Process.Wait()
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
			}
		})
	}
	return url, stop
}

// WaitForNATSReady waits until a NATS endpoint accepts connections.
// Params: test handle, nats URL, and timeout.
// Returns: endpoint is reachable or test fails.
func WaitForNATSReady(tb testing.TB, url string, timeout time.Duration) {
	tb.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		nc, err := nats.Connect(url)
		if err == nil {
			nc.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	tb.Fatalf("nats did not become ready at %s", url)
}
