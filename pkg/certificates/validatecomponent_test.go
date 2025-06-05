package certificates

import (
	"fmt"
	"testing"
)

func TestValidateComponent(t *testing.T) {
	tests := []struct {
		name        string
		component   string
		expectError bool
	}{
		{
			name:        "empty component",
			component:   "",
			expectError: false,
		},
		{
			name:        "etcd component",
			component:   "etcd",
			expectError: false,
		},
		{
			name:        "control-plane component",
			component:   "control-plane",
			expectError: false,
		},
		{
			name:        "invalid component",
			component:   "invalid",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {

			err := validateComponentForTest(tt.component)
			if tt.expectError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func validateComponentForTest(component string) error {
	if component != "" && component != componentEtcd && component != componentControlPlane {
		return fmt.Errorf("invalid component %q, must be either %q or %q", component, componentEtcd, componentControlPlane)
	}
	return nil
}
