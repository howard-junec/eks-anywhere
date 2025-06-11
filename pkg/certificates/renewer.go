package certificates

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/aws/eks-anywhere/pkg/types"
)

// Renewer handles the certificate renewal process for EKS Anywhere clusters.
type Renewer struct {
	backupDir  string
	kubeClient KubernetesClient
	ssh        SSHRunner
	os         OSRenewer
}

// NewRenewer creates a new certificate renewer instance with a timestamped backup directory.
func NewRenewer(osType string) (*Renewer, error) {
	backupDate := time.Now().Format("20060102_150405")
	backupDir := fmt.Sprintf("certificate_backup_%s", backupDate)
	fmt.Printf("Creating backup directory: %s\n", backupDir)

	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return nil, fmt.Errorf("creating backup directory: %v", err)
	}

	etcdCertsPath := filepath.Join(backupDir, tempLocalEtcdCertsDir)
	fmt.Printf("Creating etcd certs directory: %s\n", etcdCertsPath)

	if err := os.MkdirAll(etcdCertsPath, 0755); err != nil {
		return nil, fmt.Errorf("creating etcd certs directory: %v", err)
	}

	osRenewer, err := BuildOSRenewer(osType)
	if err != nil {
		return nil, fmt.Errorf("creating OS-specific renewer: %v", err)
	}

	r := &Renewer{
		backupDir:  backupDir,
		ssh:        NewSSHRunner(),
		kubeClient: NewKubernetesClient(),
		os:         osRenewer,
	}
	return r, nil
}

// validateComponent checks if the specified component is valid.
func validateComponent(component string) error {
	if component != "" && component != componentEtcd && component != componentControlPlane {
		return fmt.Errorf("invalid component %q, must be either %q or %q", component, componentEtcd, componentControlPlane)
	}
	return nil
}

// processEtcdRenewal handles the renewal of etcd certificates if needed.
func (r *Renewer) processEtcdRenewal(ctx context.Context, config *RenewalConfig, component string) error {
	if component != componentEtcd && component != "" {
		return nil
	}

	if len(config.Etcd.Nodes) == 0 {
		fmt.Printf("Cluster does not have external ETCD.\n")
		return nil
	}

	fmt.Printf("Starting etcd certificate renewal process...\n")
	if err := r.renewEtcdCerts(ctx, config); err != nil {
		return fmt.Errorf("renewing etcd certificates: %v", err)
	}

	fmt.Printf("🎉 Etcd certificate renewal process completed successfully.\n")
	return nil
}

// processControlPlaneRenewal handles the renewal of control plane certificates if needed.
func (r *Renewer) processControlPlaneRenewal(ctx context.Context, config *RenewalConfig, component string) error {
	if component != componentControlPlane && component != "" {
		return nil
	}

	if len(config.ControlPlane.Nodes) == 0 {
		return fmt.Errorf("❌ Error: No control plane node IPs found")
	}

	fmt.Printf("Starting control plane certificate renewal process...\n")
	if err := r.renewControlPlaneCerts(ctx, config, component); err != nil {
		return fmt.Errorf("renewing control plane certificates: %v", err)
	}
	fmt.Printf("🎉 Control plane certificate renewal process completed successfully.\n")
	return nil
}

// finishRenewal performs cleanup operations after certificate renewal.
func (r *Renewer) finishRenewal() error {
	fmt.Printf("✅ Cleaning up temporary files...\n")
	if err := r.cleanup(); err != nil {
		fmt.Printf("❌ API server unreachable — skipping cleanup to preserve debug data.\n")
		return err
	}
	fmt.Printf("✅ All temporary files removed.\n")
	return nil
}

// RenewCertificates orchestrates the certificate renewal process for EKS Anywhere clusters.
func (r *Renewer) RenewCertificates(ctx context.Context, _ *types.Cluster, config *RenewalConfig, component string) error {
	if err := validateComponent(component); err != nil {
		return err
	}

	if err := r.kubeClient.InitClient(); err != nil {
		return fmt.Errorf("initializing kubernetes client: %v", err)
	}

	if err := r.kubeClient.CheckAPIServerReachability(ctx); err != nil {
		return fmt.Errorf("API server health check failed: %v", err)
	}

	if err := r.kubeClient.BackupKubeadmConfig(ctx, r.backupDir); err != nil {
		return fmt.Errorf("backing up kubeadm config: %v", err)
	}

	if err := r.processEtcdRenewal(ctx, config, component); err != nil {
		return err
	}

	if err := r.processControlPlaneRenewal(ctx, config, component); err != nil {
		return err
	}

	return r.finishRenewal()
}

func (r *Renewer) renewEtcdCerts(ctx context.Context, config *RenewalConfig) error {
	if err := r.ssh.InitSSHConfig(config.Etcd.SSHUser, config.Etcd.SSHKey, config.Etcd.SSHPasswd); err != nil {
		return fmt.Errorf("initializing SSH config: %v", err)
	}

	for _, node := range config.Etcd.Nodes {
		if err := r.os.RenewEtcdCerts(ctx, node, r.ssh, r.backupDir); err != nil {
			return fmt.Errorf("renewing certificates for etcd node %s: %v", node, err)
		}
	}

	if err := r.kubeClient.UpdateAPIServerEtcdClientSecret(ctx, config.ClusterName, r.backupDir); err != nil {
		return fmt.Errorf("updating apiserver-etcd-client secret: %v", err)
	}

	return nil
}

func (r *Renewer) renewControlPlaneCerts(ctx context.Context, config *RenewalConfig, component string) error {
	if err := r.ssh.InitSSHConfig(config.ControlPlane.SSHUser, config.ControlPlane.SSHKey, config.ControlPlane.SSHPasswd); err != nil {
		return fmt.Errorf("initializing SSH config: %v", err)
	}

	// Renew certificate for each control plane node
	for _, node := range config.ControlPlane.Nodes {
		if err := r.os.RenewControlPlaneCerts(ctx, node, config, component, r.ssh, r.backupDir); err != nil {
			return fmt.Errorf("renewing certificates for control plane node %s: %v", node, err)
		}
	}

	return nil
}

func (r *Renewer) cleanup() error {
	fmt.Printf("Cleaning up directory: %s\n", r.backupDir)

	chmodCmd := exec.Command("chmod", "-R", "u+w", r.backupDir)
	if err := chmodCmd.Run(); err != nil {
		return fmt.Errorf("changing permissions: %v", err)
	}

	return os.RemoveAll(r.backupDir)
}
