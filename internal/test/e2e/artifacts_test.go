package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

func TestRufioRetryNodeDiagnosticsFromInventoryCommand(t *testing.T) {
	g := NewWithT(t)

	command := rufioRetryNodeDiagnosticsFromInventoryCommand(
		"/home/e2e/test-cluster/hardware.csv",
		"/home/e2e/test-cluster/physical-node-diagnostics",
	)

	g.Expect(command).To(ContainSubstring("csv.DictReader"))
	g.Expect(command).To(ContainSubstring(`labels.get("type") != "control-plane"`))
	g.Expect(command).To(ContainSubstring("ipaddress.ip_address"))
	g.Expect(command).To(ContainSubstring("ec2-user@\"$CONTROL_PLANE_HOST\""))
	g.Expect(command).NotTo(ContainSubstring("cat "))
	g.Expect(command).NotTo(ContainSubstring("bmc_password"))

	syntaxCheck := exec.Command("sh", "-n")
	syntaxCheck.Stdin = strings.NewReader(command)
	output, err := syntaxCheck.CombinedOutput()
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(output).To(BeEmpty())
}

func TestRufioRetryControlPlaneHostCommand(t *testing.T) {
	g := NewWithT(t)
	inventoryPath := filepath.Join(t.TempDir(), "hardware.csv")
	inventory := `hostname,ip_address,netmask,gateway,nameservers,mac,disk,labels,bmc_ip,bmc_username,bmc_password,vlan_id
worker,192.0.2.11,,,,,,type=worker,,worker-user,worker-secret,
control-plane,192.0.2.10,,,,,,type=control-plane,,cp-user,"cp,secret",
`
	g.Expect(os.WriteFile(inventoryPath, []byte(inventory), 0o600)).To(Succeed())

	output, err := exec.Command("sh", "-c", rufioRetryControlPlaneHostCommand(inventoryPath)).CombinedOutput()

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(strings.TrimSpace(string(output))).To(Equal("192.0.2.10"))
	g.Expect(string(output)).NotTo(ContainSubstring("cp-user"))
	g.Expect(string(output)).NotTo(ContainSubstring("cp,secret"))
}
