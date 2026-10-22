package httpserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// probeTimeout bounds a container healthcheck request.
const probeTimeout = 3 * time.Second

// Probe performs a GET against url and returns an error unless the response
// is 200. Binaries expose it as a "healthcheck" subcommand because the
// distroless runtime image ships no curl or wget.
func Probe(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build probe request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("probe %s: %w", url, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe %s: status %d", url, resp.StatusCode)
	}
	return nil
}
