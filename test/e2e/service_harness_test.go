package e2e

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"alerting/internal/app"
	"alerting/internal/clock"
	"alerting/internal/config"
)

// newServiceFromConfig creates Service from file config path for e2e scenarios.
// Params: test handle and absolute config path.
// Returns: initialized service instance.
func newServiceFromConfig(t *testing.T, path string) *app.Service {
	t.Helper()

	source, err := config.FromCLI(path, "")
	if err != nil {
		t.Fatalf("config source: %v", err)
	}
	service, err := app.NewService(source, clock.RealClock{})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service
}

// runService starts service in background with cancellable context.
// Params: test handle and initialized service.
// Returns: cancel callback and done channel with Run result.
func runService(t *testing.T, service *app.Service) (context.CancelFunc, <-chan error) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- service.Run(ctx)
	}()
	return cancel, done
}

// waitReady waits for /readyz endpoint to return 200.
// Params: test handle and HTTP port.
// Returns: service is ready or test fails on timeout.
func waitReady(t *testing.T, port int) {
	t.Helper()
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitFor(t, 8*time.Second, func() bool {
		response, err := http.Get(baseURL + "/readyz")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		return response.StatusCode == http.StatusOK
	})
}

// waitServiceStop asserts service Run exits without error after cancellation.
// Params: test handle and done channel returned by runService.
// Returns: test fails if stop timeout/error happens.
func waitServiceStop(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("service run error: %v", runErr)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("service did not stop after cancel")
	}
}
