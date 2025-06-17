package certificates

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/eks-anywhere/pkg/clients/kubernetes"
	"github.com/aws/eks-anywhere/pkg/constants"
	"github.com/aws/eks-anywhere/pkg/logger"
)

const tempLocalEtcdCertsDir = "etcd-client-certs"

// Renewer handles the certificate renewal process for EKS Anywhere clusters.
type Renewer struct {
	backupDir string
	kube      kubernetes.Client
	ssh       SSHRunner
	os        OSRenewer
}

// NewRenewer creates a new certificate renewer instance with a timestamped backup directory.
func NewRenewer(kube kubernetes.Client, sshRunner SSHRunner, osRenewer OSRenewer) (*Renewer, error) {
	ts := time.Now().Format("20060102_150405")
	backupDir := "certificate_backup_" + ts

	if err := os.MkdirAll(filepath.Join(backupDir, tempLocalEtcdCertsDir), 0o755); err != nil {
		return nil, fmt.Errorf("creating backup directory: %v", err)
	}
	return &Renewer{
		backupDir: backupDir,
		kube:      kube,
		ssh:       sshRunner,
		os:        osRenewer,
	}, nil
}

// RenewCertificates orchestrates the certificate renewal process for the specified component.
func (r *Renewer) RenewCertificates(ctx context.Context, cfg *RenewalConfig, component string) error {
	processEtcd, processControlPlane, err := r.validateRenewalConfig(cfg, component)
	if err != nil {
		return err
	}

	if err := r.checkAPIServerReachability(ctx); err != nil {
		logger.MarkWarning("API server unreachable, proceeding with caution", "error", err)
	}

	if err := r.backupKubeadmConfig(ctx); err != nil {
		logger.V(2).Info("kubeadm-config backup completed with status", "error", err)
	}

	if processEtcd {
		if err := r.renewEtcdCerts(ctx, cfg); err != nil {
			return err
		}
	}

	if processControlPlane {
		if err := r.renewControlPlaneCerts(ctx, cfg, component); err != nil {
			return err
		}
	}

	return r.finishRenewal()
}

func (r *Renewer) renewEtcdCerts(ctx context.Context, cfg *RenewalConfig) error {
	logger.MarkPass("Starting etcd certificate renewal process")

	for _, node := range cfg.Etcd.Nodes {
		if err := r.os.RenewEtcdCerts(ctx, node, r.ssh, r.backupDir); err != nil {
			return fmt.Errorf("renewing certificates for etcd node %s: %v", node, err)
		}
	}

	if err := r.updateAPIServerEtcdClientSecret(ctx, cfg.ClusterName); err != nil {
		logger.MarkWarning("Failed to update apiserver-etcd-client secret", "error", err)
		logger.Info("You may need to manually update the secret after the API server is reachable")
		logger.Info("Use kubectl edit secret to update the secret", "command", fmt.Sprintf("kubectl edit secret %s-apiserver-etcd-client -n eksa-system", cfg.ClusterName))

	}

	logger.MarkSuccess("Etcd certificate renewal process completed successfully.")
	return nil
}

func (r *Renewer) renewControlPlaneCerts(ctx context.Context, cfg *RenewalConfig, component string) error {
	logger.MarkPass("Starting control plane certificate renewal process")

	for _, node := range cfg.ControlPlane.Nodes {
		if err := r.os.RenewControlPlaneCerts(ctx, node, cfg, component, r.ssh, r.backupDir); err != nil {
			return fmt.Errorf("renewing certificates for control-plane node %s: %v", node, err)
		}
	}

	logger.MarkSuccess("Control plane certificate renewal process completed successfully.")
	return nil
}

func (r *Renewer) updateAPIServerEtcdClientSecret(ctx context.Context, clusterName string) error {
	logger.MarkPass("Updating apiserver-etcd-client secret", "cluster", clusterName)

	// if err := r.ensureNamespaceExists(ctx, constants.EksaSystemNamespace); err != nil {
	// 	return fmt.Errorf("ensuring eksa-system namespace exists: %v", err)
	// }

	crtPath := filepath.Join(r.backupDir, tempLocalEtcdCertsDir, "apiserver-etcd-client.crt")
	keyPath := filepath.Join(r.backupDir, tempLocalEtcdCertsDir, "apiserver-etcd-client.key")

	crtData, err := os.ReadFile(crtPath)
	if err != nil {
		return fmt.Errorf("failed to read certificate file: %v", err)
	}
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("failed to read key file: %v", err)
	}

	crtBase64 := base64.StdEncoding.EncodeToString(crtData)
	keyBase64 := base64.StdEncoding.EncodeToString(keyData)

	secretName := fmt.Sprintf("%s-apiserver-etcd-client", clusterName)

	patchData := fmt.Sprintf(`{"data":{"tls.crt":"%s","tls.key":"%s"}}`, crtBase64, keyBase64)

	cmd := exec.Command("kubectl", "patch", "secret", secretName,
		"-n", constants.EksaSystemNamespace,
		"--type=merge",
		"-p", patchData)

	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "NotFound") {
			createCmd := exec.Command("kubectl", "create", "secret", "tls",
				secretName,
				"-n", constants.EksaSystemNamespace,
				"--insecure-skip-tls-verify=true",
				"--cert", crtPath,
				"--key", keyPath)

			if output, err := createCmd.CombinedOutput(); err != nil {
				return fmt.Errorf("failed to create secret %s: %v, output: %s", secretName, err, string(output))
			}
		} else {
			return fmt.Errorf("failed to update secret %s: %v, output: %s", secretName, err, string(output))
		}
	}

	// logger.V(2).Info("Successfully updated secret", "name", secretName)
	logger.Info("Successfully updated secret", "name", secretName)
	return nil
}

// func (r *Renewer) updateAPIServerEtcdClientSecret(ctx context.Context, clusterName string) error {
// 	logger.MarkPass("Updating apiserver-etcd-client secret", "cluster", clusterName)

// 	if err := r.ensureNamespaceExists(ctx, constants.EksaSystemNamespace); err != nil {
// 		return fmt.Errorf("ensuring eksa-system namespace exists: %v", err)
// 	}

// 	crtPath := filepath.Join(r.backupDir, tempLocalEtcdCertsDir, "apiserver-etcd-client.crt")
// 	keyPath := filepath.Join(r.backupDir, tempLocalEtcdCertsDir, "apiserver-etcd-client.key")

// 	crtData, err := os.ReadFile(crtPath)
// 	if err != nil {
// 		return fmt.Errorf("failed to read certificate file: %v", err)
// 	}
// 	keyData, err := os.ReadFile(keyPath)
// 	if err != nil {
// 		return fmt.Errorf("failed to read key file: %v", err)
// 	}

// 	secretName := fmt.Sprintf("%s-apiserver-etcd-client", clusterName)
// 	secret := &corev1.Secret{
// 		ObjectMeta: metav1.ObjectMeta{
// 			Name:      secretName,
// 			Namespace: constants.EksaSystemNamespace,
// 		},
// 		Type: corev1.SecretTypeTLS,
// 		Data: map[string][]byte{
// 			"tls.crt": crtData,
// 			"tls.key": keyData,
// 		},
// 	}

// 	existingSecret := &corev1.Secret{}
// 	err = r.kube.Get(ctx, secretName, constants.EksaSystemNamespace, existingSecret)
// 	if err != nil {
// 		if apierrors.IsNotFound(err) {
// 			if err = r.kube.Create(ctx, secret); err != nil {
// 				return fmt.Errorf("failed to create secret %s: %v", secretName, err)
// 			}
// 			logger.V(2).Info("Successfully created secret", "name", secretName)
// 		} else {
// 			return fmt.Errorf("failed to get secret %s: %v", secretName, err)
// 		}
// 	} else {
// 		existingSecret.Data = secret.Data
// 		if err = r.kube.Update(ctx, existingSecret); err != nil {
// 			return fmt.Errorf("failed to update secret %s: %v", secretName, err)
// 		}
// 		logger.V(2).Info("Successfully updated secret", "name", secretName)
// 	}

// 	return nil
// }

// func (r *Renewer) ensureNamespaceExists(ctx context.Context, namespace string) error {
// 	ns := &corev1.Namespace{}
// 	err := r.kube.Get(ctx, namespace, "", ns)
// 	if err != nil {
// 		if !apierrors.IsNotFound(err) {
// 			return fmt.Errorf("checking namespace %s: %v", namespace, err)
// 		}
// 		ns.Name = namespace
// 		if err = r.kube.Create(ctx, ns); err != nil {
// 			return fmt.Errorf("create namespace %s: %v", namespace, err)
// 		}
// 		logger.Info("Created namespace", "name", namespace)
// 	}
// 	return nil
// }

func (r *Renewer) ensureNamespaceExists(ctx context.Context, namespace string) error {
	nsCmd := exec.Command("kubectl", "get", "namespace", namespace)
	if err := nsCmd.Run(); err != nil {
		createNsCmd := exec.Command("kubectl", "create", "namespace", namespace)
		if output, err := createNsCmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to create namespace %s: %v, output: %s", namespace, err, string(output))
		}
		logger.Info("Created namespace", "name", namespace)
	}
	return nil
}

func (r *Renewer) finishRenewal() error {
	logger.MarkPass("Cleaning up temporary files")
	return r.cleanup()
}

func (r *Renewer) cleanup() error {
	logger.V(2).Info("Cleaning up directory", "path", r.backupDir)
	chmodCmd := exec.Command("chmod", "-R", "u+w", r.backupDir)
	if err := chmodCmd.Run(); err != nil {
		return fmt.Errorf("changing permissions: %v", err)
	}
	return os.RemoveAll(r.backupDir)
}

func (r *Renewer) validateRenewalConfig(cfg *RenewalConfig, component string) (processEtcd, processControlPlane bool, err error) {
	processEtcd = ShouldProcessComponent(component, constants.EtcdComponent) && len(cfg.Etcd.Nodes) > 0
	processControlPlane = ShouldProcessComponent(component, constants.ControlPlaneComponent)

	if processEtcd {
		if err := r.ssh.InitSSHConfig(cfg.Etcd.SSH); err != nil {
			return false, false, fmt.Errorf("initializing SSH config for etcd: %v", err)
		}
	}

	if processControlPlane {
		if err := r.ssh.InitSSHConfig(cfg.ControlPlane.SSH); err != nil {
			return false, false, fmt.Errorf("initializing SSH config for control-plane: %v", err)
		}
	}

	return processEtcd, processControlPlane, nil
}

func (r *Renewer) checkAPIServerReachability(_ context.Context) error {
	logger.Info("Checking if Kubernetes API server is reachable...")

	for i := 0; i < 5; i++ {
		cmd := exec.Command("kubectl", "version", "--request-timeout=2m")
		cmd.Stdout = nil
		cmd.Stderr = nil

		if err := cmd.Run(); err == nil {
			logger.Info("✅ API server is reachable")
			return nil
		}

		logger.V(2).Info("API server not reachable, retrying...", "attempt", i+1)
		time.Sleep(10 * time.Second)
	}

	return fmt.Errorf("❌ Error: Kubernetes API server is not reachable")
}

func (r *Renewer) backupKubeadmConfig(ctx context.Context) error {
	logger.Info("Attempting to backup kubeadm-config ConfigMap...")

	if err := os.MkdirAll(r.backupDir, 0o755); err != nil {
		logger.MarkWarning("Failed to create backup directory", "error", err)
		return nil
	}

	backupPath := filepath.Join(r.backupDir, "kubeadm-config.yaml")

	cmd := exec.CommandContext(ctx, "kubectl", "-n", "kube-system", "get", "cm", "kubeadm-config", "-o", "yaml")
	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.MarkWarning("Could not backup kubeadm-config, continuing without backup", "error", err)
		return nil
	}

	if err := os.WriteFile(backupPath, output, 0o600); err != nil {
		logger.MarkWarning("Failed to write backup file", "error", err)
		return nil
	}

	logger.Info("kubeadm-config backed up successfully", "path", backupPath)
	return nil
}
