package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/gomega"

	"github.com/aws/eks-anywhere/internal/pkg/api"
)

func TestFindControlPlaneHardware(t *testing.T) {
	g := NewWithT(t)
	controlPlane := &api.Hardware{
		IPAddress: "192.0.2.10",
		Labels: map[string]string{
			api.HardwareLabelTypeKeyName: api.ControlPlane,
		},
	}
	worker := &api.Hardware{
		IPAddress: "192.0.2.11",
		Labels: map[string]string{
			api.HardwareLabelTypeKeyName: api.Worker,
		},
	}

	got, ok := findControlPlaneHardware([]*api.Hardware{worker, nil, controlPlane})

	g.Expect(ok).To(BeTrue())
	g.Expect(got).To(BeIdenticalTo(controlPlane))
}

func TestFindControlPlaneHardwareNotFound(t *testing.T) {
	g := NewWithT(t)

	got, ok := findControlPlaneHardware([]*api.Hardware{
		{
			IPAddress: "192.0.2.11",
			Labels: map[string]string{
				api.HardwareLabelTypeKeyName: api.Worker,
			},
		},
	})

	g.Expect(ok).To(BeFalse())
	g.Expect(got).To(BeNil())
}

func TestControlPlaneHardwareFromCSV(t *testing.T) {
	g := NewWithT(t)
	inventoryPath := filepath.Join(t.TempDir(), "hardware.csv")
	controlPlane := &api.Hardware{
		IPAddress: "192.0.2.10",
		Labels: map[string]string{
			api.HardwareLabelTypeKeyName: api.ControlPlane,
		},
	}
	worker := &api.Hardware{
		IPAddress: "192.0.2.11",
		Labels: map[string]string{
			api.HardwareLabelTypeKeyName: api.Worker,
		},
	}

	g.Expect(api.WriteHardwareSliceToCSV([]*api.Hardware{worker, controlPlane}, inventoryPath)).To(Succeed())
	data, err := os.ReadFile(inventoryPath)
	g.Expect(err).NotTo(HaveOccurred())

	got, err := controlPlaneHardwareFromCSV(data)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(got.IPAddress).To(Equal(controlPlane.IPAddress))
	g.Expect(got.Labels.Get(api.HardwareLabelTypeKeyName)).To(Equal(api.ControlPlane))
}

func TestControlPlaneHardwareFromCSVRequiresControlPlane(t *testing.T) {
	g := NewWithT(t)
	inventoryPath := filepath.Join(t.TempDir(), "hardware.csv")
	worker := &api.Hardware{
		IPAddress: "192.0.2.11",
		Labels: map[string]string{
			api.HardwareLabelTypeKeyName: api.Worker,
		},
	}

	g.Expect(api.WriteHardwareSliceToCSV([]*api.Hardware{worker}, inventoryPath)).To(Succeed())
	data, err := os.ReadFile(inventoryPath)
	g.Expect(err).NotTo(HaveOccurred())

	_, err = controlPlaneHardwareFromCSV(data)

	g.Expect(err).To(MatchError(ContainSubstring("no control-plane hardware")))
}

func TestRufioRetryNodeDiagnosticsCommand(t *testing.T) {
	g := NewWithT(t)

	command, err := rufioRetryNodeDiagnosticsCommand(
		"192.0.2.10",
		"/home/e2e/test-cluster/physical-node-diagnostics",
	)

	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(command).To(ContainSubstring("ec2-user@192.0.2.10"))
	g.Expect(command).To(ContainSubstring("cloud-init status --long"))
	g.Expect(command).To(ContainSubstring("journalctl -b --no-pager"))
	g.Expect(command).To(ContainSubstring("crictl ps -a"))
	g.Expect(command).To(ContainSubstring(
		"'/home/e2e/test-cluster/physical-node-diagnostics/control-plane.log'",
	))
	g.Expect(strings.Count(command, "RUFIO_NODE_DIAGNOSTICS")).To(Equal(2))
}

func TestRufioRetryNodeDiagnosticsCommandRejectsInvalidIP(t *testing.T) {
	g := NewWithT(t)

	_, err := rufioRetryNodeDiagnosticsCommand(
		"192.0.2.10; touch /tmp/unsafe",
		"/home/e2e/test-cluster/physical-node-diagnostics",
	)

	g.Expect(err).To(MatchError(ContainSubstring("invalid control-plane IP address")))
}
