package certificates

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/aws/eks-anywhere/pkg/constants"
	"github.com/aws/eks-anywhere/pkg/logger"
	"github.com/aws/eks-anywhere/pkg/types"
)

// Renewer handles the certificate renewal process for EKS Anywhere clusters.
type Renewer struct {
	backupDir  string
	kubeClient KubernetesClient
	ssh        SSHRunner
	os         OSRenewer
}

// NewRenewerWithClusterName creates a new certificate renewer instance with a timestamped backup directory.
// and initializes the Kubernetes client with the specified cluster name.
func NewRenewerWithClusterName(osType string, clusterName string) (*Renewer, error) {
	backupDate := time.Now().Format("20060102_150405")
	backupDir := fmt.Sprintf("certificate_backup_%s", backupDate)
	// fmt.Printf("Creating backup directory: %s\n", backupDir)
	// logger.Info("Creating backup directory", "path", backupDir)

	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating backup directory: %v", err)
	}

	etcdCertsPath := filepath.Join(backupDir, tempLocalEtcdCertsDir)
	// fmt.Printf("Creating etcd certs directory: %s\n", etcdCertsPath)
	// logger.Info("Creating etcd certs directory", "path", etcdCertsPath)

	if err := os.MkdirAll(etcdCertsPath, 0o755); err != nil {
		return nil, fmt.Errorf("creating etcd certs directory: %v", err)
	}

	osRenewer, err := BuildOSRenewer(osType)
	if err != nil {
		return nil, fmt.Errorf("creating OS-specific renewer: %v", err)
	}

	kubeClient := NewKubernetesClient()
	if err := kubeClient.InitClient(clusterName); err != nil {
		return nil, fmt.Errorf("initializing kubernetes client: %v", err)
	}

	r := &Renewer{
		backupDir:  backupDir,
		ssh:        NewSSHRunner(),
		kubeClient: kubeClient,
		os:         osRenewer,
	}
	return r, nil
}

// NewRenewer creates a new certificate renewer instance with a timestamped backup directory.
func NewRenewer(osType string) (*Renewer, error) {
	return NewRenewerWithClusterName(osType, "")
}

// processEtcdRenewal handles the renewal of etcd certificates if needed.
func (r *Renewer) processEtcdRenewal(ctx context.Context, config *RenewalConfig, component string) error {
	if !ShouldProcessComponent(component, constants.EtcdComponent) {
		return nil
	}

	if len(config.Etcd.Nodes) == 0 {
		// fmt.Printf("Cluster does not have external ETCD.\n")
		logger.Info("Cluster does not have external ETCD.")
		return nil
	}

	// fmt.Printf("Starting etcd certificate renewal process...\n")
	logger.MarkPass("Starting etcd certificate renewal process")

	if err := r.renewEtcdCerts(ctx, config); err != nil {
		return fmt.Errorf("renewing etcd certificates: %v", err)
	}

	// fmt.Printf("🎉 Etcd certificate renewal process completed successfully.\n")
	logger.MarkSuccess("Etcd certificate renewal process completed successfully.")
	return nil
}

// processControlPlaneRenewal handles the renewal of control plane certificates if needed.
func (r *Renewer) processControlPlaneRenewal(ctx context.Context, config *RenewalConfig, component string) error {
	if !ShouldProcessComponent(component, constants.ControlPlaneComponent) {
		return nil
	}

	if len(config.ControlPlane.Nodes) == 0 {
		return fmt.Errorf("❌ Error: No control plane node IPs found")
	}

	// fmt.Printf("Starting control plane certificate renewal process...\n")
	logger.MarkPass("Starting control plane certificate renewal process")
	if err := r.renewControlPlaneCerts(ctx, config, component); err != nil {
		return fmt.Errorf("renewing control plane certificates: %v", err)
	}
	// fmt.Printf("🎉 Control plane certificate renewal process completed successfully.\n")
	logger.MarkSuccess("Control plane certificate renewal process completed successfully.")
	return nil
}

// finishRenewal performs cleanup operations after certificate renewal.
func (r *Renewer) finishRenewal() error {
	// fmt.Printf("✅ Cleaning up temporary files...\n")
	logger.MarkPass("Cleaning up temporary files")
	if err := r.cleanup(); err != nil {
		// fmt.Printf("❌ API server unreachable — skipping cleanup to preserve debug data.\n")
		logger.MarkFail("API server unreachable — skipping cleanup to preserve debug data.")
		return err
	}
	// fmt.Printf("✅ All temporary files removed.\n")
	logger.MarkPass("Cleanup completed")
	return nil
}

// RenewCertificates orchestrates the certificate renewal process for EKS Anywhere clusters.
func (r *Renewer) RenewCertificates(ctx context.Context, _ *types.Cluster, config *RenewalConfig, component string) error {
	if err := ValidateComponent(component); err != nil {
		return err
	}

	// if err := r.kubeClient.InitClient(); err != nil {
	// 	return fmt.Errorf("initializing kubernetes client: %v", err)
	// }

	if r.kubeClient.IsCertificateExpired() {
		// fmt.Printf("🔧 Proceeding with certificate renewal for cluster with expired certificates...\n")
		logger.MarkWarning("Proceeding with certificate renewal for cluster with expired certificates...")
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
	// fmt.Printf("Cleaning up directory: %s\n", r.backupDir)
	// logger.Info("Cleaning up directory", "path", r.backupDir)
	logger.V(2).Info("Cleaning up directory", "path", r.backupDir)

	chmodCmd := exec.Command("chmod", "-R", "u+w", r.backupDir)
	if err := chmodCmd.Run(); err != nil {
		return fmt.Errorf("changing permissions: %v", err)
	}

	return os.RemoveAll(r.backupDir)
}
