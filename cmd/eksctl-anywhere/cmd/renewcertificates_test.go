package cmd

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRenewCertificatesValidation(t *testing.T) {
	tests := []struct {
		name        string
		configFile  string
		clusterName string
		component   string
		sshKey      string
		expectError bool
		errorMsg    string
		expectWarn  bool
		warnMsg     string
	}{
		{
			name:        "invalid component",
			configFile:  "test.yaml",
			component:   "invalid",
			expectError: true,
			errorMsg:    "invalid component",
		},
		{
			name:        "both config and cluster-name provided",
			configFile:  "test.yaml",
			clusterName: "test-cluster",
			expectWarn:  true,
			warnMsg:     "Both --config and --cluster-name provided, using --config",
		},
		{
			name:        "neither config nor cluster-name provided",
			expectError: true,
			errorMsg:    "must specify either --config or --cluster-name",
		},
		{
			name:        "cluster-name without ssh-key",
			clusterName: "test-cluster",
			expectError: true,
			errorMsg:    "--ssh-key is required when using --cluster-name",
		},
		{
			name:        "valid config file",
			configFile:  "test.yaml",
			expectError: false,
		},
		{
			name:        "valid cluster-name with ssh-key",
			clusterName: "test-cluster",
			sshKey:      "/tmp/test-key",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			if tt.sshKey != "" {
				if err := os.WriteFile(tt.sshKey, []byte("test-key"), 0600); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(tt.sshKey)
			}

			var tmpfile *os.File
			if tt.configFile != "" {
				var err error
				tmpfile, err = os.CreateTemp("", "config-*.yaml")
				if err != nil {
					t.Fatal(err)
				}
				defer os.Remove(tmpfile.Name())

				configContent := `
clusterName: test-cluster
controlPlane:
  nodes:
  - 192.168.1.10
  os: ubuntu
  sshKey: /tmp/test-key
  sshUser: ec2-user
`
				if _, err := tmpfile.Write([]byte(configContent)); err != nil {
					t.Fatal(err)
				}
				if err := tmpfile.Close(); err != nil {
					t.Fatal(err)
				}
				tt.configFile = tmpfile.Name()
			}

			// Set up the command options
			rc := &renewCertificatesOptions{
				configFile:  tt.configFile,
				clusterName: tt.clusterName,
				component:   tt.component,
				sshKey:      tt.sshKey,
			}

			var stdout bytes.Buffer
			cmd := &cobra.Command{}
			cmd.SetOut(&stdout)

			// Run the validation
			err := validateRenewCertificatesOptions(cmd, rc)

			// Check for expected errors
			if tt.expectError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tt.expectError && err != nil && !strings.Contains(err.Error(), tt.errorMsg) {
				t.Errorf("expected error message to contain %q, got %q", tt.errorMsg, err.Error())
			}

			// Check for expected warnings
			if tt.expectWarn && !strings.Contains(stdout.String(), tt.warnMsg) {
				t.Errorf("expected warning message to contain %q, got %q", tt.warnMsg, stdout.String())
			}
		})
	}
}

func validateRenewCertificatesOptions(cmd *cobra.Command, rc *renewCertificatesOptions) error {
	if err := validateComponent(rc.component); err != nil {
		return err
	}

	// if both options are provided
	if rc.configFile != "" && rc.clusterName != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "Warning: Both --config and --cluster-name provided, using --config. The --cluster-name value will be ignored for this command only, but the CLUSTER_NAME environment variable remains unchanged.\n")
		// clear clusterName to ensure it's not used
		rc.clusterName = ""
	}

	// if neither option is provided
	if rc.configFile == "" && rc.clusterName == "" {
		return fmt.Errorf("must specify either --config or --cluster-name")
	}

	if rc.clusterName != "" && rc.sshKey == "" {
		return fmt.Errorf("--ssh-key is required when using --cluster-name")
	}

	return nil
}
