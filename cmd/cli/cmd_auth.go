package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newLoginCmd(c *cli) *cobra.Command {
	var url, token string
	cmd := &cobra.Command{
		Use:   "login --url <url> --token <token>",
		Short: "Store the control plane URL and API token",
		Long: "Verifies the token against GET /me and writes ~/.config/nebulactrl/config.json (mode 0600).\n" +
			"Pass --token - to read the token from stdin; NEBULACTRL_TOKEN is used when --token is omitted.\n" +
			"The token travels with every command, so the URL must be https://. Plain http is accepted for this machine only, or elsewhere with --insecure.\n" +
			"Use a REST API token (Account → API tokens); an MCP token is for agents and is refused here.",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if url == "" {
				url = os.Getenv(envURL)
			}
			if url == "" {
				return usagef("--url is required")
			}
			switch token {
			case "-":
				raw, err := io.ReadAll(c.in)
				if err != nil {
					return fmt.Errorf("read token from stdin: %w", err)
				}
				token = string(raw)
			case "":
				token = os.Getenv(envToken)
			}
			token = strings.TrimSpace(token)
			if token == "" {
				return usagef("--token is required")
			}
			cfg := config{URL: strings.TrimRight(strings.TrimSpace(url), "/"), Token: token}
			if err := c.dial(cfg); err != nil {
				return err
			}
			ctx, cancel := c.ctx(cmd.Context())
			defer cancel()
			r, err := c.api.GetMeWithResponse(ctx)
			if err := check(r, err); err != nil {
				var ae *apiError
				if errors.As(err, &ae) && ae.Status == 401 {
					return errors.New("the control plane rejected the token (401): check the token and that it has not been revoked")
				}
				if errors.As(err, &ae) && ae.Code == "token_kind_mismatch" {
					return errors.New("this is an MCP token, which works only on /mcp: create a REST API token (Account → API tokens) for the CLI")
				}
				return err
			}
			// Remember the token under its team so `teams switch` can return to it.
			if old, err := loadConfig(); err == nil && old.URL == cfg.URL {
				cfg.Profiles = old.Profiles
			}
			if tr, err := c.api.ListTeamsWithResponse(ctx); check(tr, err) == nil {
				for _, t := range deref(tr.JSON200.Items) {
					if t.Current {
						if cfg.Profiles == nil {
							cfg.Profiles = map[string]string{}
						}
						cfg.Team, cfg.Profiles[t.Slug] = t.Slug, cfg.Token
					}
				}
			}
			path, err := saveConfig(cfg)
			if err != nil {
				return err
			}
			if c.asJSON {
				return c.printJSON(map[string]any{"url": cfg.URL, "email": r.JSON200.Email, "name": r.JSON200.Name, "config": path})
			}
			c.info("Logged in to %s as %s", cfg.URL, r.JSON200.Email)
			c.info("Saved %s", path)
			return nil
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "control plane URL, e.g. https://nebula.example.com")
	cmd.Flags().StringVar(&token, "token", "", "API token (nct_…); - reads stdin")
	return cmd
}
