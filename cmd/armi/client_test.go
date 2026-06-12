package main

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestClientConfigurationUsesEnvironment(t *testing.T) {
	t.Setenv("ARMI_BASE_URL", "http://env.example")
	t.Setenv("ARMI_USERNAME", "env-user")
	t.Setenv("ARMI_TIMEOUT", "45s")

	values := executeClientConfigProbe(t)

	if got := values["base-url"]; got != "http://env.example" {
		t.Fatalf("base-url = %q, want environment value", got)
	}
	if got := values["username"]; got != "env-user" {
		t.Fatalf("username = %q, want environment value", got)
	}
	if got := values["timeout"]; got != "45s" {
		t.Fatalf("timeout = %q, want environment value", got)
	}
}

func TestClientConfigurationFlagsOverrideEnvironment(t *testing.T) {
	t.Setenv("ARMI_BASE_URL", "http://env.example")
	t.Setenv("ARMI_USERNAME", "env-user")

	values := executeClientConfigProbe(t,
		"--base-url", "http://flag.example",
		"--username", "flag-user",
	)

	if got := values["base-url"]; got != "http://flag.example" {
		t.Fatalf("base-url = %q, want flag value", got)
	}
	if got := values["username"]; got != "flag-user" {
		t.Fatalf("username = %q, want flag value", got)
	}
}

func executeClientConfigProbe(t *testing.T, args ...string) map[string]string {
	t.Helper()

	values := make(map[string]string)
	clientCmd := newClientCommand()
	probeCmd := &cobra.Command{
		Use: "config-probe",
		RunE: func(cmd *cobra.Command, _ []string) error {
			config, err := clientConfiguration(cmd)
			if err != nil {
				return err
			}
			values["base-url"] = config.GetString("base-url")
			values["username"] = config.GetString("username")
			values["timeout"] = config.GetDuration("timeout").String()
			return nil
		},
	}
	clientCmd.AddCommand(probeCmd)
	clientCmd.SetArgs(append([]string{"config-probe"}, args...))

	if err := clientCmd.Execute(); err != nil {
		t.Fatalf("execute client command: %v", err)
	}
	return values
}
