package e2e

import (
	"bytes"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"

	"github.com/aws/eks-anywhere/internal/pkg/api"
	"github.com/aws/eks-anywhere/internal/pkg/s3"
	"github.com/aws/eks-anywhere/internal/pkg/ssm"
	e2etests "github.com/aws/eks-anywhere/test/framework"
)

const e2eHomeFolder = "/home/e2e/"

const rufioRetryNodeDiagnosticsScript = `set +e
section() {
	printf '\n===== %s =====\n' "$1"
}

section "timestamp"
date -u

section "system"
uname -a
systemctl is-system-running

section "network"
ip address
ip route
ss -lntp

section "cloud-init status"
cloud-init status --long

section "service status"
systemctl --no-pager --full status \
	cloud-init-local.service cloud-init.service cloud-config.service cloud-final.service \
	containerd.service kubelet.service

section "cloud-init journal"
journalctl -b --no-pager -n 2000 \
	-u cloud-init-local.service -u cloud-init.service -u cloud-config.service -u cloud-final.service

section "containerd and kubelet journal"
journalctl -b --no-pager -n 2000 -u containerd.service -u kubelet.service

section "kubernetes static pods"
ls -la /etc/kubernetes /etc/kubernetes/manifests

section "container runtime"
crictl info
crictl pods
crictl ps -a

section "cloud-init output"
tail -n 4000 /var/log/cloud-init-output.log

exit 0
`

func (e *E2ESession) uploadGeneratedFilesFromInstance(testName string) {
	e.logger.V(1).Info("Uploading log files to s3 bucket")
	command := newCopyCommand().from(
		e2eHomeFolder, e.clusterName(e.branchName, e.instanceId, testName),
	).to(
		e.generatedArtifactsBucketPath(), testName,
	).recursive().String()

	if err := ssm.Run(e.session, logr.Discard(), e.instanceId, command, ssmTimeout); err != nil {
		e.logger.Error(err, "error uploading log files from instance")
	} else {
		e.logger.V(1).Info("Successfully uploaded log files to S3")
	}
}

func (e *E2ESession) uploadDiagnosticArchiveFromInstance(testName string) {
	bundleNameFormat := "support-bundle-*.tar.gz"
	e.logger.V(1).Info("Uploading diagnostic bundle to s3 bucket")
	command := newCopyCommand().from(e2eHomeFolder).to(
		e.generatedArtifactsBucketPath(), testName,
	).recursive().exclude("*").include(bundleNameFormat).String()

	if err := ssm.Run(e.session, logr.Discard(), e.instanceId, command, ssmTimeout); err != nil {
		e.logger.Error(err, "error uploading diagnostic bundle from instance")
	} else {
		e.logger.V(1).Info("Successfully uploaded diagnostic bundle files to S3")
	}
}

func (e *E2ESession) uploadJUnitReportFromInstance(testName string) {
	junitFile := "junit-testing.xml"
	e.logger.V(1).Info("Uploading JUnit report to s3 bucket")
	command := newCopyCommand().from(e2eHomeFolder).to(
		e.generatedArtifactsBucketPath(), testName,
	).recursive().exclude("*").include(junitFile).String()

	if err := ssm.Run(e.session, logr.Discard(), e.instanceId, command, ssmTimeout); err != nil {
		e.logger.Error(err, "error uploading JUnit report from instance")
	} else {
		e.logger.V(1).Info("Successfully uploaded JUnit report files to S3")
	}
}

func (e *E2ESession) downloadJUnitReportToLocalDisk(testName, destinationFolder string) {
	junitFile := "junit-testing.xml"
	key := filepath.Join(e.generatedArtifactsPath(), testName, junitFile)
	dst := filepath.Join(destinationFolder, fmt.Sprintf("junit-testing-%s.xml", testName))

	e.logger.V(1).Info("Downloading JUnit report to disk", "dst", dst)
	if err := s3.DownloadToDisk(e.session, key, e.storageBucket, dst); err != nil {
		e.logger.Error(err, "Error downloading JUnit report from s3")
	}
}

func (e *E2ESession) generatedArtifactsBucketPath() string {
	return fmt.Sprintf("s3://%s/%s", e.storageBucket, e.generatedArtifactsPath())
}

func (e *E2ESession) generatedArtifactsPath() string {
	return filepath.Join(e.jobId, "generated-artifacts")
}

func (e *E2ESession) collectRufioRetryNodeDiagnostics(testName string) {
	if testName != rufioHardOffRetryTestName {
		return
	}

	controlPlane, err := e.rufioRetryControlPlaneHardware(testName)
	if err != nil {
		e.logger.Error(err, "Cannot collect Rufio retry node diagnostics")
		return
	}

	outputDir := filepath.Join(
		e2eHomeFolder,
		e.clusterName(e.branchName, e.instanceId, testName),
		"physical-node-diagnostics",
	)
	command, err := rufioRetryNodeDiagnosticsCommand(controlPlane.IPAddress, outputDir)
	if err != nil {
		e.logger.Error(err, "Cannot build Rufio retry node diagnostic command")
		return
	}

	e.logger.Info("Collecting read-only diagnostics from the Tinkerbell control-plane node")
	if err := ssm.Run(e.session, logr.Discard(), e.instanceId, command, ssmTimeout); err != nil {
		e.logger.Error(err, "Failed to collect Tinkerbell control-plane node diagnostics")
		return
	}
	e.logger.Info("Collected Tinkerbell control-plane node diagnostics")
}

func (e *E2ESession) rufioRetryControlPlaneHardware(testName string) (*api.Hardware, error) {
	if controlPlane, ok := findControlPlaneHardware(e.hardware); ok {
		return controlPlane, nil
	}

	inventoryPath := filepath.Join(
		e2eHomeFolder,
		e.clusterName(e.branchName, e.instanceId, testName),
		"hardware.csv",
	)
	output, err := ssm.RunCommand(
		e.session,
		logr.Discard(),
		e.instanceId,
		"cat "+shellQuote(inventoryPath),
		ssmTimeout,
	)
	if err != nil {
		return nil, fmt.Errorf("reading generated hardware inventory: %w", err)
	}
	if !output.Successful() {
		return nil, fmt.Errorf(
			"reading generated hardware inventory returned status %s",
			output.StatusDetails(),
		)
	}

	return controlPlaneHardwareFromCSV(output.StdOut)
}

func controlPlaneHardwareFromCSV(data []byte) (*api.Hardware, error) {
	hardware, err := api.NewHardwareSlice(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parsing generated hardware inventory: %w", err)
	}
	controlPlane, ok := findControlPlaneHardware(hardware)
	if !ok {
		return nil, fmt.Errorf("generated hardware inventory has no control-plane hardware")
	}
	return controlPlane, nil
}

func findControlPlaneHardware(hardware []*api.Hardware) (*api.Hardware, bool) {
	for _, machine := range hardware {
		if machine != nil && machine.Labels.Get(api.HardwareLabelTypeKeyName) == api.ControlPlane {
			return machine, true
		}
	}
	return nil, false
}

func rufioRetryNodeDiagnosticsCommand(ipAddress, outputDir string) (string, error) {
	ipAddress = strings.TrimSpace(ipAddress)
	ip := net.ParseIP(ipAddress)
	if ip == nil {
		return "", fmt.Errorf("invalid control-plane IP address %q", ipAddress)
	}

	sshHost := ipAddress
	if ip.To4() == nil {
		sshHost = "[" + ipAddress + "]"
	}
	outputFile := filepath.Join(outputDir, "control-plane.log")

	return fmt.Sprintf(
		"mkdir -p %s && timeout 5m ssh -i %s "+
			"-o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=no "+
			"-o UserKnownHostsFile=/dev/null ec2-user@%s 'sudo -n bash -s' "+
			"> %s 2>&1 <<'RUFIO_NODE_DIAGNOSTICS'\n%s\nRUFIO_NODE_DIAGNOSTICS",
		shellQuote(outputDir),
		shellQuote(e2etests.SSHKeyPath),
		sshHost,
		shellQuote(outputFile),
		rufioRetryNodeDiagnosticsScript,
	), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
