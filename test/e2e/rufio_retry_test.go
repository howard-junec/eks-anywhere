//go:build e2e && (tinkerbell || all_providers)
// +build e2e
// +build tinkerbell all_providers

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/yaml"

	"github.com/aws/eks-anywhere/internal/pkg/api"
	"github.com/aws/eks-anywhere/pkg/api/v1alpha1"
	rufiov1alpha1 "github.com/aws/eks-anywhere/pkg/api/v1alpha1/thirdparty/tinkerbell/rufio"
	"github.com/aws/eks-anywhere/pkg/constants"
	releasev1alpha1 "github.com/aws/eks-anywhere/release/api/v1alpha1"
	"github.com/aws/eks-anywhere/test/framework"
)

const (
	expectedTinkerbellImageEnv          = "EXPECTED_TINKERBELL_IMAGE"
	tinkerbellPublicIPv6OverrideTestEnv = "EKSA_TEST_TINKERBELL_PUBLIC_IPV6"
	localBundleReleaseFile              = "bin/local-bundle-release.yaml"
	eksaAWSAccessKeyIDEnv               = "EKSA_AWS_ACCESS_KEY_ID"
	eksaAWSSecretAccessKeyEnv           = "EKSA_AWS_SECRET_ACCESS_KEY"
	eksaAWSSessionTokenEnv              = "EKSA_AWS_SESSION_TOKEN"
	rufioMachineResource                = "machines.bmc.tinkerbell.org"
	rufioTaskResource                   = "tasks.bmc.tinkerbell.org"
	rufioRetryLogMessage                = "power-off attempt failed; requeuing"
)

func TestTinkerbellKubernetes136UbuntuRufioHardOffRetryRegistryMirror(t *testing.T) {
	expectedImage := os.Getenv(expectedTinkerbellImageEnv)
	if expectedImage == "" {
		t.Fatalf("%s must identify the candidate Tinkerbell image", expectedTinkerbellImageEnv)
	}
	candidateRegistry := strings.SplitN(expectedImage, "/", 2)[0]
	// Keep this IPv4-only test from using the kind node's IPv4-mapped address
	// as an auto-detected public IPv6 address.
	t.Setenv(tinkerbellPublicIPv6OverrideTestEnv, "::")

	test := framework.NewClusterE2ETest(
		t,
		framework.NewTinkerbell(t, framework.WithUbuntu136Tinkerbell()),
		framework.WithClusterSingleNode(v1alpha1.Kube136),
		framework.WithControlPlaneHardware(1),
		framework.WithWorkerHardware(1),
		framework.WithRegistryMirrorEndpointAndCert(constants.TinkerbellProviderName),
	)
	test.LoginToECRWithCredentials(
		candidateRegistry,
		os.Getenv(eksaAWSAccessKeyIDEnv),
		os.Getenv(eksaAWSSecretAccessKeyEnv),
		os.Getenv(eksaAWSSessionTokenEnv),
	)
	mirroredImage := mirrorTinkerbellCandidate(t, test, expectedImage)
	t.Cleanup(test.CleanupDownloadedArtifactsAndImages)

	test.GenerateClusterConfig()
	test.DownloadImages()
	test.ImportImages()
	test.GenerateHardwareConfig()
	test.GenerateSupportBundleOnCleanupIfTestFailed()
	test.CreateCluster(framework.WithControlPlaneWaitTimeout("20m"))
	test.ValidateControlPlaneNodes(framework.ValidateControlPlaneNoTaints, framework.ValidateControlPlaneLabels)

	ctx := context.Background()
	kubeconfig := test.KubeconfigFilePath()
	assertTinkerbellImage(t, ctx, test, kubeconfig, mirroredImage)

	connection := spareWorkerConnection(t, ctx, test, kubeconfig)
	connectionObject := toUnstructuredConnection(t, connection)
	namePrefix := test.ClusterName + "-rufio-retry"
	powerOnTask := namePrefix + "-on"
	powerOffTask := namePrefix + "-off"
	cleanupTask := namePrefix + "-cleanup"
	dummySecret := namePrefix + "-dummy"
	sparePoweredOff := false

	t.Cleanup(func() {
		if sparePoweredOff {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		t.Log("Ensuring the spare Tinkerbell worker is powered off")
		if err := test.KubectlClient.Apply(cleanupCtx, kubeconfig, newRufioTask(cleanupTask, "off", connectionObject)); err != nil {
			t.Logf("Failed to create spare-worker cleanup Task: %v", err)
			return
		}
		if _, err := waitForRufioTaskCondition(cleanupCtx, test, kubeconfig, cleanupTask, "Completed"); err != nil {
			t.Logf("Failed to power off spare worker during cleanup: %v", err)
		}
	})

	if err := test.KubectlClient.Apply(ctx, kubeconfig, newRufioTask(powerOnTask, "on", connectionObject)); err != nil {
		t.Fatalf("Failed to create spare-worker power-on Task: %v", err)
	}
	if _, err := waitForRufioTaskCondition(ctx, test, kubeconfig, powerOnTask, "Completed"); err != nil {
		t.Fatalf("Spare-worker power-on Task did not complete: %v", err)
	}

	secret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Secret",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      dummySecret,
			Namespace: constants.EksaSystemNamespace,
		},
		Type: corev1.SecretTypeBasicAuth,
		StringData: map[string]string{
			corev1.BasicAuthUsernameKey: "unused",
			corev1.BasicAuthPasswordKey: "unused",
		},
	}
	if err := test.KubectlClient.Apply(ctx, kubeconfig, secret); err != nil {
		t.Fatalf("Failed to create dummy BMC Secret: %v", err)
	}

	unreachableConnection := map[string]any{
		"host": "127.0.0.1",
		"port": int64(623),
		"authSecretRef": map[string]any{
			"name":      dummySecret,
			"namespace": constants.EksaSystemNamespace,
		},
		"insecureTLS": true,
	}
	retryStarted := time.Now()
	if err := test.KubectlClient.Apply(ctx, kubeconfig, newRufioTask(powerOffTask, "off", unreachableConnection)); err != nil {
		t.Fatalf("Failed to create fault-injected power-off Task: %v", err)
	}

	retryingTask, err := waitForRufioTaskCondition(ctx, test, kubeconfig, powerOffTask, "Retrying")
	if err != nil {
		t.Fatalf("Power-off Task did not enter Retrying: %v", err)
	}
	taskUID := retryingTask.GetUID()
	if taskUID == "" {
		t.Fatal("Fault-injected power-off Task has no UID")
	}
	if err := waitForRufioRetryLogs(ctx, test, kubeconfig, powerOffTask, retryStarted, 2); err != nil {
		t.Fatalf("Power-off Task did not retry from its requeue timer: %v", err)
	}

	patchBytes, err := json.Marshal(map[string]any{
		"spec": map[string]any{
			"connection": connectionObject,
		},
	})
	if err != nil {
		t.Fatalf("Failed to marshal Task recovery patch: %v", err)
	}
	if err := test.KubectlClient.MergePatchResource(
		ctx,
		rufioTaskResource,
		powerOffTask,
		string(patchBytes),
		kubeconfig,
		constants.EksaSystemNamespace,
	); err != nil {
		t.Fatalf("Failed to restore the Task's real BMC connection: %v", err)
	}

	completedTask, err := waitForRufioTaskCondition(ctx, test, kubeconfig, powerOffTask, "Completed")
	if err != nil {
		t.Fatalf("Recovered power-off Task did not complete: %v", err)
	}
	if completedTask.GetUID() != taskUID {
		t.Fatalf("Power-off recovery recreated the Task: got UID %q, want %q", completedTask.GetUID(), taskUID)
	}
	sparePoweredOff = true

	deleteRufioRetryResources(t, ctx, test, kubeconfig, powerOnTask, powerOffTask, cleanupTask, dummySecret)
	test.DeleteCluster()
	test.ValidateHardwareDecommissioned()
}

func mirrorTinkerbellCandidate(t *testing.T, test *framework.ClusterE2ETest, sourceImage string) string {
	t.Helper()

	endpoint := os.Getenv(framework.RegistryEndpointTinkerbellVar)
	port := os.Getenv(framework.RegistryPortTinkerbellVar)
	if port == "" {
		port = "443"
	}
	tagIndex := strings.LastIndex(sourceImage, ":")
	if endpoint == "" || tagIndex <= strings.LastIndex(sourceImage, "/") {
		t.Fatalf("Invalid candidate image or Tinkerbell registry configuration")
	}

	targetImage := fmt.Sprintf(
		"%s/eks-anywhere/tinkerbell/tinkerbell:%s",
		net.JoinHostPort(endpoint, port),
		sourceImage[tagIndex+1:],
	)
	test.Run("docker", "pull", sourceImage)
	test.Run("docker", "tag", sourceImage, targetImage)
	test.Run("docker", "push", targetImage)

	bundleData, err := os.ReadFile(localBundleReleaseFile)
	if err != nil {
		t.Fatalf("Failed to read local bundle: %v", err)
	}
	bundles := &releasev1alpha1.Bundles{}
	if err := yaml.Unmarshal(bundleData, bundles); err != nil {
		t.Fatalf("Failed to parse local bundle: %v", err)
	}
	for i := range bundles.Spec.VersionsBundles {
		boots := &bundles.Spec.VersionsBundles[i].Tinkerbell.TinkerbellStack.Boots
		boots.URI = targetImage
		boots.ImageDigest = ""
	}
	bundleData, err = yaml.Marshal(bundles)
	if err != nil {
		t.Fatalf("Failed to marshal local bundle: %v", err)
	}
	if err := os.WriteFile(localBundleReleaseFile, bundleData, 0o600); err != nil {
		t.Fatalf("Failed to update local bundle: %v", err)
	}

	return targetImage
}

func assertTinkerbellImage(t *testing.T, ctx context.Context, test *framework.ClusterE2ETest, kubeconfig, expected string) {
	t.Helper()
	deployment, err := test.KubectlClient.GetDeployment(ctx, "tinkerbell", constants.EksaSystemNamespace, kubeconfig)
	if err != nil {
		t.Fatalf("Failed to get Tinkerbell deployment: %v", err)
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name != "tinkerbell" {
			continue
		}
		if container.Image != expected {
			t.Fatalf("Tinkerbell deployment uses %q, want candidate image %q", container.Image, expected)
		}
		return
	}
	t.Fatal("Tinkerbell deployment has no tinkerbell container")
}

func spareWorkerConnection(t *testing.T, ctx context.Context, test *framework.ClusterE2ETest, kubeconfig string) rufiov1alpha1.Connection {
	t.Helper()
	hardware, err := test.KubectlClient.GetUnprovisionedTinkerbellHardware(ctx, kubeconfig, constants.EksaSystemNamespace)
	if err != nil {
		t.Fatalf("Failed to list unprovisioned Tinkerbell hardware: %v", err)
	}

	for i := range hardware {
		if hardware[i].Labels[api.HardwareLabelTypeKeyName] != api.Worker || hardware[i].Spec.BMCRef == nil {
			continue
		}
		machine := &rufiov1alpha1.Machine{}
		if err := test.KubectlClient.GetObject(
			ctx,
			rufioMachineResource,
			hardware[i].Spec.BMCRef.Name,
			constants.EksaSystemNamespace,
			kubeconfig,
			machine,
		); err != nil {
			t.Fatalf("Failed to get spare worker BMCMachine %q: %v", hardware[i].Spec.BMCRef.Name, err)
		}
		connection := machine.Spec.Connection
		if connection.AuthSecretRef.Name != "" && connection.AuthSecretRef.Namespace == "" {
			connection.AuthSecretRef.Namespace = constants.EksaSystemNamespace
		}
		return connection
	}

	t.Fatal("No unprovisioned worker hardware with a BMC reference was found")
	return rufiov1alpha1.Connection{}
}

func toUnstructuredConnection(t *testing.T, connection rufiov1alpha1.Connection) map[string]any {
	t.Helper()
	data, err := json.Marshal(connection)
	if err != nil {
		t.Fatalf("Failed to marshal BMC connection: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("Failed to convert BMC connection: %v", err)
	}
	return object
}

func newRufioTask(name, powerAction string, connection map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "bmc.tinkerbell.org/v1alpha1",
			"kind":       "Task",
			"metadata": map[string]any{
				"name":      name,
				"namespace": constants.EksaSystemNamespace,
			},
			"spec": map[string]any{
				"task": map[string]any{
					"powerAction": powerAction,
				},
				"connection": connection,
			},
		},
	}
}

func waitForRufioTaskCondition(
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig, taskName, conditionType string,
) (*unstructured.Unstructured, error) {
	var task *unstructured.Unstructured
	err := wait.PollUntilContextTimeout(ctx, 3*time.Second, 4*time.Minute, true, func(ctx context.Context) (bool, error) {
		task = &unstructured.Unstructured{}
		if err := test.KubectlClient.GetObject(
			ctx,
			rufioTaskResource,
			taskName,
			constants.EksaSystemNamespace,
			kubeconfig,
			task,
		); err != nil {
			return false, err
		}
		if conditionTrue(task, "Failed") && conditionType != "Failed" {
			return false, fmt.Errorf("Task %s unexpectedly failed: %s", taskName, conditionMessage(task, "Failed"))
		}
		return conditionTrue(task, conditionType), nil
	})
	if err != nil {
		return task, err
	}
	return task, nil
}

func conditionTrue(task *unstructured.Unstructured, conditionType string) bool {
	conditions, _, _ := unstructured.NestedSlice(task.Object, "status", "conditions")
	for _, rawCondition := range conditions {
		condition, ok := rawCondition.(map[string]any)
		if !ok {
			continue
		}
		if condition["type"] == conditionType && condition["status"] == "True" {
			return true
		}
	}
	return false
}

func conditionMessage(task *unstructured.Unstructured, conditionType string) string {
	conditions, _, _ := unstructured.NestedSlice(task.Object, "status", "conditions")
	for _, rawCondition := range conditions {
		condition, ok := rawCondition.(map[string]any)
		if !ok || condition["type"] != conditionType {
			continue
		}
		message, _ := condition["message"].(string)
		return message
	}
	return ""
}

func waitForRufioRetryLogs(
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig, taskName string,
	since time.Time,
	want int,
) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		pod, err := test.KubectlClient.GetPodNameByLabel(ctx, constants.EksaSystemNamespace, "app=tinkerbell", kubeconfig)
		if err != nil {
			return false, nil
		}
		logs, err := test.KubectlClient.GetPodLogsSince(ctx, constants.EksaSystemNamespace, pod, "tinkerbell", kubeconfig, since)
		if err != nil {
			return false, nil
		}
		matches := 0
		for _, line := range strings.Split(logs, "\n") {
			if strings.Contains(line, taskName) && strings.Contains(line, rufioRetryLogMessage) {
				matches++
			}
		}
		return matches >= want, nil
	})
}

func deleteRufioRetryResources(
	t *testing.T,
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig string,
	powerOnTask, powerOffTask, cleanupTask, dummySecret string,
) {
	t.Helper()
	for _, resource := range []struct {
		kind string
		name string
	}{
		{rufioTaskResource, powerOnTask},
		{rufioTaskResource, powerOffTask},
		{rufioTaskResource, cleanupTask},
		{"secret", dummySecret},
	} {
		if _, err := test.KubectlClient.ExecuteCommand(
			ctx,
			"delete",
			resource.kind,
			resource.name,
			"--namespace", constants.EksaSystemNamespace,
			"--kubeconfig", kubeconfig,
			"--ignore-not-found=true",
			"--wait=true",
		); err != nil {
			t.Fatalf("Failed to delete %s %s: %v", resource.kind, resource.name, err)
		}
	}
}
