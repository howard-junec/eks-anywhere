package certificates

import (
	"os"
	"testing"
)

func TestInitSSHConfig(t *testing.T) {
	tests := []struct {
		name        string
		user        string
		keyPath     string
		passwd      string
		keyContent  string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid SSH key without passphrase",
			user:        "ec2-user",
			keyPath:     "/tmp/test-key-1",
			passwd:      "",
			keyContent:  testPrivateKey,
			expectError: false,
		},
		{
			name:        "invalid SSH key",
			user:        "ec2-user",
			keyPath:     "/tmp/test-key-2",
			passwd:      "",
			keyContent:  "invalid-key",
			expectError: true,
			errorMsg:    "failed to parse SSH key",
		},
		{
			name:        "non-existent SSH key file",
			user:        "ec2-user",
			keyPath:     "/tmp/non-existent-key",
			passwd:      "",
			keyContent:  "",
			expectError: true,
			errorMsg:    "failed to read SSH key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.keyContent != "" {
				if err := os.WriteFile(tt.keyPath, []byte(tt.keyContent), 0o600); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(tt.keyPath)
			}

			r, err := NewRenewer()
			if err != nil {
				t.Fatalf("failed to create renewer: %v", err)
			}

			err = r.initSSHConfig(tt.user, tt.keyPath, tt.passwd)
			if tt.expectError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if tt.expectError && err != nil && tt.errorMsg != "" && !contains(err.Error(), tt.errorMsg) {
				t.Errorf("expected error message to contain %q, got %q", tt.errorMsg, err.Error())
			}

			if !tt.expectError {
				if r.sshConfig == nil {
					t.Error("SSH config was not set")
				}
				if r.sshConfig.User != tt.user {
					t.Errorf("expected SSH user %q, got %q", tt.user, r.sshConfig.User)
				}
				if r.sshKeyPath != tt.keyPath {
					t.Errorf("expected SSH key path %q, got %q", tt.keyPath, r.sshKeyPath)
				}
			}
		})
	}
}

// Test private key for SSH tests
// This is a test key, not used for anything real.
var testPrivateKey = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACBsETg9gZQ5dSy+4qy7Cg4Zx7bE+KFi0xQyNKTJiM4YHwAAAJg2zz0UNs89
FAAAAAtzc2gtZWQyNTUxOQAAACBsETg9gZQ5dSy+4qy7Cg4Zx7bE+KFi0xQyNKTJiM4YHw
AAAEAIUUzgh0BfSZJ1JJ0NqQwO8FnIQgYyVFtZ3wYQEIQQoGwROD2BlDl1LL7irLsKDhnH
tsT4oWLTFDI0pMmIzhgfAAAAEHRlc3RAZXhhbXBsZS5jb20BAgMEBQ==
-----END OPENSSH PRIVATE KEY-----`
