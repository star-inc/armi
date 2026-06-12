package main

import (
	"github.com/spf13/cobra"
)

func newClientHealthCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check Armi server health",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := buildArmiClient(cmd)
			if err != nil {
				return err
			}
			result, err := client.Health(cmd.Context())
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
}
