package certificates

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/aws/eks-anywhere/pkg/types"
	"github.com/golang/mock/gomock"
	"golang.org/x/crypto/ssh"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

var testCommands = []string{
    "cd /etc/kubernetes/pki && for f in $(find . -type f ! -path './etcd/*'); do mkdir -p $(dirname '/etc/kubernetes/pki.bak_*/test-key/'$f) && cp $f '/etc/kubernetes/pki.bak_*/test-key/'$f; done",
    "for cert in admin.conf apiserver apiserver-kubelet-client controller-manager.conf front-proxy-client scheduler.conf; do kubeadm certs renew $cert; done",
    "kubeadm certs check-expiration",
}

func TestRenewCertificates(t *testing.T) {
    tests := []struct {
        name           string
        config         *RenewalConfig
        component      string
        expectError    bool
        sshErr         error
        expectCommands []string
        setup          func(*testing.T, *gomock.Controller) (*Renewer, func())
    }{
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
            setup: func(t *testing.T, ctrl *gomock.Controller) (*Renewer, func()) {
                r := NewRenewer()
                r.kubeClient = fake.NewSimpleClientset()
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
            expectError:    false,
            expectCommands: testCommands,
            setup: func(t *testing.T, ctrl *gomock.Controller) (*Renewer, func()) {
                r := NewRenewer()
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

                // Mock SSH session
                mockSession := NewMockSession(ctrl)

                // Mock SSH client
                mockClient := NewMockClient(ctrl)
                mockClient.EXPECT().NewSession().Return(mockSession, nil).AnyTimes()
                mockClient.EXPECT().Close().Return(nil).AnyTimes()

                // Set up SSH dialer to return our mock client
                r.sshDialer = func(network, addr string, config *ssh.ClientConfig) (sshClient, error) {
                    return mockClient, nil
                }

                // Create test SSH key
                if err := os.WriteFile("/tmp/test-key", []byte("test-key"), 0600); err != nil {
                    t.Fatal(err)
                }
                return r, func() {
                    os.Remove("/tmp/test-key")
                }
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
                r := NewRenewer()
                r.kubeClient = fake.NewSimpleClientset()

                // Mock SSH client with error
                mockClient := NewMockClient(ctrl)
                mockClient.EXPECT().NewSession().Return(nil, errors.New("ssh connection failed")).AnyTimes()
                mockClient.EXPECT().Close().Return(nil).AnyTimes()

                // Set up SSH dialer to return our mock client
                r.sshDialer = func(network, addr string, config *ssh.ClientConfig) (sshClient, error) {
                    return mockClient, nil
                }

                if err := os.WriteFile("/tmp/test-key", []byte("test-key"), 0600); err != nil {
                    t.Fatal(err)
                }
                return r, func() {
                    os.Remove("/tmp/test-key")
                }
            },
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
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
                r := NewRenewer()
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
                r := NewRenewer()
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
                r := NewRenewer()
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

