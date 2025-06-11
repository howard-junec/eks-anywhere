package certificates

import (
	"context"
	"fmt"
)

// LinuxRenewer implements OSRenewer for Linux-based systems (Ubuntu/RHEL)
type LinuxRenewer struct {
	certPaths CertificatePaths
	osType    string
}

// NewLinuxRenewer creates a new LinuxRenewer
func NewLinuxRenewer(certPaths CertificatePaths) *LinuxRenewer {
	return &LinuxRenewer{
		certPaths: certPaths,
		osType:    string(OSTypeUbuntu),
	}
}

// RenewControlPlaneCerts renews control plane certificates on a Linux node
func (l *LinuxRenewer) RenewControlPlaneCerts(ctx context.Context, node string, config *RenewalConfig, component string, sshRunner SSHRunner, backupDir string) error {
	fmt.Printf("Processing control plane node: %s...\n", node)

	// Backup certificates, excluding etcd directory if component is control-plane
	var backupCmd string
	if component == componentControlPlane && len(config.Etcd.Nodes) > 0 {
		// When only updating control plane with external etcd, exclude etcd directory
		backupCmd = fmt.Sprintf(`
sudo mkdir -p '/etc/kubernetes/pki.bak_%[1]s'
cd %[2]s
for f in $(find . -type f ! -path './etcd/*'); do
    sudo mkdir -p $(dirname '/etc/kubernetes/pki.bak_%[1]s/'$f)
    sudo cp $f '/etc/kubernetes/pki.bak_%[1]s/'$f
done`, backupDir, l.certPaths.ControlPlaneCertDir)
	} else {
		backupCmd = fmt.Sprintf("sudo cp -r '%s' '/etc/kubernetes/pki.bak_%s'",
			l.certPaths.ControlPlaneCertDir, backupDir)
	}
	if err := sshRunner.RunCommand(ctx, node, backupCmd); err != nil {
		return fmt.Errorf("failed to backup certificates: %v", err)
	}

	// Renew certificates
	fmt.Printf("Renewing certificates on node %s...\n", node)
	renewCmd := "sudo kubeadm certs renew all"
	if component == componentControlPlane && len(config.Etcd.Nodes) > 0 {
		// When only renewing control plane certs with external etcd,
		// we need to skip the etcd directory to preserve certificates
		renewCmd = `for cert in admin.conf apiserver apiserver-kubelet-client controller-manager.conf front-proxy-client scheduler.conf; do
            sudo kubeadm certs renew $cert
        done`
	}
	if err := sshRunner.RunCommand(ctx, node, renewCmd); err != nil {
		return fmt.Errorf("failed to renew certificates: %v", err)
	}

	// Validate certificates
	fmt.Printf("Validating certificates on node %s...\n", node)
	validateCmd := "sudo kubeadm certs check-expiration"
	if err := sshRunner.RunCommand(ctx, node, validateCmd); err != nil {
		return fmt.Errorf("certificate validation failed: %v", err)
	}

	// Restart
	fmt.Printf("Restarting control plane components on node %s...\n", node)
	restartCmd := fmt.Sprintf("sudo mkdir -p /tmp/manifests && "+
		"sudo mv %s/* /tmp/manifests/ && "+
		"sleep 20 && "+
		"sudo mv /tmp/manifests/* %s/",
		l.certPaths.ControlPlaneManifests, l.certPaths.ControlPlaneManifests)
	if err := sshRunner.RunCommand(ctx, node, restartCmd); err != nil {
		return fmt.Errorf("failed to restart control plane components: %v", err)
	}

	fmt.Printf("✅ Completed renewing certificate for the control node: %s.\n", node)
	fmt.Printf("---------------------------------------------\n")
	return nil
}

// RenewEtcdCerts renews etcd certificates on a Linux node
func (l *LinuxRenewer) RenewEtcdCerts(ctx context.Context, node string, sshRunner SSHRunner, backupDir string) error {
	fmt.Printf("Processing etcd node: %s...\n", node)

	// Backup certificates
	fmt.Printf("# Backup certificates\n")
	backupCmd := fmt.Sprintf("cd %s && sudo cp -r pki pki.bak_%s && sudo rm -rf pki/* && sudo cp pki.bak_%s/ca.* pki/",
		l.certPaths.EtcdCertDir, backupDir, backupDir)
	if err := sshRunner.RunCommand(ctx, node, backupCmd); err != nil {
		return fmt.Errorf("failed to backup certificates: %v", err)
	}

	// Renew certificates
	fmt.Printf("# Renew certificates\n")
	renewCmd := "sudo etcdadm join phase certificates http://eks-a-etcd-dumb-url"
	if err := sshRunner.RunCommand(ctx, node, renewCmd); err != nil {
		return fmt.Errorf("failed to renew certificates: %v", err)
	}

	// Validate certificates
	fmt.Printf("# Validate certificates\n")
	validateCmd := fmt.Sprintf("sudo etcdctl --cacert=%s/pki/ca.crt "+
		"--cert=%s/pki/etcdctl-etcd-client.crt "+
		"--key=%s/pki/etcdctl-etcd-client.key "+
		"endpoint health",
		l.certPaths.EtcdCertDir, l.certPaths.EtcdCertDir, l.certPaths.EtcdCertDir)
	if err := sshRunner.RunCommand(ctx, node, validateCmd); err != nil {
		return fmt.Errorf("certificate validation failed: %v", err)
	}

	fmt.Printf("✅ Completed renewing certificate for the ETCD node: %s.\n", node)
	fmt.Printf("---------------------------------------------\n")
	return nil
}
