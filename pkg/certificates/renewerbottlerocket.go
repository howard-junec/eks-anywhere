package certificates

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *Renewer) renewControlPlaneCertsBottlerocket(ctx context.Context, node string, config *RenewalConfig, component string) error {
	fmt.Printf("Processing control plane node: %s...\n", node)
	client, err := r.sshDialer("tcp", fmt.Sprintf("%s:22", node), r.sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to node %s: %v", node, err)
	}
	defer client.Close()

	// If we have external etcd nodes, first transfer certificates to the node
	if component == componentControlPlane && len(config.Etcd.Nodes) > 0 {
		if err := r.transferCertsToControlPlane(ctx, node); err != nil {
			return fmt.Errorf("failed to transfer certificates to control plane node: %v", err)
		}
	}

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
		renewCmd = `ctr run \
--mount type=bind,src=/var/lib/kubeadm,dst=/var/lib/kubeadm,options=rbind:rw \
--mount type=bind,src=/var/lib/kubeadm,dst=/etc/kubernetes,options=rbind:rw \
--rm \
${IMAGE_ID} tmp-cert-renew \
/opt/bin/kubeadm certs renew admin.conf apiserver apiserver-kubelet-client controller-manager.conf front-proxy-client scheduler.conf`
	} else {
		renewCmd = `ctr run \
--mount type=bind,src=/var/lib/kubeadm,dst=/var/lib/kubeadm,options=rbind:rw \
--mount type=bind,src=/var/lib/kubeadm,dst=/etc/kubernetes,options=rbind:rw \
--rm \
${IMAGE_ID} tmp-cert-renew \
/opt/bin/kubeadm certs renew all`
	}

	// Main sheltie session
	session := fmt.Sprintf(`set -euo pipefail

# open root shell
sudo sheltie << 'EOF'
# Backup certificates
%[1]s

# pull the image
echo "Pulling kubeadm bootstrap image..."
IMAGE_ID=$(apiclient get | apiclient exec admin jq -r '.settings["host-containers"]["kubeadm-bootstrap"].source')
ctr image pull ${IMAGE_ID}

# Execute renewal commands
echo "Starting certificate renewal process..."
%[2]s

# verify certificates
ctr run \
--mount type=bind,src=/var/lib/kubeadm,dst=/var/lib/kubeadm,options=rbind:rw \
--mount type=bind,src=/var/lib/kubeadm,dst=/etc/kubernetes,options=rbind:rw \
--rm \
${IMAGE_ID} tmp-cert-renew \
/opt/bin/kubeadm certs check-expiration

# If we have external etcd nodes, copy certificates from /tmp to pki
if [ -d "/run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp/etcd-client-certs" ]; then
    echo "Copying certificates..."

    # Copy to /etc/kubernetes/pki with standard naming

    cp -v /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp/etcd-client-certs/apiserver-etcd-client.crt /var/lib/kubeadm/pki/server-etcd-client.crt
    cp -v /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp/etcd-client-certs/apiserver-etcd-client.key /var/lib/kubeadm/pki/apiserver-etcd-client.key

    # Set permissions for /etc/kubernetes/pki
    chmod 600 /var/lib/kubeadm/pki/server-etcd-client.crt
    chmod 600 /var/lib/kubeadm/pki/apiserver-etcd-client.key


    echo "Verifying /etc/kubernetes/pki files..."
    ls -l /var/lib/kubeadm/pki/apiserver-etcd-client.crt
    ls -l /var/lib/kubeadm/pki/apiserver-etcd-client.key

else
    echo "❌ Source directory does not exist"
    ls -l /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp/
fi


# Restart static control plane pods
echo "Restarting static control plane pods..."
apiclient get | apiclient exec admin jq -r '.settings.kubernetes["static-pods"] | keys[]' | xargs -n 1 -I {} apiclient set settings.kubernetes.static-pods.{}.enabled=false 
apiclient get | apiclient exec admin jq -r '.settings.kubernetes["static-pods"] | keys[]' | xargs -n 1 -I {} apiclient set settings.kubernetes.static-pods.{}.enabled=true
EOF`, backupCmd, renewCmd, tempLocalEtcdCertsDir)

	if err := r.runCommand(ctx, client, session); err != nil {
		return fmt.Errorf("failed to renew certificates: %v", err)
	}

	fmt.Printf("✅ Completed renewing certificate for the control node: %s.\n", node)
	fmt.Printf("---------------------------------------------\n")
	return nil
}

func (r *Renewer) transferCertsToControlPlane(ctx context.Context, node string) error {
	fmt.Printf("Transferring certificates to control plane node: %s...\n", node)

	client, err := r.sshDialer("tcp", fmt.Sprintf("%s:22", node), r.sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to node %s: %v", node, err)
	}
	defer client.Close()

	srcCrt := filepath.Join(r.backupDir, tempLocalEtcdCertsDir, "apiserver-etcd-client.crt")
	crtContent, err := os.ReadFile(srcCrt)
	if err != nil {
		return fmt.Errorf("failed to read certificate file: %v", err)
	}

	srcKey := filepath.Join(r.backupDir, tempLocalEtcdCertsDir, "apiserver-etcd-client.key")
	keyContent, err := os.ReadFile(srcKey)
	if err != nil {
		return fmt.Errorf("failed to read key file: %v", err)
	}

	crtBase64 := base64.StdEncoding.EncodeToString(crtContent)
	keyBase64 := base64.StdEncoding.EncodeToString(keyContent)

	session := fmt.Sprintf(`
sudo sheltie << 'EOF'
echo "Creating directory..."
mkdir -p /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp/%[1]s
ls -l /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp/

echo "Writing certificate file..."
echo '%[2]s' | base64 -d > /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp/%[1]s/apiserver-etcd-client.crt || {
    echo "❌ Failed to write certificate file"
    exit 1
}

echo "Writing key file..."
echo '%[3]s' | base64 -d > /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp/%[1]s/apiserver-etcd-client.key || {
    echo "❌ Failed to write key file"
    exit 1
}

echo "Setting permissions..."
chmod 600 /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp/%[1]s/apiserver-etcd-client.crt || {
    echo "❌ Failed to set permissions on certificate"
    exit 1
}
chmod 600 /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp/%[1]s/apiserver-etcd-client.key || {
    echo "❌ Failed to set permissions on key"
    exit 1
}

echo "Verifying files..."
ls -l /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp/%[1]s/
exit
EOF`, tempLocalEtcdCertsDir, crtBase64, keyBase64)

	if err := r.runCommand(ctx, client, session); err != nil {
		return fmt.Errorf("failed to transfer certificates: %v", err)
	}

	fmt.Printf("External certificates transferred to control plane node: %s.\n", node)
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
echo "Source files in /var/lib/etcd/pki/:"
ls -l /var/lib/etcd/pki/apiserver-etcd-client.*

echo "Copying certificates to %[1]s..."
cp /var/lib/etcd/pki/apiserver-etcd-client.* %[1]s || { 
    echo "❌ Failed to copy certs to tmp"
    echo "Source files:"
    ls -l /var/lib/etcd/pki/apiserver-etcd-client.*
    echo "Destination directory:"
    ls -l %[1]s
    exit 1
}

echo "Setting permissions..."
chmod 600 %[1]s/apiserver-etcd-client.crt || { 
    echo "❌ Failed to chmod certificate"
    ls -l %[1]s/apiserver-etcd-client.crt
    exit 1
}
chmod 600 %[1]s/apiserver-etcd-client.key || { 
    echo "❌ Failed to chmod key"
    ls -l %[1]s/apiserver-etcd-client.key
    exit 1
}

echo "Verifying copied files..."
ls -l %[1]s/apiserver-etcd-client.*
exit
EOF`, bottlerocketTmpDir)

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
	// Read certificate file with debug info
	fmt.Printf("Reading certificate from ETCD node %s...\n", node)
	debugCmd := fmt.Sprintf(`
sudo sheltie << 'EOF'
echo "Checking source files:"
ls -l %s/apiserver-etcd-client.*
exit
EOF`, bottlerocketTmpDir)
	if err := r.runCommand(ctx, client, debugCmd); err != nil {
		return fmt.Errorf("failed to list certificate files: %v", err)
	}

	// Read certificate content
	crtCmd := fmt.Sprintf(`
sudo sheltie << 'EOF'
cat %s/apiserver-etcd-client.crt
exit
EOF`, bottlerocketTmpDir)
	crtContent, err := r.runCommandWithOutput(ctx, client, crtCmd)
	if err != nil {
		return fmt.Errorf("failed to read certificate file: %v", err)
	}

	if len(crtContent) == 0 {
		return fmt.Errorf("certificate file is empty")
	}

	// Read key file with debug info
	fmt.Printf("Reading key from ETCD node %s...\n", node)
	keyCmd := fmt.Sprintf(`
sudo sheltie << 'EOF'
cat %s/apiserver-etcd-client.key
exit
EOF`, bottlerocketTmpDir)
	keyContent, err := r.runCommandWithOutput(ctx, client, keyCmd)
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

func (r *Renewer) updateAPIServerEtcdClientSecret(ctx context.Context, clusterName string) error {
	fmt.Printf("Updating %s-apiserver-etcd-client secret...\n", clusterName)

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

	// get current sercet or create
	secretName := fmt.Sprintf("%s-apiserver-etcd-client", clusterName)
	secret, err := r.kubeClient.CoreV1().Secrets("eksa-system").Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get secret %s: %v", secretName, err)
		}

		// if sercet not exist, create
		secret = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: "eksa-system",
			},
			Type: corev1.SecretTypeTLS,
			Data: map[string][]byte{
				"tls.crt": crtData,
				"tls.key": keyData,
			},
		}

		_, err = r.kubeClient.CoreV1().Secrets("eksa-system").Create(ctx, secret, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("failed to create secret %s: %v", secretName, err)
		}
	} else {
		// if sercet exist, renew it
		if secret.Data == nil {
			secret.Data = make(map[string][]byte)
		}

		secret.Data["tls.crt"] = crtData
		secret.Data["tls.key"] = keyData

		_, err = r.kubeClient.CoreV1().Secrets("eksa-system").Update(ctx, secret, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update secret %s: %v", secretName, err)
		}
	}

	fmt.Printf("✅ Successfully updated %s secret.\n", secretName)
	return nil
}
