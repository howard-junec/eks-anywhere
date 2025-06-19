package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/aws/eks-anywhere/pkg/api/v1alpha1"
	"github.com/aws/eks-anywhere/pkg/certificates"
	"github.com/aws/eks-anywhere/pkg/constants"
	"github.com/aws/eks-anywhere/pkg/dependencies"
	"github.com/aws/eks-anywhere/pkg/kubeconfig"
	"github.com/aws/eks-anywhere/pkg/logger"
)

type renewCertificatesOptions struct {
	configFile string
	component  string
}

var rc = &renewCertificatesOptions{}

var renewCertificatesCmd = &cobra.Command{
	Use:          "certificates",
	Short:        "Renew certificates",
	Long:         "Renew external ETCD and control plane certificates",
	PreRunE:      bindFlagsToViper,
	SilenceUsage: true,
	RunE:         rc.renewCertificates,
}

func init() {
	renewCmd.AddCommand(renewCertificatesCmd)
	renewCertificatesCmd.Flags().StringVarP(&rc.configFile, "config", "f", "", "Config file containing node and SSH information")
	renewCertificatesCmd.Flags().StringVarP(&rc.component, "component", "c", "", fmt.Sprintf("Component to renew certificates for (%s or %s). If not specified, renews both.", constants.EtcdComponent, constants.ControlPlaneComponent))
	renewCertificatesCmd.Flags().IntVarP(&certificates.VerbosityLevel, "verbosity", "v", 0, "Set the verbosity level")

	if err := renewCertificatesCmd.MarkFlagRequired("config"); err != nil {
		logger.Fatal(err, "marking config as required")
	}
}

// newRenewerForCmd builds dependencies & returns a ready to-use Renewer.
func newRenewerForCmd(ctx context.Context, cfg *certificates.RenewalConfig) (*certificates.Renewer, func(), error) {
	deps, err := dependencies.NewFactory().
		WithExecutableBuilder().
		WithKubectl().
		WithUnAuthKubeClient().
		Build(ctx)
	if err != nil {
		return nil, nil, err
	}

	// temporary kubeconfig
	kubeCfgPath, cleanup, err := createTempKubeconfig(cfg.ClusterName)
	if err != nil {
		return nil, nil, err
	}

	kubeClient := deps.UnAuthKubeClient.KubeconfigClient(kubeCfgPath)

	os := cfg.OS
	if os == string(v1alpha1.Ubuntu) || os == string(v1alpha1.RedHat) {
		os = string(certificates.OSTypeLinux)
	}
	osRenewer, err := certificates.BuildOSRenewer(os)
	if err != nil {
		cleanup()
		return nil, nil, err
	}

	renewer, err := certificates.NewRenewer(kubeClient, osRenewer)
	if err != nil {
		cleanup()
		return nil, nil, err
	}

	return renewer, cleanup, nil
}

func (rc *renewCertificatesOptions) renewCertificates(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	cfg, err := certificates.ParseConfig(rc.configFile)
	if err != nil {
		return err
	}
	if err = certificates.ValidateComponentWithConfig(rc.component, cfg); err != nil {
		return err
	}

	renewer, cleanup, err := newRenewerForCmd(ctx, cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	return renewer.RenewCertificates(ctx, cfg, rc.component)
}

func createTempKubeconfig(clusterName string) (string, func(), error) {
	originalPath := kubeconfig.FromClusterName(clusterName)
	tempPath := fmt.Sprintf("%s.backup.%d", originalPath, time.Now().Unix())

	fmt.Printf("DEBUG: Creating temp kubeconfig\n")
	fmt.Printf("DEBUG: originalPath = %s\n", originalPath)
	fmt.Printf("DEBUG: tempPath = %s\n", tempPath)

	if err := copyFile(originalPath, tempPath); err != nil {
		return "", nil, fmt.Errorf("failed to copy kubeconfig: %v", err)
	}

	if _, err := os.Stat(tempPath); err != nil {
		return "", nil, fmt.Errorf("temp file not created: %v", err)
	}

	fmt.Printf("DEBUG: Temp kubeconfig created successfully\n")

	cleanup := func() {
		fmt.Printf("DEBUG: Cleaning up temp kubeconfig: %s\n", tempPath)
		os.Remove(tempPath)
	}
	return tempPath, cleanup, nil
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}
