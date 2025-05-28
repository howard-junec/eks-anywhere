package certificates

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func (r *Renewer) renewControlPlaneCertsBottlerocket(ctx context.Context, node string, config *RenewalConfig, component string) error {
	fmt.Printf("Processing control plane node: %s...\n", node)
	client, err := r.sshDialer("tcp", fmt.Sprintf("%s:22", node), r.sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to node %s: %v", node, err)
	}
	defer client.Close()

	// Single sheltie session for all control plane operations
	var backupCmd string
	if component == componentControlPlane && len(config.Etcd.Nodes) > 0 {
		backupCmd = fmt.Sprintf(`mkdir -p '/etc/kubernetes/pki.bak_%[1]s'
cd %[2]s
for f in $(find . -type f ! -path './etcd/*'); do
    mkdir -p $(dirname '/etc/kubernetes/pki.bak_%[1]s/'$f)
    cp $f '/etc/kubernetes/pki.bak_%[1]s/'$f
done`, r.backupDir, bottlerocketControlPlaneCertDir)
	} else {
		backupCmd = fmt.Sprintf("cp -r '%s' '/etc/kubernetes/pki.bak_%s'",
			bottlerocketControlPlaneCertDir, r.backupDir)
	}

	// Prepare renewal command
	var renewCmd string
	if component == componentControlPlane && len(config.Etcd.Nodes) > 0 {
		// When only renewing control plane certs with external etcd,
		// we need to preserve etcd client certificates
		renewCmd = `
# Debug: Check initial state
echo "Checking initial certificate state..."
ls -l /var/lib/kubeadm/pki/apiserver-etcd-client.* || echo "No etcd client certs found initially"

# Create temp directory for certificate operations
echo "Creating temporary directory for certificate operations..."
mkdir -p /var/lib/kubeadm/tmp
chmod 700 /var/lib/kubeadm/tmp

# Backup etcd client certificates if they exist
echo "Backing up etcd client certificates..."
if [ -f "/var/lib/kubeadm/pki/apiserver-etcd-client.crt" ]; then
    cp /var/lib/kubeadm/pki/apiserver-etcd-client.crt /var/lib/kubeadm/tmp/
    cp /var/lib/kubeadm/pki/apiserver-etcd-client.key /var/lib/kubeadm/tmp/
    echo "✅ Etcd client certificates backed up successfully"
else
    echo "⚠️ No etcd client certificates found to backup"
fi

# Execute certificate renewal in container
echo "Executing certificate renewal..."
ctr run \
--mount type=bind,src=/var/lib/kubeadm,dst=/var/lib/kubeadm,options=rbind:rw \
--mount type=bind,src=/var/lib/kubeadm,dst=/etc/kubernetes,options=rbind:rw \
--rm \
${IMAGE_ID} tmp-cert-renew \
/opt/bin/kubeadm certs renew admin.conf apiserver apiserver-kubelet-client controller-manager.conf front-proxy-client scheduler.conf || echo "⚠️ Certificate renewal completed with warnings"

# Restore etcd client certificates
echo "Restoring etcd client certificates..."
if [ -f "/var/lib/kubeadm/tmp/apiserver-etcd-client.crt" ]; then
    cp /var/lib/kubeadm/tmp/apiserver-etcd-client.crt /var/lib/kubeadm/pki/
    cp /var/lib/kubeadm/tmp/apiserver-etcd-client.key /var/lib/kubeadm/pki/
    rm -rf /var/lib/kubeadm/tmp
    echo "✅ Etcd client certificates restored successfully"
else
    echo "⚠️ No backed up etcd client certificates found to restore"
fi

# Debug: Check final state
echo "Checking final certificate state..."
ls -l /var/lib/kubeadm/pki/apiserver-etcd-client.* || echo "⚠️ No etcd client certs found in final state"`
	} else {
		renewCmd = `ctr run \
--mount type=bind,src=/var/lib/kubeadm,dst=/var/lib/kubeadm,options=rbind:rw \
--mount type=bind,src=/var/lib/kubeadm,dst=/etc/kubernetes,options=rbind:rw \
--rm \
${IMAGE_ID} tmp-cert-renew \
/opt/bin/kubeadm certs renew all`
	}

	session := fmt.Sprintf(`set -euo pipefail

# open root shell
sudo sheltie << 'EOF'
# Backup certificates
%s

# pull the image
echo "Pulling kubeadm bootstrap image..."
IMAGE_ID=$(apiclient get | apiclient exec admin jq -r '.settings["host-containers"]["kubeadm-bootstrap"].source')
ctr image pull ${IMAGE_ID}

# Execute renewal commands
echo "Starting certificate renewal process..."
%s

# verify certificates
ctr run \
--mount type=bind,src=/var/lib/kubeadm,dst=/var/lib/kubeadm,options=rbind:rw \
--mount type=bind,src=/var/lib/kubeadm,dst=/etc/kubernetes,options=rbind:rw \
--rm \
${IMAGE_ID} tmp-cert-renew \
/opt/bin/kubeadm certs check-expiration

# If external etcd exists, copy certificates from admin container's tmp
if [ -d "/run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp" ]; then
  # Copy from admin container's tmp to pki
  cp /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp/apiserver-etcd-client.key /var/lib/kubeadm/pki/
  cp /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp/server-etcd-client.crt /var/lib/kubeadm/pki/
fi


# Restart static control plane pods
apiclient get | apiclient exec admin jq -r '.settings.kubernetes["static-pods"] | keys[]' | xargs -n 1 -I {} apiclient set settings.kubernetes.static-pods.{}.enabled=false 
apiclient get | apiclient exec admin jq -r '.settings.kubernetes["static-pods"] | keys[]' | xargs -n 1 -I {} apiclient set settings.kubernetes.static-pods.{}.enabled=true
EOF`, backupCmd, renewCmd, bottlerocketTmpDir, tempLocalEtcdCertsDir, component, tempLocalEtcdCertsDir, tempLocalEtcdCertsDir, bottlerocketControlPlaneCertDir, tempLocalEtcdCertsDir, tempLocalEtcdCertsDir, bottlerocketControlPlaneCertDir, bottlerocketTmpDir, tempLocalEtcdCertsDir)

	if err := r.runCommand(ctx, client, session); err != nil {
		return fmt.Errorf("failed to renew certificates: %v", err)
	}

	fmt.Printf("✅ Completed renewing certificate for the control node: %s.\n", node)
	fmt.Printf("---------------------------------------------\n")
	return nil
}

func (r *Renewer) renewEtcdCertsBottlerocket(ctx context.Context, node string) error {
	fmt.Printf("Processing etcd node: %s...\n", node)

	client, err := r.sshDialer("tcp", fmt.Sprintf("%s:22", node), r.sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to node %s: %v", node, err)
	}
	defer client.Close()

	// First sheltie session for certificate renewal
	firstSession := fmt.Sprintf(`set -euo pipefail
sudo sheltie << 'EOF'
# Get image ID and pull it
IMAGE_ID=$(apiclient get | apiclient exec admin jq -r '.settings["host-containers"]["kubeadm-bootstrap"].source')
ctr image pull ${IMAGE_ID}

# Backup certs
cp -r /var/lib/etcd/pki /var/lib/etcd/pki.bak_%[1]s
rm /var/lib/etcd/pki/*
cp /var/lib/etcd/pki.bak_%[1]s/ca.* /var/lib/etcd/pki
echo "✅ Certs backedup"

# Recreate certificates
ctr run \
--mount type=bind,src=/var/lib/etcd/pki,dst=/etc/etcd/pki,options=rbind:rw \
--net-host \
--rm \
${IMAGE_ID} tmp-cert-renew \
/opt/bin/etcdadm join phase certificates http://eks-a-etcd-dumb-url --init-system kubelet
exit
EOF`, r.backupDir)

	if err := r.runCommand(ctx, client, firstSession); err != nil {
		return fmt.Errorf("failed to renew certificates: %v", err)
	}

	// Second sheltie session for copying certs
	secondSession := fmt.Sprintf(`set -euo pipefail
sudo sheltie << 'EOF'
cp /var/lib/etcd/pki/apiserver-etcd-client.* %s || { echo "❌ Failed to copy certs to tmp"; exit 1; }
chmod 766 %s/apiserver-etcd-client.key || { echo "❌ Failed to chmod key"; exit 1; }
exit
EOF`, bottlerocketTmpDir, bottlerocketTmpDir)

	if err := r.runCommand(ctx, client, secondSession); err != nil {
		return fmt.Errorf("failed to copy certificates2 to tmp: %v", err)
	}

	// Copy certificates to local
	fmt.Printf("Copying certificates from node %s...\n", node)
	if err := r.copyEtcdCerts(ctx, client, node); err != nil {
		return fmt.Errorf("failed to copy certificates3: %v", err)
	}

	// Third sheltie session for cleanup
	thirdSession := fmt.Sprintf(`set -euo pipefail
sudo sheltie << 'EOF'
rm -f %s/apiserver-etcd-client.*
exit
EOF`, bottlerocketTmpDir)

	if err := r.runCommand(ctx, client, thirdSession); err != nil {
		return fmt.Errorf("failed to cleanup temporary files: %v", err)
	}

	fmt.Printf("✅ Completed renewing certificate for the ETCD node: %s.\n", node)
	fmt.Printf("---------------------------------------------\n")
	return nil
}

func (r *Renewer) copyEtcdCerts(ctx context.Context, client sshClient, node string) error {
	// Read certificate file
	crtContent, err := r.runCommandWithOutput(ctx, client, "cat /tmp/apiserver-etcd-client.crt")
	if err != nil {
		return fmt.Errorf("failed to read certificate file: %v", err)
	}

	if len(crtContent) == 0 {
		return fmt.Errorf("certificate file is empty")
	}

	// Read key file
	keyContent, err := r.runCommandWithOutput(ctx, client, "cat /tmp/apiserver-etcd-client.key")
	if err != nil {
		return fmt.Errorf("failed to read key file: %v", err)
	}

	if len(keyContent) == 0 {
		return fmt.Errorf("key file is empty")
	}

	// Write certificate file
	crtPath := filepath.Join(r.backupDir, tempLocalEtcdCertsDir, "apiserver-etcd-client.crt")
	if err := os.WriteFile(crtPath, []byte(crtContent), 0600); err != nil {
		return fmt.Errorf("failed to write certificate file: %v", err)
	}

	// Write key file
	keyPath := filepath.Join(r.backupDir, tempLocalEtcdCertsDir, "apiserver-etcd-client.key")
	if err := os.WriteFile(keyPath, []byte(keyContent), 0600); err != nil {
		return fmt.Errorf("failed to write key file: %v", err)
	}

	return nil
}
