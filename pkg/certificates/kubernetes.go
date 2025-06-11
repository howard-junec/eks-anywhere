package certificates

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// KubernetesClient provides methods for interacting with Kubernetes
type KubernetesClient interface {
	// InitClient initializes the Kubernetes client
	InitClient() error

	// CheckAPIServerReachability checks if the Kubernetes API server is reachable
	CheckAPIServerReachability(ctx context.Context) error

	// BackupKubeadmConfig backs up the kubeadm-config ConfigMap
	BackupKubeadmConfig(ctx context.Context, backupDir string) error

	// UpdateAPIServerEtcdClientSecret updates the apiserver-etcd-client secret
	UpdateAPIServerEtcdClientSecret(ctx context.Context, clusterName, backupDir string) error
}

// DefaultKubernetesClient is the default implementation of KubernetesClient
type DefaultKubernetesClient struct {
	client kubernetes.Interface
}

// NewKubernetesClient creates a new DefaultKubernetesClient
func NewKubernetesClient() *DefaultKubernetesClient {
	return &DefaultKubernetesClient{}
}

// InitClient initializes the Kubernetes client
func (k *DefaultKubernetesClient) InitClient() error {
	if k.client != nil {
		return nil
	}

	kubeconfig := os.Getenv("KUBECONFIG")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return fmt.Errorf("building kubeconfig: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("creating kubernetes client: %v", err)
	}

	k.client = clientset
	return nil
}

// CheckAPIServerReachability checks if the Kubernetes API server is reachable
func (k *DefaultKubernetesClient) CheckAPIServerReachability(ctx context.Context) error {
	for i := 0; i < 5; i++ {
		_, err := k.client.Discovery().ServerVersion()
		if err == nil {
			return nil
		}
		time.Sleep(10 * time.Second)
	}
	return fmt.Errorf("kubernetes API server is not reachable")
}

// BackupKubeadmConfig backs up the kubeadm-config ConfigMap
func (k *DefaultKubernetesClient) BackupKubeadmConfig(ctx context.Context, backupDir string) error {
	cm, err := k.client.CoreV1().ConfigMaps("kube-system").Get(ctx, "kubeadm-config", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("getting kubeadm-config: %v", err)
	}

	backupPath := filepath.Join(backupDir, "kubeadm-config.yaml")
	if err := os.WriteFile(backupPath, []byte(cm.Data["ClusterConfiguration"]), 0o600); err != nil {
		return fmt.Errorf("writing kubeadm config backup: %v", err)
	}

	return nil
}

// UpdateAPIServerEtcdClientSecret updates the apiserver-etcd-client secret
func (k *DefaultKubernetesClient) UpdateAPIServerEtcdClientSecret(ctx context.Context, clusterName, backupDir string) error {
	fmt.Printf("Updating %s-apiserver-etcd-client secret...\n", clusterName)

	if err := k.ensureNamespaceExists(ctx, "eksa-system"); err != nil {
		return fmt.Errorf("ensuring eksa-system namespace exists: %v", err)
	}

	crtPath := filepath.Join(backupDir, tempLocalEtcdCertsDir, "apiserver-etcd-client.crt")
	keyPath := filepath.Join(backupDir, tempLocalEtcdCertsDir, "apiserver-etcd-client.key")

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
	secret, err := k.client.CoreV1().Secrets("eksa-system").Get(ctx, secretName, metav1.GetOptions{})
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

		_, err = k.client.CoreV1().Secrets("eksa-system").Create(ctx, secret, metav1.CreateOptions{})
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

		_, err = k.client.CoreV1().Secrets("eksa-system").Update(ctx, secret, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update secret %s: %v", secretName, err)
		}
	}

	fmt.Printf("✅ Successfully updated %s secret.\n", secretName)
	return nil
}

// ensureNamespaceExists ensures that the specified namespace exists
func (k *DefaultKubernetesClient) ensureNamespaceExists(ctx context.Context, namespace string) error {
	_, err := k.client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			_, err = k.client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
			if err != nil {
				return fmt.Errorf("create namespace %s: %v", namespace, err)
			}
			fmt.Printf("Created namespace %s\n", namespace)
		} else {
			return fmt.Errorf("check namespace %s: %v", namespace, err)
		}
	}
	return nil
}
