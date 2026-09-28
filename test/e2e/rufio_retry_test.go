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
	expectedTinkerbellImageDigestEnv    = "EXPECTED_TINKERBELL_IMAGE_DIGEST"
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
	expectedDigest := os.Getenv(expectedTinkerbellImageDigestEnv)
	if expectedDigest == "" {
		t.Fatalf("%s must identify the candidate Tinkerbell digest", expectedTinkerbellImageDigestEnv)
	}
	candidateRegistry, candidateRepository, candidateTag := parseECRImage(t, expectedImage)
	accessKey := os.Getenv(eksaAWSAccessKeyIDEnv)
	secretKey := os.Getenv(eksaAWSSecretAccessKeyEnv)
	sessionToken := os.Getenv(eksaAWSSessionTokenEnv)
	if accessKey == "" || secretKey == "" || sessionToken == "" {
		t.Fatal("Candidate image credentials were not forwarded to the E2E runner")
	}
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
		accessKey,
		secretKey,
		sessionToken,
	)
	actualDigest := test.GetECRImageDigestWithCredentials(
		candidateRepository,
		candidateTag,
		accessKey,
		secretKey,
		sessionToken,
	)
	if actualDigest != expectedDigest {
		t.Fatalf("Candidate image digest is %q, want %q", actualDigest, expectedDigest)
	}
	mirroredImage := mirrorTinkerbellCandidate(t, test, expectedImage, expectedDigest, candidateTag)
	t.Cleanup(func() {
		test.CleanupDownloadedArtifactsAndImages()
	})

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
	if connection.ProviderOptions != nil && connection.ProviderOptions.RPC != nil {
		t.Fatal("Safe fault injection does not support BMC connections that can bypass connection.host through RPC")
	}
	connectionObject := toUnstructuredConnection(t, connection)
	namePrefix := test.ClusterName + "-rufio-retry"
	powerOnTask := namePrefix + "-on"
	powerOffTask := namePrefix + "-off"
	cleanupTask := namePrefix + "-cleanup"
	faultService := namePrefix + "-fault"
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

	faultPort := connection.Port
	if faultPort == 0 {
		faultPort = 623
	}
	if faultPort < 1 || faultPort > 65535 {
		t.Fatalf("Invalid BMC port %d", faultPort)
	}
	if net.ParseIP(connection.Host) == nil {
		t.Fatalf("Spare worker BMC host %q is not an IP address", connection.Host)
	}
	// Keep the Task generation unchanged: the Service starts with no Endpoints,
	// then DNS begins resolving to the real BMC only after Endpoints are added.
	service := &corev1.Service{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Service",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      faultService,
			Namespace: constants.EksaSystemNamespace,
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: corev1.ClusterIPNone,
			Ports: []corev1.ServicePort{
				{
					Name: "bmc",
					Port: int32(faultPort),
				},
			},
		},
	}
	if err := test.KubectlClient.Apply(ctx, kubeconfig, service); err != nil {
		t.Fatalf("Failed to create fault-injection Service: %v", err)
	}

	faultConnection := connection
	faultConnection.Host = fmt.Sprintf("%s.%s.svc", faultService, constants.EksaSystemNamespace)
	retryStarted := time.Now()
	if err := test.KubectlClient.Apply(
		ctx,
		kubeconfig,
		newRufioTask(powerOffTask, "off", toUnstructuredConnection(t, faultConnection)),
	); err != nil {
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
	initialRetryCount, err := waitForRufioRetryLogCount(ctx, test, kubeconfig, powerOffTask, retryStarted, 1)
	if err != nil {
		t.Fatalf("Power-off Task emitted no retry log: %v", err)
	}
	stableRetryCount, err := waitForRufioRetryLogCount(
		ctx,
		test,
		kubeconfig,
		powerOffTask,
		retryStarted,
		initialRetryCount+1,
	)
	if err != nil {
		t.Fatalf("Power-off Task did not emit an additional timer-driven retry log: %v", err)
	}
	if err := ensureRufioRetryLogsStable(
		ctx,
		test,
		kubeconfig,
		powerOffTask,
		retryStarted,
		stableRetryCount,
		15*time.Second,
	); err != nil {
		t.Fatalf("Power-off Task retried before its minimum backoff elapsed: %v", err)
	}

	endpoints := &corev1.Endpoints{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Endpoints",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      faultService,
			Namespace: constants.EksaSystemNamespace,
		},
		Subsets: []corev1.EndpointSubset{
			{
				Addresses: []corev1.EndpointAddress{
					{IP: connection.Host},
				},
				Ports: []corev1.EndpointPort{
					{
						Name: "bmc",
						Port: int32(faultPort),
					},
				},
			},
		},
	}
	if err := test.KubectlClient.Apply(ctx, kubeconfig, endpoints); err != nil {
		t.Fatalf("Failed to restore fault Service connectivity to the BMC: %v", err)
	}

	completedTask, err := waitForRufioTaskCondition(ctx, test, kubeconfig, powerOffTask, "Completed")
	if err != nil {
		t.Fatalf("Recovered power-off Task did not complete: %v", err)
	}
	if completedTask.GetUID() != taskUID {
		t.Fatalf("Power-off recovery recreated the Task: got UID %q, want %q", completedTask.GetUID(), taskUID)
	}
	sparePoweredOff = true

	deleteRufioRetryResources(t, ctx, test, kubeconfig, powerOnTask, powerOffTask, cleanupTask, faultService)
	test.DeleteCluster()
	test.ValidateHardwareDecommissioned()
}

func mirrorTinkerbellCandidate(
	t *testing.T,
	test *framework.ClusterE2ETest,
	sourceImage, sourceDigest, sourceTag string,
) string {
	t.Helper()

	endpoint := os.Getenv(framework.RegistryEndpointTinkerbellVar)
	port := os.Getenv(framework.RegistryPortTinkerbellVar)
	if port == "" {
		port = "443"
	}
	if endpoint == "" || sourceDigest == "" || sourceTag == "" {
		t.Fatalf("Invalid candidate image or Tinkerbell registry configuration")
	}

	targetImage := fmt.Sprintf(
		"%s/eks-anywhere/tinkerbell/tinkerbell:%s",
		net.JoinHostPort(endpoint, port),
		sourceTag,
	)
	sourceReference := sourceImage + "@" + sourceDigest
	test.Run("docker", "pull", sourceReference)
	test.Run("docker", "tag", sourceReference, targetImage)
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

func parseECRImage(t *testing.T, image string) (registry, repository, tag string) {
	t.Helper()

	slashIndex := strings.Index(image, "/")
	tagIndex := strings.LastIndex(image, ":")
	if slashIndex <= 0 || tagIndex <= slashIndex+1 || tagIndex == len(image)-1 {
		t.Fatalf("Invalid ECR image URI %q", image)
	}
	return image[:slashIndex], image[slashIndex+1 : tagIndex], image[tagIndex+1:]
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

func waitForRufioRetryLogCount(
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig, taskName string,
	since time.Time,
	want int,
) (int, error) {
	count := 0
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		var err error
		count, err = rufioRetryLogCount(ctx, test, kubeconfig, taskName, since)
		if err != nil {
			return false, nil
		}
		return count >= want, nil
	})
	return count, err
}

func ensureRufioRetryLogsStable(
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig, taskName string,
	since time.Time,
	want int,
	duration time.Duration,
) error {
	stableSince := time.Now()
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, duration+10*time.Second, true, func(ctx context.Context) (bool, error) {
		count, err := rufioRetryLogCount(ctx, test, kubeconfig, taskName, since)
		if err != nil {
			return false, nil
		}
		if count != want {
			return false, fmt.Errorf("retry log count changed from %d to %d during the quiet period", want, count)
		}
		return time.Since(stableSince) >= duration, nil
	})
}

func rufioRetryLogCount(
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig, taskName string,
	since time.Time,
) (int, error) {
	pod, err := test.KubectlClient.GetPodNameByLabel(ctx, constants.EksaSystemNamespace, "app=tinkerbell", kubeconfig)
	if err != nil {
		return 0, err
	}
	logs, err := test.KubectlClient.GetPodLogsSince(ctx, constants.EksaSystemNamespace, pod, "tinkerbell", kubeconfig, since)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, taskName) && strings.Contains(line, rufioRetryLogMessage) {
			count++
		}
	}
	return count, nil
}

func deleteRufioRetryResources(
	t *testing.T,
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig string,
	powerOnTask, powerOffTask, cleanupTask, faultService string,
) {
	t.Helper()
	for _, resource := range []struct {
		kind string
		name string
	}{
		{rufioTaskResource, powerOnTask},
		{rufioTaskResource, powerOffTask},
		{rufioTaskResource, cleanupTask},
		{"endpoints", faultService},
		{"service", faultService},
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
