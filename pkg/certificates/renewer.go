package certificates

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/aws/eks-anywhere/pkg/types"
)

const (
	tempLocalEtcdCertsDir = "etcd-client-certs"

	ubuntuEtcdCertDir           = "/etc/etcd"
	ubuntuControlPlaneCertDir   = "/etc/kubernetes/pki"
	ubuntuControlPlaneManifests = "/etc/kubernetes/manifests"

	bottlerocketEtcdCertDir         = "/var/lib/etcd"
	bottlerocketControlPlaneCertDir = "/var/lib/kubeadm/pki"
	bottlerocketTmpDir              = "/run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp"

	componentEtcd         = "etcd"
	componentControlPlane = "control-plane"
)

// sshDialer is a function type for creating SSH clients
type sshDialer func(network, addr string, config *ssh.ClientConfig) (sshClient, error)

type Renewer struct {
	backupDir  string
	sshConfig  *ssh.ClientConfig
	sshKeyPath string // Store SSH key path from config
	kubeClient kubernetes.Interface
	sshDialer  sshDialer
}

func NewRenewer() *Renewer {
	backupDate := time.Now().Format("20060102_150405")
	r := &Renewer{
		backupDir: fmt.Sprintf("certificate_backup_%s", backupDate),
		sshDialer: func(network, addr string, config *ssh.ClientConfig) (sshClient, error) {
			return ssh.Dial(network, addr, config)
		},
	}
	return r
}

func (r *Renewer) RenewCertificates(ctx context.Context, cluster *types.Cluster, config *RenewalConfig, component string) error {
	if component != "" && component != componentEtcd && component != componentControlPlane {
		return fmt.Errorf("invalid component %q, must be either %q or %q", component, componentEtcd, componentControlPlane)
	}

	fmt.Printf("✅ Checking if Kubernetes API server is reachable...\n")
	if err := r.initKubeClient(); err != nil {
		return fmt.Errorf("failed to initialize kubernetes client: %v", err)
	}

	if err := r.checkAPIServerReachability(ctx); err != nil {
		return fmt.Errorf("API server health check failed: %v", err)
	}

	if err := os.MkdirAll(r.backupDir, 0700); err != nil {
		return fmt.Errorf("failed to create backup directory: %v", err)
	}

	etcdCertsPath := filepath.Join(r.backupDir, tempLocalEtcdCertsDir)
	if err := os.MkdirAll(etcdCertsPath, 0700); err != nil {
		return fmt.Errorf("failed to create etcd certs directory: %v", err)
	}

	fmt.Printf("✅ Backing up kubeadm-config ConfigMap...\n")
	if err := r.backupKubeadmConfig(ctx); err != nil {
		return fmt.Errorf("failed to backup kubeadm config: %v", err)
	}

	if component == componentEtcd || component == "" {
		if len(config.Etcd.Nodes) > 0 {
			fmt.Printf("Starting etcd certificate renewal process...\n")
			if err := r.renewEtcdCerts(ctx, config); err != nil {
				return fmt.Errorf("failed to renew etcd certificates: %v", err)
			}
			fmt.Printf("🎉 Etcd certificate renewal process completed successfully.\n")
		} else {
			fmt.Printf("Cluster does not have external ETCD.\n")
		}
	}

	if component == componentControlPlane || component == "" {
		if len(config.ControlPlane.Nodes) == 0 {
			return fmt.Errorf("❌ Error: No control plane node IPs found")
		}
		fmt.Printf("Starting control plane certificate renewal process...\n")
		if err := r.renewControlPlaneCerts(ctx, config, component); err != nil {
			return fmt.Errorf("failed to renew control plane certificates: %v", err)
		}
		fmt.Printf("🎉 Control plane certificate renewal process completed successfully.\n")
	}

	fmt.Printf("✅ Cleaning up temporary files...\n")
	if err := r.cleanup(); err != nil {
		fmt.Printf("❌ API server unreachable — skipping cleanup to preserve debug data.\n")
		return err
	}
	fmt.Printf("✅ All temporary files removed.\n")
	return nil
}

func (r *Renewer) initKubeClient() error {
	if r.kubeClient != nil {
		return nil
	}

	kubeconfig := os.Getenv("KUBECONFIG")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return fmt.Errorf("failed to build kubeconfig: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %v", err)
	}

	r.kubeClient = clientset
	return nil
}

func (r *Renewer) checkAPIServerReachability(ctx context.Context) error {
	for i := 0; i < 5; i++ {
		_, err := r.kubeClient.Discovery().ServerVersion()
		if err == nil {
			return nil
		}
		time.Sleep(10 * time.Second)
	}
	return fmt.Errorf("kubernetes API server is not reachable")
}

func (r *Renewer) backupKubeadmConfig(ctx context.Context) error {
	cm, err := r.kubeClient.CoreV1().ConfigMaps("kube-system").Get(ctx, "kubeadm-config", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get kubeadm-config: %v", err)
	}

	backupPath := filepath.Join(r.backupDir, "kubeadm-config.yaml")
	if err := os.WriteFile(backupPath, []byte(cm.Data["ClusterConfiguration"]), 0600); err != nil {
		return fmt.Errorf("failed to write kubeadm config backup: %v", err)
	}

	return nil
}

func (r *Renewer) renewEtcdCerts(ctx context.Context, config *RenewalConfig) error {

	if err := r.initSSHConfig(config.Etcd.SSHUser, config.Etcd.SSHKey, config.Etcd.SSHPasswd); err != nil {
		return fmt.Errorf("failed to initialize SSH config: %v", err)
	}

	for _, node := range config.Etcd.Nodes {
		if err := r.renewEtcdNodeCerts(ctx, node, config.Etcd); err != nil {
			return fmt.Errorf("failed to renew certificates for etcd node %s: %v", node, err)
		}
	}

	return nil
}

func (r *Renewer) renewControlPlaneCerts(ctx context.Context, config *RenewalConfig, component string) error {
	if err := r.initSSHConfig(config.ControlPlane.SSHUser, config.ControlPlane.SSHKey, config.ControlPlane.SSHPasswd); err != nil {
		return fmt.Errorf("failed to initialize SSH config: %v", err)
	}

	// Renew certificate for each control plane node
	for _, node := range config.ControlPlane.Nodes {
		if err := r.renewControlPlaneNodeCerts(ctx, node, config, component); err != nil {
			return fmt.Errorf("failed to renew certificates for control plane node %s: %v", node, err)
		}
	}

	return nil
}

func (r *Renewer) initSSHConfig(user, keyPath string, passwd string) error {
	r.sshKeyPath = keyPath // Store SSH key path
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("failed to read SSH key: %v", err)
	}

	var signer ssh.Signer
	signer, err = ssh.ParsePrivateKey(key)
	if err != nil {
		if err.Error() == "ssh: this private key is passphrase protected" {
			if passwd == "" {
				fmt.Printf("Enter passphrase for SSH key '%s': ", keyPath)
				var passphrase []byte
				passphrase, err = term.ReadPassword(int(os.Stdin.Fd()))
				if err != nil {
					return fmt.Errorf("failed to read passphrase: %v", err)
				}
				fmt.Println() // Print newline after password input
				passwd = string(passphrase)
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(passwd))
			if err != nil {
				return fmt.Errorf("failed to parse SSH key with passphrase: %v", err)
			}
		} else {
			return fmt.Errorf("failed to parse SSH key: %v", err)
		}
	}

	r.sshConfig = &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	return nil
}

func (r *Renewer) renewEtcdNodeCerts(ctx context.Context, node string, config NodeConfig) error {
	switch config.OS {
	case "ubuntu", "rhel":
		return r.renewEtcdCertsLinux(ctx, node)
	case "bottlerocket":
		return r.renewEtcdCertsBottlerocket(ctx, node)
	default:
		return fmt.Errorf("unsupported OS: %s", config.OS)
	}
}

func (r *Renewer) renewControlPlaneNodeCerts(ctx context.Context, node string, config *RenewalConfig, component string) error {
	switch config.ControlPlane.OS {
	case "ubuntu", "rhel":
		return r.renewControlPlaneCertsLinux(ctx, node, config, component)
	case "bottlerocket":
		return r.renewControlPlaneCertsBottlerocket(ctx, node, config, component)
	default:
		return fmt.Errorf("unsupported OS: %s", config.ControlPlane.OS)
	}
}

func (r *Renewer) renewEtcdCertsLinux(ctx context.Context, node string) error {
	fmt.Printf("Processing etcd node: %s...\n", node)
	client, err := r.sshDialer("tcp", fmt.Sprintf("%s:22", node), r.sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to node %s: %v", node, err)
	}
	defer client.Close()

	// Backup certificates
	fmt.Printf("# Backup certificates\n")
	backupCmd := fmt.Sprintf("cd %s && sudo cp -r pki pki.bak_%s && sudo rm -rf pki/* && sudo cp pki.bak_%s/ca.* pki/",
		ubuntuEtcdCertDir, r.backupDir, r.backupDir)
	if err := r.runCommand(ctx, client, backupCmd); err != nil {
		return fmt.Errorf("failed to backup certificates: %v", err)
	}

	// Renew certificates
	fmt.Printf("# Renew certificates\n")
	renewCmd := "sudo etcdadm join phase certificates http://eks-a-etcd-dumb-url"
	if err := r.runCommand(ctx, client, renewCmd); err != nil {
		return fmt.Errorf("failed to renew certificates: %v", err)
	}

	// Validate certificates
	fmt.Printf("# Validate certificates\n")
	validateCmd := fmt.Sprintf("sudo etcdctl --cacert=%s/pki/ca.crt "+
		"--cert=%s/pki/etcdctl-etcd-client.crt "+
		"--key=%s/pki/etcdctl-etcd-client.key "+
		"endpoint health",
		ubuntuEtcdCertDir, ubuntuEtcdCertDir, ubuntuEtcdCertDir)
	if err := r.runCommand(ctx, client, validateCmd); err != nil {
		return fmt.Errorf("certificate validation failed: %v", err)
	}

	// Copy certificates to local
	fmt.Printf("Copying certificates from node %s...\n", node)
	if err := r.copyEtcdCerts(ctx, client, node); err != nil {
		return fmt.Errorf("failed to copy certificates1: %v", err)
	}

	fmt.Printf("✅ Completed renewing certificate for the ETCD node: %s.\n", node)
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

func (r *Renewer) renewControlPlaneCertsLinux(ctx context.Context, node string, config *RenewalConfig, component string) error {
	fmt.Printf("Processing control plane node: %s...\n", node)
	client, err := r.sshDialer("tcp", fmt.Sprintf("%s:22", node), r.sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to node %s: %v", node, err)
	}
	defer client.Close()

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
done`, r.backupDir, ubuntuControlPlaneCertDir)
	} else {
		backupCmd = fmt.Sprintf("sudo cp -r '%s' '/etc/kubernetes/pki.bak_%s'",
			ubuntuControlPlaneCertDir, r.backupDir)
	}
	if err := r.runCommand(ctx, client, backupCmd); err != nil {
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
	if err := r.runCommand(ctx, client, renewCmd); err != nil {
		return fmt.Errorf("failed to renew certificates: %v", err)
	}

	// Validate certificates
	fmt.Printf("Validating certificates on node %s...\n", node)
	validateCmd := "sudo kubeadm certs check-expiration"
	if err := r.runCommand(ctx, client, validateCmd); err != nil {
		return fmt.Errorf("certificate validation failed: %v", err)
	}

	// Restart
	fmt.Printf("Restarting control plane components on node %s...\n", node)
	restartCmd := fmt.Sprintf("sudo mkdir -p /tmp/manifests && "+
		"sudo mv %s/* /tmp/manifests/ && "+
		"sleep 20 && "+
		"sudo mv /tmp/manifests/* %s/",
		ubuntuControlPlaneManifests, ubuntuControlPlaneManifests)
	if err := r.runCommand(ctx, client, restartCmd); err != nil {
		return fmt.Errorf("failed to restart control plane components: %v", err)
	}

	fmt.Printf("✅ Completed renewing certificate for the control node: %s.\n", node)
	fmt.Printf("---------------------------------------------\n")
	return nil
}

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

# Only copy etcd client certificates if external etcd exists and we're not in control-plane-only mode
if [[ -d "%s/%s" ]] && [[ "%s" != "control-plane" ]]; then
  sudo cp '%s/%s/apiserver-etcd-client.crt' '%s/server-etcd-client.crt'
  sudo cp '%s/%s/apiserver-etcd-client.key' '%s'
  rm -rf %s/%s
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

func (r *Renewer) runCommand(ctx context.Context, client sshClient, cmd string) error {
	done := make(chan error, 1)
	output := make(chan string, 1)
	go func() {
		session, err := client.NewSession()
		if err != nil {
			done <- fmt.Errorf("failed to create session: %v", err)
			output <- ""
			return
		}
		defer session.Close()

		var stdoutBuf, stderrBuf strings.Builder
		session.Stdout = &stdoutBuf
		session.Stderr = &stderrBuf
		err = session.Run(cmd)
		if err != nil {

			errOutput := stderrBuf.String()
			if errOutput == "" {
				errOutput = stdoutBuf.String()
			}
			done <- fmt.Errorf("command failed: %v\nOutput: %s", err, errOutput)
		} else {
			done <- nil
		}
		output <- stdoutBuf.String()
	}()
	select {
	case <-ctx.Done():
		return fmt.Errorf("command cancelled: %v", ctx.Err())
	case err := <-done:
		cmdOutput := <-output
		if err != nil {
			fmt.Printf("Command output:\n%s\n", cmdOutput)
			return err
		}
		return nil
	}
}

func (r *Renewer) runCommandWithOutput(ctx context.Context, client sshClient, cmd string) (string, error) {
	type result struct {
		output string
		err    error
	}
	done := make(chan result, 1)

	go func() {
		session, err := client.NewSession()
		if err != nil {
			done <- result{"", fmt.Errorf("failed to create session: %v", err)}
			return
		}
		defer session.Close()

		output, err := session.Output(cmd)
		if err != nil {
			done <- result{"", fmt.Errorf("command failed: %v", err)}
			return
		}
		done <- result{strings.TrimSpace(string(output)), nil}
	}()

	select {
	case <-ctx.Done():
		return "", fmt.Errorf("command cancelled: %v", ctx.Err())
	case res := <-done:
		return res.output, res.err
	}
}

func (r *Renewer) copyEtcdCerts(ctx context.Context, client sshClient, node string) error {
	// Read certificate file
	crtContent, err := r.runCommandWithOutput(ctx, client, "cat /tmp/apiserver-etcd-client.crt")
	if err != nil {
		return fmt.Errorf("failed to read certificate file: %v", err)
	}

	// Read key file
	keyContent, err := r.runCommandWithOutput(ctx, client, "cat /tmp/apiserver-etcd-client.key")
	if err != nil {
		return fmt.Errorf("failed to read key file: %v", err)
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

func (r *Renewer) cleanup() error {
	return os.RemoveAll(r.backupDir)
}
