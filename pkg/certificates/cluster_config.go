package certificates

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"
)

// SSH configuration for the cluster.
type clusterSSHConfig struct {
	SSHKeyPath  string
	SSHUsername string
}

// findControlPlaneUsername looks for a VSphereMachineConfig with control-plane annotation.
func findControlPlaneUsername(documents []string) string {
	for _, doc := range documents {
		if doc == "" {
			continue
		}

		// check if this document is a VSphereMachineConfig with control-plane annotation
		var machineConfig struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Annotations map[string]string `yaml:"annotations"`
				Name        string            `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				OSFamily string `yaml:"osFamily"`
				Users    []struct {
					Name string `yaml:"name"`
				} `yaml:"users"`
			} `yaml:"spec"`
		}

		if err := yaml.Unmarshal([]byte(doc), &machineConfig); err != nil {
			// skip documents that don't match this structure
			continue
		}

		// check if this is a VSphereMachineConfig with control-plane annotation
		if machineConfig.Kind == "VSphereMachineConfig" &&
			machineConfig.Metadata.Annotations != nil &&
			machineConfig.Metadata.Annotations["anywhere.eks.amazonaws.com/control-plane"] == "true" {
			// found the control plane machine config
			if len(machineConfig.Spec.Users) > 0 {
				username := machineConfig.Spec.Users[0].Name
				fmt.Printf("Found SSH username '%s' in VSphereMachineConfig for control plane\n", username)
				return username
			}
		}
	}
	return ""
}

// findAnyMachineConfigUsername looks for any VSphereMachineConfig with a username.
func findAnyMachineConfigUsername(documents []string) string {
	for _, doc := range documents {
		if doc == "" {
			continue
		}

		var machineConfig struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Users []struct {
					Name string `yaml:"name"`
				} `yaml:"users"`
			} `yaml:"spec"`
		}

		if err := yaml.Unmarshal([]byte(doc), &machineConfig); err != nil {
			continue
		}

		if machineConfig.Kind == "VSphereMachineConfig" && len(machineConfig.Spec.Users) > 0 {
			username := machineConfig.Spec.Users[0].Name
			fmt.Printf("Found SSH username '%s' in VSphereMachineConfig\n", username)
			return username
		}
	}
	return ""
}

// findSSHKeyPath looks for SSHKeyPath in Cluster resource.
func findSSHKeyPath(documents []string) string {
	for _, doc := range documents {
		if doc == "" {
			continue
		}

		var clusterConfig struct {
			Kind string `yaml:"kind"`
			Spec struct {
				ControlPlaneConfiguration struct {
					SSHKeyPath string `yaml:"sshKeyPath"`
				} `yaml:"controlPlaneConfiguration"`
			} `yaml:"spec"`
		}

		if err := yaml.Unmarshal([]byte(doc), &clusterConfig); err != nil {
			continue
		}

		if clusterConfig.Kind == "Cluster" {
			return clusterConfig.Spec.ControlPlaneConfiguration.SSHKeyPath
		}
	}
	return ""
}

// getClusterConfig retrieves SSH configuration from the cluster's configuration.
func getClusterConfig(clusterName string) (*clusterSSHConfig, error) {
	clusterDir := filepath.Join(".", clusterName)
	clusterConfigPath := filepath.Join(clusterDir, fmt.Sprintf("%s-eks-a-cluster.yaml", clusterName))

	data, err := os.ReadFile(clusterConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read cluster config file: %v", err)
	}

	sshConfig := &clusterSSHConfig{}

	// split the YAML file into multiple documents
	documents := strings.Split(string(data), "---")

	// Look for username in control plane config
	sshConfig.SSHUsername = findControlPlaneUsername(documents)

	// If no username found in control plane, look for any machine config
	if sshConfig.SSHUsername == "" {
		sshConfig.SSHUsername = findAnyMachineConfigUsername(documents)
	}

	// Look for SSH key path
	sshConfig.SSHKeyPath = findSSHKeyPath(documents)

	// If no username found, use default
	if sshConfig.SSHUsername == "" {
		sshConfig.SSHUsername = "ec2-user"
		fmt.Printf("No SSH username found in config, using default: %s\n", sshConfig.SSHUsername)
	}

	return sshConfig, nil
}

// createKubernetesClient creates a Kubernetes client from the environment.
func createKubernetesClient() (*kubernetes.Clientset, error) {
	kubeconfig := os.Getenv("KUBECONFIG")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %v", err)
	}

	return clientset, nil
}

// getClusterConfiguration retrieves the kubeadm-config ConfigMap.
func getClusterConfiguration(clientset *kubernetes.Clientset) (
	struct {
		Etcd struct {
			External struct {
				Endpoints []string `yaml:"endpoints"`
			} `yaml:"external"`
		} `yaml:"etcd"`
	},
	error,
) {
	var clusterConfig struct {
		Etcd struct {
			External struct {
				Endpoints []string `yaml:"endpoints"`
			} `yaml:"external"`
		} `yaml:"etcd"`
	}

	cm, err := clientset.CoreV1().ConfigMaps("kube-system").Get(context.TODO(), "kubeadm-config", metav1.GetOptions{})
	if err != nil {
		return clusterConfig, fmt.Errorf("failed to get kubeadm-config: %v", err)
	}

	if err := yaml.Unmarshal([]byte(cm.Data["ClusterConfiguration"]), &clusterConfig); err != nil {
		return clusterConfig, fmt.Errorf("failed to parse cluster configuration: %v", err)
	}

	return clusterConfig, nil
}

// findNodeIP finds the internal IP address of a node.
func findNodeIP(node corev1.Node) string {
	var nodeIP string
	for _, addr := range node.Status.Addresses {
		if addr.Type == "InternalIP" {
			nodeIP = addr.Address
			break
		}
	}
	// If InternalIP not found, fall back to the first address
	if nodeIP == "" && len(node.Status.Addresses) > 0 {
		nodeIP = node.Status.Addresses[0].Address
		fmt.Printf("Warning: InternalIP not found for node %s, using %s instead\n",
			node.Name, nodeIP)
	}
	return nodeIP
}

// detectNodeOS detects the OS type from the node's OSImage.
func detectNodeOS(osImage string) string {
	osImage = strings.ToLower(osImage)
	if strings.Contains(osImage, "bottlerocket") {
		return "bottlerocket"
	} else if strings.Contains(osImage, "ubuntu") {
		return "ubuntu"
	} else if strings.Contains(osImage, "rhel") || strings.Contains(osImage, "red hat") {
		return "redhat"
	}
	fmt.Printf("DEBUG: Could not detect OS from OSImage: %s\n", osImage)
	return ""
}

// processControlPlaneNodes processes control plane nodes to extract IPs and OS type.
func processControlPlaneNodes(nodes *corev1.NodeList) ([]string, string) {
	var nodeIPs []string
	var osType string

	for _, node := range nodes.Items {
		if _, isControlPlane := node.Labels["node-role.kubernetes.io/control-plane"]; isControlPlane {
			nodeIP := findNodeIP(node)
			if nodeIP != "" {
				nodeIPs = append(nodeIPs, nodeIP)
			}

			// Use the OS type from the first control plane node
			if osType == "" {
				osType = detectNodeOS(node.Status.NodeInfo.OSImage)
			}
		}
	}

	return nodeIPs, osType
}

// processEtcdEndpoints processes external etcd endpoints to extract IPs.
func processEtcdEndpoints(endpoints []string) []string {
	var etcdIPs []string
	for _, endpoint := range endpoints {
		parts := strings.Split(endpoint, "://")
		if len(parts) != 2 {
			continue
		}
		ip := strings.Split(parts[1], ":")[0]
		etcdIPs = append(etcdIPs, ip)
	}
	return etcdIPs
}

// determineSSHUser determines the SSH user based on OS type and cluster config.
func determineSSHUser(osType, configUsername string) string {
	if osType == "ubuntu" && configUsername != "ubuntu" {
		fmt.Printf("Warning: Overriding SSH user from '%s' to 'ubuntu' for Ubuntu nodes\n", configUsername)
		return "ubuntu"
	} else if osType == "bottlerocket" && configUsername != "ec2-user" {
		fmt.Printf("Warning: Overriding SSH user from '%s' to 'ec2-user' for Bottlerocket nodes\n", configUsername)
		return "ec2-user"
	} else if osType == "rhel" || osType == "redhat" {
		fmt.Printf("Using SSH user '%s' for RHEL/RedHat nodes as specified in cluster config\n", configUsername)
		return configUsername
	}
	return configUsername
}

// BuildConfigFromCluster creates a RenewalConfig from a running cluster.
func BuildConfigFromCluster(clusterName, sshKeyPath string) (*RenewalConfig, error) {
	// Validate SSH key exists
	if _, err := os.Stat(sshKeyPath); err != nil {
		return nil, fmt.Errorf("SSH key file not found: %v", err)
	}

	// Create Kubernetes client
	clientset, err := createKubernetesClient()
	if err != nil {
		return nil, err
	}

	// Get cluster configuration
	clusterConfig, err := getClusterConfiguration(clientset)
	if err != nil {
		return nil, err
	}

	// Get nodes
	nodes, err := clientset.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %v", err)
	}

	// Initialize renewal config
	renewalConfig := &RenewalConfig{
		ClusterName: clusterName,
		ControlPlane: NodeConfig{
			Nodes: []string{},
		},
		Etcd: NodeConfig{
			Nodes: []string{},
		},
	}

	// Process control plane nodes
	controlPlaneIPs, osType := processControlPlaneNodes(nodes)
	renewalConfig.ControlPlane.Nodes = controlPlaneIPs
	renewalConfig.ControlPlane.OS = osType

	// Process etcd nodes if external etcd is configured
	if len(clusterConfig.Etcd.External.Endpoints) > 0 {
		renewalConfig.Etcd.Nodes = processEtcdEndpoints(clusterConfig.Etcd.External.Endpoints)
		renewalConfig.Etcd.OS = osType // Assume same OS as control plane
	}

	// Get SSH configuration
	sshConfig, err := getClusterConfig(clusterName)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster configuration: %v", err)
	}

	// Set SSH configuration
	renewalConfig.ControlPlane.SSHKey = sshKeyPath
	renewalConfig.ControlPlane.SSHUser = determineSSHUser(osType, sshConfig.SSHUsername)
	renewalConfig.Etcd.SSHKey = sshKeyPath
	renewalConfig.Etcd.SSHUser = renewalConfig.ControlPlane.SSHUser

	return renewalConfig, nil
}
