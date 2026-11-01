package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nebulactrl/nebulactrl/internal/secretbox"
)

func keygenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keygen",
		Short: "Print a new NEBULACTRL_SECRET_KEY (base64 of 32 random bytes)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := secretbox.GenerateKey()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), k)
			return nil
		},
	}
}
