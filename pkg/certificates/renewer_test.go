package certificates

import (
	"context"
	"errors"
	"testing"

	"github.com/golang/mock/gomock"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/aws/eks-anywhere/pkg/types"
)

var testCommands = []string{
	"cd /etc/kubernetes/pki && for f in $(find . -type f ! -path './etcd/*'); do mkdir -p $(dirname '/etc/kubernetes/pki.bak_*/test-key/'$f) && cp $f '/etc/kubernetes/pki.bak_*/test-key/'$f; done",
	"for cert in admin.conf apiserver apiserver-kubelet-client controller-manager.conf front-proxy-client scheduler.conf; do kubeadm certs renew $cert; done",
	"kubeadm certs check-expiration",
}

// renewCertificatesTestCase defines a test case for RenewCertificates.
type renewCertificatesTestCase struct {
	name           string
	config         *RenewalConfig
	component      string
	expectError    bool
	sshErr         error
	expectCommands []string
	setup          func(*testing.T, *gomock.Controller) (*Renewer, func())
}

// setupBasicRenewer creates a basic renewer with the given client.
func setupBasicRenewer(t *testing.T, client *fake.Clientset) *Renewer {
	r, err := NewRenewer()
	if err != nil {
		t.Fatalf("failed to create renewer: %v", err)
	}
	r.kubeClient = client
	return r
}

// setupMockSSHClient sets up a mock SSH client for the renewer.
func setupMockSSHClient(r *Renewer, mockClient *MockClient) {
	// Set up SSH dialer to return our mock client
	r.sshDialer = func(_, _ string, _ *ssh.ClientConfig) (sshClient, error) {
		return mockClient, nil
	}

	// Skip SSH key initialization by directly setting the SSH config
	r.sshConfig = &ssh.ClientConfig{
		User: "ec2-user",
		Auth: []ssh.AuthMethod{
			ssh.Password("test-password"),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
}

// createConfigMap creates a kubeadm config map.
func createConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubeadm-config",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			"ClusterConfiguration": "test-config",
		},
	}
}

// runRenewCertificatesTest runs a single RenewCertificates test case.
func runRenewCertificatesTest(t *testing.T, tt renewCertificatesTestCase) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	r, cleanup := tt.setup(t, ctrl)
	defer cleanup()

	cluster := &types.Cluster{
		Name: tt.config.ClusterName,
	}

	err := r.RenewCertificates(context.Background(), cluster, tt.config, tt.component)
	if tt.expectError && err == nil {
		t.Error("expected error but got none")
	}
	if !tt.expectError && err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// getRenewCertificatesTestCases returns test cases for RenewCertificates.
func getRenewCertificatesTestCases() []renewCertificatesTestCase {
	return []renewCertificatesTestCase{
		{
			name: "invalid component",
			config: &RenewalConfig{
				ClusterName: "test-cluster",
				ControlPlane: NodeConfig{
					Nodes:   []string{"192.168.1.10"},
					OS:      "ubuntu",
					SSHKey:  "/tmp/test-key",
					SSHUser: "ec2-user",
				},
			},
			component:   "invalid",
			expectError: true,
			setup: func(t *testing.T, _ *gomock.Controller) (*Renewer, func()) {
				r := setupBasicRenewer(t, fake.NewSimpleClientset())
				return r, func() {}
			},
		},
		{
			name: "control plane only with external etcd",
			config: &RenewalConfig{
				ClusterName: "test-cluster",
				ControlPlane: NodeConfig{
					Nodes:   []string{"192.168.1.10"},
					OS:      "ubuntu",
					SSHKey:  "/tmp/test-key",
					SSHUser: "ec2-user",
				},
				Etcd: NodeConfig{
					Nodes:   []string{"192.168.1.20"},
					OS:      "ubuntu",
					SSHKey:  "/tmp/test-key",
					SSHUser: "ec2-user",
				},
			},
			component:      "control-plane",
			expectError:    true,
			expectCommands: testCommands,
			setup: func(t *testing.T, ctrl *gomock.Controller) (*Renewer, func()) {
				cm := createConfigMap()
				r := setupBasicRenewer(t, fake.NewSimpleClientset(cm))

				// Mock SSH client
				mockClient := NewMockClient(ctrl)
				mockClient.EXPECT().NewSession().Return(nil, nil).AnyTimes()
				mockClient.EXPECT().Close().Return(nil).AnyTimes()

				setupMockSSHClient(r, mockClient)
				return r, func() {}
			},
		},
		{
			name: "ssh error",
			config: &RenewalConfig{
				ClusterName: "test-cluster",
				ControlPlane: NodeConfig{
					Nodes:   []string{"192.168.1.10"},
					OS:      "ubuntu",
					SSHKey:  "/tmp/test-key",
					SSHUser: "ec2-user",
				},
			},
			component:   "control-plane",
			expectError: true,
			sshErr:      errors.New("ssh connection failed"),
			setup: func(t *testing.T, ctrl *gomock.Controller) (*Renewer, func()) {
				r := setupBasicRenewer(t, fake.NewSimpleClientset())

				// Mock SSH client with error
				mockClient := NewMockClient(ctrl)
				mockClient.EXPECT().NewSession().Return(nil, errors.New("ssh connection failed")).AnyTimes()
				mockClient.EXPECT().Close().Return(nil).AnyTimes()

				setupMockSSHClient(r, mockClient)
				return r, func() {}
			},
		},
		{
			name: "bottlerocket OS",
			config: &RenewalConfig{
				ClusterName: "test-cluster",
				ControlPlane: NodeConfig{
					Nodes:   []string{"192.168.1.10"},
					OS:      "bottlerocket",
					SSHKey:  "/tmp/test-key",
					SSHUser: "ec2-user",
				},
			},
			component:   "control-plane",
			expectError: true,
			setup: func(t *testing.T, ctrl *gomock.Controller) (*Renewer, func()) {
				cm := createConfigMap()
				r := setupBasicRenewer(t, fake.NewSimpleClientset(cm))

				// Mock SSH client
				mockClient := NewMockClient(ctrl)
				mockClient.EXPECT().NewSession().Return(nil, nil).AnyTimes()
				mockClient.EXPECT().Close().Return(nil).AnyTimes()

				setupMockSSHClient(r, mockClient)
				return r, func() {}
			},
		},
		{
			name: "rhel OS",
			config: &RenewalConfig{
				ClusterName: "test-cluster",
				ControlPlane: NodeConfig{
					Nodes:   []string{"192.168.1.10"},
					OS:      "rhel",
					SSHKey:  "/tmp/test-key",
					SSHUser: "ec2-user",
				},
			},
			component:   "control-plane",
			expectError: true,
			setup: func(t *testing.T, ctrl *gomock.Controller) (*Renewer, func()) {
				cm := createConfigMap()
				r := setupBasicRenewer(t, fake.NewSimpleClientset(cm))

				// Mock SSH client
				mockClient := NewMockClient(ctrl)
				mockClient.EXPECT().NewSession().Return(nil, nil).AnyTimes()
				mockClient.EXPECT().Close().Return(nil).AnyTimes()

				setupMockSSHClient(r, mockClient)
				return r, func() {}
			},
		},
	}
}

// TestRenewCertificates tests the RenewCertificates function.
func TestRenewCertificates(t *testing.T) {
	tests := getRenewCertificatesTestCases()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runRenewCertificatesTest(t, tt)
		})
	}
}

func TestCheckAPIServerReachability(t *testing.T) {
	tests := []struct {
		name        string
		setup       func() *Renewer
		expectError bool
	}{
		{
			name: "API server reachable",
			setup: func() *Renewer {
				r, err := NewRenewer()
				if err != nil {
					t.Fatalf("failed to create renewer: %v", err)
				}
				r.kubeClient = fake.NewSimpleClientset()
				return r
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.setup()
			err := r.checkAPIServerReachability(context.Background())
			if tt.expectError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestBackupKubeadmConfig(t *testing.T) {
	tests := []struct {
		name        string
		setup       func() *Renewer
		expectError bool
	}{
		{
			name: "successful backup",
			setup: func() *Renewer {
				r, err := NewRenewer()
				if err != nil {
					t.Fatalf("failed to create renewer: %v", err)
				}
				cm := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "kubeadm-config",
						Namespace: "kube-system",
					},
					Data: map[string]string{
						"ClusterConfiguration": "test-config",
					},
				}
				r.kubeClient = fake.NewSimpleClientset(cm)
				return r
			},
			expectError: false,
		},
		{
			name: "configmap not found",
			setup: func() *Renewer {
				r, err := NewRenewer()
				if err != nil {
					t.Fatalf("failed to create renewer: %v", err)
				}
				r.kubeClient = fake.NewSimpleClientset()
				return r
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.setup()
			err := r.backupKubeadmConfig(context.Background())
			if tt.expectError && err == nil {
				t.Error("expected error but got none")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
