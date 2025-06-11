package cmd

import (
	"fmt"
	"log"

	"github.com/spf13/cobra"

	"github.com/aws/eks-anywhere/pkg/certificates"
	"github.com/aws/eks-anywhere/pkg/constants"
	"github.com/aws/eks-anywhere/pkg/types"
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
	if err := renewCertificatesCmd.MarkFlagRequired("config"); err != nil {
		log.Fatalf("marking config as required: %s", err)
	}
}

func validateComponent(component string) error {
	if component != "" && component != constants.EtcdComponent && component != constants.ControlPlaneComponent {
		return fmt.Errorf("invalid component %q, must be either '%s' or '%s'", component, constants.EtcdComponent, constants.ControlPlaneComponent)
	}
	return nil
}

func (rc *renewCertificatesOptions) renewCertificates(cmd *cobra.Command, _ []string) error {
	if err := validateComponent(rc.component); err != nil {
		return err
	}

	config, err := certificates.ParseConfig(rc.configFile)
	if err != nil {
		return fmt.Errorf("failed to parse config file: %v", err)
	}

	cluster := &types.Cluster{
		Name: config.ClusterName,
	}

	var osType string
	if rc.component == constants.EtcdComponent && len(config.Etcd.Nodes) > 0 {
		osType = config.Etcd.OS
	} else if rc.component == constants.ControlPlaneComponent {
		osType = config.ControlPlane.OS
	} else {
		osType = config.ControlPlane.OS
		if len(config.Etcd.Nodes) > 0 && config.Etcd.OS != config.ControlPlane.OS {
			return fmt.Errorf("etcd and control plane use different OS types (%s and %s), please specify --component",
				config.Etcd.OS, config.ControlPlane.OS)
		}
	}

	renewer, err := certificates.NewRenewer(osType)
	if err != nil {
		return fmt.Errorf("failed to create renewer: %v", err)
	}

	// pass empty string for component to renew both etcd and control plane certificates.
	return renewer.RenewCertificates(cmd.Context(), cluster, config, rc.component)
}
