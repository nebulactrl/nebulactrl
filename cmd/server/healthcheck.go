package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// healthcheckCmd probes the local server's /readyz. It exists because the
// runtime image ships no curl: Docker's HEALTHCHECK and
// compose use `nebulactrl-server healthcheck` instead. It deliberately does not
// load the full configuration (no secret key needed).
func healthcheckCmd() *cobra.Command {
	var (
		url     string
		timeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "healthcheck",
		Short: "Probe the local server (/readyz); exit 0 when ready",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if url == "" {
				url = "http://" + probeAddr(os.Getenv("NEBULACTRL_LISTEN")) + "/readyz"
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				return err
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("%s returned %s", url, resp.Status)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "URL to probe (default: http://127.0.0.1:<port of NEBULACTRL_LISTEN>/readyz)")
	cmd.Flags().DurationVar(&timeout, "timeout", 3*time.Second, "request timeout")
	return cmd
}

// probeAddr turns a listen address (":8080", "0.0.0.0:8080", "[::]:8080",
// "10.0.0.5:9000") into a host:port reachable from inside the container.
func probeAddr(listen string) string {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return "127.0.0.1:8080"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "127.0.0.1:8080"
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
