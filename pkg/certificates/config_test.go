package certificates

import (
	"os"
	"strings"
	"testing"
)

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name        string
		configYaml  string
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid config with both etcd and control plane",
			configYaml: `
clusterName: test-cluster
controlPlane:
  nodes:
  - 192.168.1.10
  os: ubuntu
  sshKey: /tmp/test-key
  sshUser: ec2-user
etcd:
  nodes:
  - 192.168.1.20
  os: ubuntu
  sshKey: /tmp/test-key
  sshUser: ec2-user
`,
			expectError: false,
		},
		{
			name: "valid config without etcd (embedded)",
			configYaml: `
clusterName: test-cluster
controlPlane:
  nodes:
  - 192.168.1.10
  os: ubuntu
  sshKey: /tmp/test-key
  sshUser: ec2-user
`,
			expectError: false,
		},
		{
			name: "invalid config - missing cluster name",
			configYaml: `
controlPlane:
  nodes:
  - 192.168.1.10
  os: ubuntu
  sshKey: /tmp/test-key
  sshUser: ec2-user
`,
			expectError: true,
		},
		{
			name: "invalid config - missing control plane nodes",
			configYaml: `
clusterName: test-cluster
controlPlane:
  os: ubuntu
  sshKey: /tmp/test-key
  sshUser: ec2-user
`,
			expectError: true,
		},
		{
			name: "invalid config - unsupported OS",
			configYaml: `
clusterName: test-cluster
controlPlane:
  nodes:
  - 192.168.1.10
  os: windows
  sshKey: /tmp/test-key
  sshUser: ec2-user
`,
			expectError: true,
			errorMsg:    "unsupported OS",
		},
		{
			name: "valid config with SSH password",
			configYaml: `
clusterName: test-cluster
controlPlane:
  nodes:
  - 192.168.1.10
  os: ubuntu
  sshKey: /tmp/test-key
  sshUser: ec2-user
  sshPasswd: password123
`,
			expectError: false,
		},
		{
			name: "valid config with redhat OS",
			configYaml: `
clusterName: test-cluster
controlPlane:
  nodes:
  - 192.168.1.10
  os: redhat
  sshKey: /tmp/test-key
  sshUser: ec2-user
`,
			expectError: false,
		},
		{
			name: "valid config with bottlerocket OS",
			configYaml: `
clusterName: test-cluster
controlPlane:
  nodes:
  - 192.168.1.10
  os: bottlerocket
  sshKey: /tmp/test-key
  sshUser: ec2-user
`,
			expectError: false,
		},
		{
			name: "valid config with different OS types",
			configYaml: `
clusterName: test-cluster
controlPlane:
  nodes:
  - 192.168.1.10
  os: ubuntu
  sshKey: /tmp/test-key
  sshUser: ec2-user
etcd:
  nodes:
  - 192.168.1.20
  os: bottlerocket
  sshKey: /tmp/test-key
  sshUser: ec2-user
`,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary config file
			tmpfile, err := os.CreateTemp("", "config-*.yaml")
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(tmpfile.Name())

			if _, err := tmpfile.Write([]byte(tt.configYaml)); err != nil {
				t.Fatal(err)
			}
			if err := tmpfile.Close(); err != nil {
				t.Fatal(err)
			}

			// Create temporary SSH key file
			keyFile := "/tmp/test-key"
			if err := os.WriteFile(keyFile, []byte("test-key"), 0600); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(keyFile)

			// Test config parsing
			_, err = ParseConfig(tmpfile.Name())
			if tt.expectError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tt.expectError && err != nil && tt.errorMsg != "" && !contains(err.Error(), tt.errorMsg) {
				t.Errorf("expected error message to contain %q, got %q", tt.errorMsg, err.Error())
			}
		})
	}
}

func TestValidateNodeConfig(t *testing.T) {
	tests := []struct {
		name        string
		config      NodeConfig
		component   string
		expectError bool
	}{
		{
			name: "valid ubuntu config",
			config: NodeConfig{
				Nodes:   []string{"192.168.1.10"},
				OS:      "ubuntu",
				SSHKey:  "/tmp/test-key",
				SSHUser: "ec2-user",
			},
			component:   "control plane",
			expectError: false,
		},
		{
			name: "valid rhel config",
			config: NodeConfig{
				Nodes:   []string{"192.168.1.10"},
				OS:      "rhel",
				SSHKey:  "/tmp/test-key",
				SSHUser: "ec2-user",
			},
			component:   "etcd",
			expectError: false,
		},
		{
			name: "valid bottlerocket config",
			config: NodeConfig{
				Nodes:   []string{"192.168.1.10"},
				OS:      "bottlerocket",
				SSHKey:  "/tmp/test-key",
				SSHUser: "ec2-user",
			},
			component:   "control plane",
			expectError: false,
		},
		{
			name: "invalid - missing nodes",
			config: NodeConfig{
				OS:      "ubuntu",
				SSHKey:  "/tmp/test-key",
				SSHUser: "ec2-user",
			},
			component:   "control plane",
			expectError: true,
		},
		{
			name: "invalid - unsupported OS",
			config: NodeConfig{
				Nodes:   []string{"192.168.1.10"},
				OS:      "windows",
				SSHKey:  "/tmp/test-key",
				SSHUser: "ec2-user",
			},
			component:   "control plane",
			expectError: true,
		},
		{
			name: "invalid - missing SSH key",
			config: NodeConfig{
				Nodes:   []string{"192.168.1.10"},
				OS:      "ubuntu",
				SSHUser: "ec2-user",
			},
			component:   "control plane",
			expectError: true,
		},
		{
			name: "invalid - missing SSH user",
			config: NodeConfig{
				Nodes:  []string{"192.168.1.10"},
				OS:     "ubuntu",
				SSHKey: "/tmp/test-key",
			},
			component:   "control plane",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			if tt.config.SSHKey != "" {
				if err := os.WriteFile(tt.config.SSHKey, []byte("test-key"), 0600); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(tt.config.SSHKey)
			}

			err := validateNodeConfig(&tt.config)
			if tt.expectError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
