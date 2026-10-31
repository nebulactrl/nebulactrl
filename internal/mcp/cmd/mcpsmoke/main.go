// Command mcpsmoke connects to a NebulaCtrl MCP endpoint with the official SDK
// client and checks the three things a release gate needs:
//
//  1. the default listing (toolsets core + observe) and get_overview,
//
//  2. a session narrowed with ?toolsets=core,config that lists the services of
//     an environment through project_list, environment_list and service_list,
//
//  3. the production guard: apply_changes on a production environment must not
//     deploy without an approval (the server answers confirmation_required with
//     the URL of the dashboard page where a person approves it).
//
//     NEBULACTRL_URL=http://localhost:8080 NEBULACTRL_TOKEN=nct_… go run ./internal/mcp/cmd/mcpsmoke
//
// NEBULACTRL_ENVIRONMENT_ID skips the environment discovery. Any failure exits
// non-zero.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mcpsmoke:", err)
		os.Exit(1)
	}
}

type bearerTransport struct {
	token string
	next  http.RoundTripper
}

func (t bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(r)
}

func run() error {
	base := strings.TrimRight(os.Getenv("NEBULACTRL_URL"), "/")
	token := os.Getenv("NEBULACTRL_TOKEN")
	if base == "" || token == "" {
		return errors.New("NEBULACTRL_URL and NEBULACTRL_TOKEN are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// 1. Default toolsets.
	session, err := connect(ctx, base+"/mcp", token)
	if err != nil {
		return err
	}
	defer session.Close()
	if _, err := listTools(ctx, session, "default"); err != nil {
		return err
	}
	overview, err := call(ctx, session, "get_overview", map[string]any{})
	if err != nil {
		return err
	}
	printJSON("get_overview", overview)

	// 2. A session narrowed to core + config.
	configSession, err := connect(ctx, base+"/mcp?toolsets=core,config", token)
	if err != nil {
		return err
	}
	defer configSession.Close()
	if _, err := listTools(ctx, configSession, "core,config"); err != nil {
		return err
	}
	envID := os.Getenv("NEBULACTRL_ENVIRONMENT_ID")
	prodEnvID := ""
	if envID == "" {
		if envID, prodEnvID, err = discoverEnvironment(ctx, configSession); err != nil {
			return err
		}
	}
	services, err := call(ctx, configSession, "service_list", map[string]any{"environmentId": envID})
	if err != nil {
		return err
	}
	printJSON("service_list("+envID+")", services)

	// 3. The production guard: stage a harmless variable on a production
	// service, try to apply it, expect the server to hold it, then unstage.
	if prodEnvID == "" {
		fmt.Println("production guard: skipped (no production environment visible)")
		return nil
	}
	var prodServices struct {
		Items []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"items"`
	}
	if err := callInto(ctx, configSession, "service_list", map[string]any{"environmentId": prodEnvID}, &prodServices); err != nil {
		return err
	}
	if len(prodServices.Items) == 0 {
		fmt.Println("production guard: skipped (the production environment has no services)")
		return nil
	}
	target := prodServices.Items[0]
	const probeKey = "MCPSMOKE_PROBE"
	var staged struct {
		Generation int64 `json:"generation"`
		Production bool  `json:"production"`
	}
	if err := callInto(ctx, session, "stage_changes", map[string]any{
		"service":   target.ID,
		"variables": []map[string]any{{"key": probeKey, "value": "1"}},
	}, &staged); err != nil {
		return err
	}
	defer func() {
		// Unstage the probe whatever happened; the variable was never deployed.
		if _, err := call(context.Background(), session, "stage_changes", map[string]any{
			"service":   target.ID,
			"variables": []map[string]any{{"key": probeKey, "delete": true}},
		}); err != nil {
			fmt.Fprintln(os.Stderr, "mcpsmoke: could not unstage the probe variable:", err)
		}
	}()
	if !staged.Production {
		return fmt.Errorf("stage_changes on %s did not report a production service", target.Slug)
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "apply_changes", Arguments: map[string]any{
		"services": []map[string]any{{"service": target.ID, "expectedGeneration": staged.Generation}},
	}})
	if err != nil {
		return fmt.Errorf("apply_changes: %w", err)
	}
	text := textOf(res)
	if !res.IsError || !(strings.Contains(text, "confirmation_required") || strings.Contains(text, "approval")) {
		return fmt.Errorf("apply_changes on production was not held for approval: %s", firstLine(text))
	}
	fmt.Println("production guard: held, " + firstLine(text))
	return nil
}

func connect(ctx context.Context, endpoint, token string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "nebulactrl-mcpsmoke", Version: "0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: bearerTransport{token: token, next: http.DefaultTransport}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", endpoint, err)
	}
	return session, nil
}

func listTools(ctx context.Context, s *mcp.ClientSession, label string) ([]string, error) {
	tools, err := s.ListTools(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("list tools (%s): %w", label, err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, t := range tools.Tools {
		names = append(names, t.Name)
	}
	fmt.Printf("tools %s (%d): %s\n", label, len(names), strings.Join(names, ", "))
	return names, nil
}

func printJSON(label string, v any) {
	pretty, _ := json.MarshalIndent(v, "", "  ")
	fmt.Printf("%s:\n%s\n", label, pretty)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// discoverEnvironment returns the first environment of the first project that
// has one, and the first production environment it sees (empty if none).
func discoverEnvironment(ctx context.Context, s *mcp.ClientSession) (string, string, error) {
	var projects struct {
		Items []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := callInto(ctx, s, "project_list", map[string]any{}, &projects); err != nil {
		return "", "", err
	}
	first, prod := "", ""
	for _, p := range projects.Items {
		var envs struct {
			Items []struct {
				ID           string `json:"id"`
				IsProduction bool   `json:"isProduction"`
			} `json:"items"`
		}
		if err := callInto(ctx, s, "environment_list", map[string]any{"projectId": p.ID}, &envs); err != nil {
			return "", "", err
		}
		for _, e := range envs.Items {
			if first == "" {
				first = e.ID
			}
			if prod == "" && e.IsProduction {
				prod = e.ID
			}
		}
	}
	if first == "" {
		return "", "", errors.New("no project with an environment is visible to this token")
	}
	return first, prod, nil
}

func call(ctx context.Context, s *mcp.ClientSession, tool string, args map[string]any) (any, error) {
	res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	if res.IsError {
		return nil, fmt.Errorf("%s: tool error: %s", tool, textOf(res))
	}
	return res.StructuredContent, nil
}

func callInto(ctx context.Context, s *mcp.ClientSession, tool string, args map[string]any, dst any) error {
	out, err := call(ctx, s, tool, args)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func textOf(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}
