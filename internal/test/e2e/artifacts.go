package e2e

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"

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

const rufioRetryControlPlaneHostScript = `import csv
import ipaddress
import sys

with open(sys.argv[1], newline="") as inventory:
    for row in csv.DictReader(inventory):
        labels = {}
        for pair in row.get("labels", "").split("|"):
            if "=" in pair:
                key, value = pair.split("=", 1)
                labels[key.strip()] = value.strip()
        if labels.get("type") != "control-plane":
            continue

        address = ipaddress.ip_address(row["ip_address"].strip())
        print(f"[{address}]" if address.version == 6 else address)
        break
    else:
        raise SystemExit("generated hardware inventory has no control-plane hardware")
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

	clusterDir := filepath.Join(
		e2eHomeFolder,
		e.clusterName(e.branchName, e.instanceId, testName),
	)
	inventoryPath := filepath.Join(clusterDir, "hardware.csv")
	outputDir := filepath.Join(
		clusterDir,
		"physical-node-diagnostics",
	)
	command := rufioRetryNodeDiagnosticsFromInventoryCommand(inventoryPath, outputDir)

	e.logger.Info("Collecting read-only diagnostics from the Tinkerbell control-plane node")
	if err := ssm.Run(e.session, logr.Discard(), e.instanceId, command, ssmTimeout); err != nil {
		e.logger.Error(err, "Failed to collect Tinkerbell control-plane node diagnostics")
		return
	}
	e.logger.Info("Collected Tinkerbell control-plane node diagnostics")
}

func rufioRetryNodeDiagnosticsFromInventoryCommand(inventoryPath, outputDir string) string {
	return fmt.Sprintf(
		"CONTROL_PLANE_HOST=$(%s) && "+
			"mkdir -p %s && timeout 5m ssh -i %s "+
			"-o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=no "+
			"-o UserKnownHostsFile=/dev/null ec2-user@\"$CONTROL_PLANE_HOST\" 'sudo -n bash -s' "+
			"> %s 2>&1 <<'RUFIO_NODE_DIAGNOSTICS'\n%s\nRUFIO_NODE_DIAGNOSTICS",
		rufioRetryControlPlaneHostCommand(inventoryPath),
		shellQuote(outputDir),
		shellQuote(e2etests.SSHKeyPath),
		shellQuote(filepath.Join(outputDir, "control-plane.log")),
		rufioRetryNodeDiagnosticsScript,
	)
}

func rufioRetryControlPlaneHostCommand(inventoryPath string) string {
	return fmt.Sprintf(
		"python3 - %s <<'RUFIO_HARDWARE_INVENTORY'\n%s\nRUFIO_HARDWARE_INVENTORY\n",
		shellQuote(inventoryPath),
		rufioRetryControlPlaneHostScript,
	)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
