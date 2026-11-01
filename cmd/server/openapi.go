package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/app"
	"github.com/nebulactrl/nebulactrl/internal/config"
)

func openapiCmd() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "openapi",
		Short: "Write the OpenAPI 3.1 document (no database or secrets required)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.Build(&config.Config{}, nil, nil)
			if err != nil {
				return err
			}
			defer a.Close()
			doc, err := json.MarshalIndent(a.API.OpenAPI(), "", "  ")
			if err != nil {
				return fmt.Errorf("marshal openapi: %w", err)
			}
			doc = append(doc, '\n')
			if out == "-" {
				_, err = cmd.OutOrStdout().Write(doc)
				return err
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil { //nolint:gosec // the spec is public documentation
				return err
			}
			if err := os.WriteFile(out, doc, 0o644); err != nil { //nolint:gosec // the spec is public documentation
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s (%d bytes)\n", out, len(doc))
			return nil
		},
	}
	cmd.Flags().StringVarP(&out, "out", "o", "openapi/openapi.json", "output file, or - for stdout")
	return cmd
}
