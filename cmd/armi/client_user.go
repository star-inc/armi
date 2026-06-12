package main

import (
	"fmt"
	"strings"

	"github.com/star-inc/armi/pkgs/contract"
	"github.com/spf13/cobra"
)

func newClientUserCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage Armi users",
	}
	cmd.AddCommand(
		newClientUserRegisterCommand(),
		newClientUserMeCommand(),
		newClientUserUpdateCommand(),
	)
	return cmd
}

func newClientUserRegisterCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Register a local Armi user",
		RunE: func(cmd *cobra.Command, _ []string) error {
			account, _ := cmd.Flags().GetString("account")
			password, _ := cmd.Flags().GetString("account-password")
			if strings.TrimSpace(account) == "" || password == "" {
				return fmt.Errorf("account and account-password are required")
			}

			client, err := buildArmiClient(cmd)
			if err != nil {
				return err
			}
			result, err := client.RegisterUser(cmd.Context(), contract.RegisterRequest{
				Username: account,
				Password: password,
			})
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().String("account", "", "Username for the new account")
	cmd.Flags().String("account-password", "", "Password for the new account")
	return cmd
}

func newClientUserMeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "me",
		Short: "Get the authenticated user profile",
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := buildArmiClient(cmd)
			if err != nil {
				return err
			}
			result, err := client.GetMe(cmd.Context())
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
}

func newClientUserUpdateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update the authenticated user profile",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var req contract.UpdateMeRequest
			if cmd.Flags().Changed("new-username") {
				value, _ := cmd.Flags().GetString("new-username")
				req.Username = &value
			}
			if cmd.Flags().Changed("new-password") {
				value, _ := cmd.Flags().GetString("new-password")
				req.Password = &value
			}
			if req.Username == nil && req.Password == nil {
				return fmt.Errorf("at least one of new-username or new-password is required")
			}

			client, err := buildArmiClient(cmd)
			if err != nil {
				return err
			}
			result, err := client.UpdateMe(cmd.Context(), req)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
	cmd.Flags().String("new-username", "", "New username")
	cmd.Flags().String("new-password", "", "New password")
	return cmd
}
