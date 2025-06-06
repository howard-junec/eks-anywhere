package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRenewCertificatesValidation(t *testing.T) {
	tests := []struct {
		name        string
		configFile  string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "no config file provided",
			configFile:  "",
			expectError: true,
			errorMsg:    "must specify --config",
		},
		{
			name:        "valid config file",
			configFile:  "test.yaml",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
				configFile: tt.configFile,
			}

			cmd := &cobra.Command{}

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
		})
	}
}

func validateRenewCertificatesOptions(_ *cobra.Command, rc *renewCertificatesOptions) error {
	if rc.configFile == "" {
		return os.ErrNotExist
	}
	return nil
}
