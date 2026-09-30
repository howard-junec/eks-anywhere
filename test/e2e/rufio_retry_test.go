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
	"sort"
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
	"github.com/aws/eks-anywhere/pkg/executables"
	"github.com/aws/eks-anywhere/pkg/types"
	releasev1alpha1 "github.com/aws/eks-anywhere/release/api/v1alpha1"
	"github.com/aws/eks-anywhere/test/framework"
)

const (
	expectedTinkerbellMirrorImageEnv    = "EXPECTED_TINKERBELL_MIRROR_IMAGE"
	expectedTinkerbellBundleImageEnv    = "EXPECTED_TINKERBELL_BUNDLE_IMAGE"
	expectedTinkerbellRuntimeDigestsEnv = "EXPECTED_TINKERBELL_RUNTIME_DIGESTS"
	tinkerbellPublicIPv6OverrideTestEnv = "EKSA_TEST_TINKERBELL_PUBLIC_IPV6"
	localBundleReleaseFile              = "bin/local-bundle-release.yaml"
	rufioMachineResource                = "machines.bmc.tinkerbell.org"
	rufioTaskResource                   = "tasks.bmc.tinkerbell.org"
	rufioRetryLogMessage                = "power-off attempt failed; requeuing"
	rufioRetryInitialInterval           = 30 * time.Second
	rufioRetryMaxInterval               = 5 * time.Minute
	rufioRetryStepInterval              = 2 * time.Minute
	rufioRetryJitter                    = 10 * time.Second
	rufioTaskConditionTimeout           = 4 * time.Minute
	rufioRecoveryTimeout                = rufioRetryMaxInterval + 3*time.Minute
)

func TestTinkerbellKubernetes135UbuntuRufioHardOffRetryRegistryMirror(t *testing.T) {
	mirrorImage := os.Getenv(expectedTinkerbellMirrorImageEnv)
	if mirrorImage == "" {
		t.Fatalf("%s must identify the mirrored candidate image", expectedTinkerbellMirrorImageEnv)
	}
	bundleImage := os.Getenv(expectedTinkerbellBundleImageEnv)
	if bundleImage == "" {
		t.Fatalf("%s must identify the candidate bundle image", expectedTinkerbellBundleImageEnv)
	}
	runtimeDigests := parseImageDigests(t, os.Getenv(expectedTinkerbellRuntimeDigestsEnv))
	// Keep this IPv4-only test from using the kind node's IPv4-mapped address
	// as an auto-detected public IPv6 address.
	t.Setenv(tinkerbellPublicIPv6OverrideTestEnv, "::")

	test := framework.NewClusterE2ETest(
		t,
		framework.NewTinkerbell(t, framework.WithUbuntu135Tinkerbell()),
		framework.WithClusterSingleNode(v1alpha1.Kube135),
		framework.WithControlPlaneHardware(1),
		framework.WithWorkerHardware(1),
		framework.WithRegistryMirrorEndpointAndCert(constants.TinkerbellProviderName),
	)
	t.Cleanup(func() {
		test.CleanupDownloadedArtifactsAndImages()
	})

	setTinkerbellBundleImage(t, mirrorImage)
	test.GenerateClusterConfig()
	test.DownloadImages()
	test.ImportImages()
	setTinkerbellBundleImage(t, bundleImage)
	test.GenerateHardwareConfig()
	test.GenerateSupportBundleOnCleanupIfTestFailed()
	createCommand := test.StartCreateCluster(framework.WithControlPlaneWaitTimeout("30m"))
	createStopped := false
	t.Cleanup(func() {
		if createStopped {
			return
		}
		if err := createCommand.Stop(); err != nil {
			t.Logf("Stopped in-progress cluster creation during cleanup: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()
	kubeconfig := test.BootstrapKubeconfigFilePath()
	if err := waitForBootstrapTinkerbell(ctx, test, kubeconfig, createCommand); err != nil {
		t.Fatalf("Bootstrap Tinkerbell did not become ready: %v", err)
	}
	assertTinkerbellImage(t, ctx, test, kubeconfig, bundleImage, runtimeDigests)

	connection, err := waitForSpareWorkerConnection(ctx, test, kubeconfig, createCommand)
	if err != nil {
		t.Fatalf("Spare worker BMC connection did not become available: %v", err)
	}
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

	if net.ParseIP(connection.Host) == nil {
		t.Fatalf("Spare worker BMC host %q is not an IP address", connection.Host)
	}
	servicePorts, endpointPorts := rufioFaultServicePorts(t, connection)
	// Keep the Task generation unchanged. The ClusterIP initially has no
	// Endpoints, then forwards to the real BMC after Endpoints are added.
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
			Ports: servicePorts,
		},
	}
	if err := test.KubectlClient.Apply(ctx, kubeconfig, service); err != nil {
		t.Fatalf("Failed to create fault-injection Service: %v", err)
	}
	createdService := &corev1.Service{}
	if err := test.KubectlClient.GetObject(
		ctx,
		"service",
		faultService,
		constants.EksaSystemNamespace,
		kubeconfig,
		createdService,
	); err != nil {
		t.Fatalf("Failed to read fault-injection Service: %v", err)
	}
	if net.ParseIP(createdService.Spec.ClusterIP) == nil {
		t.Fatalf("Fault-injection Service has invalid ClusterIP %q", createdService.Spec.ClusterIP)
	}

	faultConnection := connection
	faultConnection.Host = createdService.Spec.ClusterIP
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
	taskStartTime := rufioTaskStartTime(t, retryingTask)
	tinkerbellPod, err := test.KubectlClient.GetPodNameByLabel(
		ctx,
		constants.EksaSystemNamespace,
		"app=tinkerbell",
		kubeconfig,
	)
	if err != nil {
		t.Fatalf("Failed to find Tinkerbell pod while checking retries: %v", err)
	}
	retryLogs, err := waitForRufioRetryLogs(ctx, test, kubeconfig, tinkerbellPod, powerOffTask, retryStarted, 2)
	if err != nil {
		t.Fatalf("Power-off Task did not emit two retry logs: %v", err)
	}
	assertRufioRetryBackoff(t, taskStartTime, retryLogs[0])
	assertRufioRetryBackoff(t, taskStartTime, retryLogs[1])
	t.Logf(
		"Observed retry delays %s and %s before restarting the Tinkerbell controller",
		retryLogs[0].RequeueAfter,
		retryLogs[1].RequeueAfter,
	)
	if retryInterval := retryLogs[1].Time.Sub(retryLogs[0].Time); retryInterval+2*time.Second < retryLogs[0].RequeueAfter {
		t.Fatalf(
			"Power-off Task retried after %s, earlier than its scheduled %s backoff",
			retryInterval,
			retryLogs[0].RequeueAfter,
		)
	}

	restartStartedAt := time.Now()
	restartedPod := restartTinkerbellController(t, ctx, test, kubeconfig)
	restartedTask, err := waitForRufioTaskCondition(ctx, test, kubeconfig, powerOffTask, "Retrying")
	if err != nil {
		t.Fatalf("Power-off Task did not remain retryable after controller restart: %v", err)
	}
	if restartedTask.GetUID() != taskUID {
		t.Fatalf("Controller restart recreated the Task: got UID %q, want %q", restartedTask.GetUID(), taskUID)
	}
	resumedRetryLogs, err := waitForRufioRetryLogs(
		ctx,
		test,
		kubeconfig,
		restartedPod,
		powerOffTask,
		restartStartedAt,
		1,
	)
	if err != nil {
		t.Fatalf("Restarted controller did not resume the retrying Task: %v", err)
	}
	assertRufioRetryBackoff(t, taskStartTime, resumedRetryLogs[0])
	t.Logf("Controller restart resumed the Task with a %s retry delay", resumedRetryLogs[0].RequeueAfter)

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
				Ports: endpointPorts,
			},
		},
	}
	if err := test.KubectlClient.Apply(ctx, kubeconfig, endpoints); err != nil {
		t.Fatalf("Failed to restore fault Service connectivity to the BMC: %v", err)
	}
	t.Logf("Restored BMC connectivity; waiting up to %s for the scheduled retry", rufioRecoveryTimeout)

	completedTask, err := waitForRufioTaskConditionWithin(
		ctx,
		test,
		kubeconfig,
		powerOffTask,
		"Completed",
		rufioRecoveryTimeout,
	)
	if err != nil {
		t.Fatalf(
			"Recovered power-off Task did not complete: %v; last Task state: %s",
			err,
			rufioTaskStatusSummary(completedTask),
		)
	}
	if completedTask.GetUID() != taskUID {
		t.Fatalf("Power-off recovery recreated the Task: got UID %q, want %q", completedTask.GetUID(), taskUID)
	}
	sparePoweredOff = true

	deleteRufioRetryResources(
		t,
		ctx,
		test,
		kubeconfig,
		powerOnTask,
		powerOffTask,
		cleanupTask,
		faultService,
	)
	if err := createCommand.Stop(); err != nil {
		t.Logf("Stopped cluster creation after focused Rufio retry validation: %v", err)
	}
	createStopped = true
}

func setTinkerbellBundleImage(t *testing.T, image string) {
	t.Helper()

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
		boots.URI = image
		boots.ImageDigest = ""
	}
	bundleData, err = yaml.Marshal(bundles)
	if err != nil {
		t.Fatalf("Failed to marshal local bundle: %v", err)
	}
	if err := os.WriteFile(localBundleReleaseFile, bundleData, 0o600); err != nil {
		t.Fatalf("Failed to update local bundle: %v", err)
	}
}

func parseImageDigests(t *testing.T, value string) map[string]struct{} {
	t.Helper()
	digests := make(map[string]struct{})
	for _, digest := range strings.Split(value, ",") {
		digest = strings.TrimSpace(digest)
		if digest == "" {
			continue
		}
		if !strings.HasPrefix(digest, "sha256:") {
			t.Fatalf("%s contains invalid digest %q", expectedTinkerbellRuntimeDigestsEnv, digest)
		}
		digests[digest] = struct{}{}
	}
	if len(digests) == 0 {
		t.Fatalf("%s must contain at least one image digest", expectedTinkerbellRuntimeDigestsEnv)
	}
	return digests
}

func assertTinkerbellImage(
	t *testing.T,
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig, expected string,
	expectedDigests map[string]struct{},
) {
	t.Helper()
	deployment, err := test.KubectlClient.GetDeployment(ctx, "tinkerbell", constants.EksaSystemNamespace, kubeconfig)
	if err != nil {
		t.Fatalf("Failed to get Tinkerbell deployment: %v", err)
	}
	foundContainer := false
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name != "tinkerbell" {
			continue
		}
		foundContainer = true
		if container.Image != expected {
			t.Fatalf("Tinkerbell deployment uses %q, want candidate image %q", container.Image, expected)
		}
		break
	}
	if !foundContainer {
		t.Fatal("Tinkerbell deployment has no tinkerbell container")
	}

	pods, err := test.KubectlClient.GetPods(
		ctx,
		executables.WithKubeconfig(kubeconfig),
		executables.WithNamespace(constants.EksaSystemNamespace),
		executables.WithSelector("app=tinkerbell"),
	)
	if err != nil {
		t.Fatalf("Failed to get Tinkerbell pods: %v", err)
	}
	for _, pod := range pods {
		for _, status := range pod.Status.ContainerStatuses {
			if status.Name != "tinkerbell" || !status.Ready {
				continue
			}
			digest := digestFromImageID(status.ImageID)
			if _, ok := expectedDigests[digest]; !ok {
				t.Fatalf("Tinkerbell pod resolved image digest %q, want one of %v", digest, mapKeys(expectedDigests))
			}
			return
		}
	}
	t.Fatal("No ready Tinkerbell container was found")
}

func digestFromImageID(imageID string) string {
	if index := strings.LastIndex(imageID, "@"); index >= 0 {
		return imageID[index+1:]
	}
	if index := strings.LastIndex(imageID, "sha256:"); index >= 0 {
		return imageID[index:]
	}
	return imageID
}

func mapKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func waitForBootstrapTinkerbell(
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig string,
	createCommand *framework.RunningCommand,
) error {
	return wait.PollUntilContextTimeout(ctx, 5*time.Second, 15*time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := commandExitError(createCommand); err != nil {
			return false, err
		}
		if _, err := os.Stat(kubeconfig); err != nil {
			return false, nil
		}
		deployment, err := test.KubectlClient.GetDeployment(ctx, "tinkerbell", constants.EksaSystemNamespace, kubeconfig)
		if err != nil || deployment.Status.AvailableReplicas == 0 {
			return false, nil
		}
		pods, err := test.KubectlClient.GetPods(
			ctx,
			executables.WithKubeconfig(kubeconfig),
			executables.WithNamespace(constants.EksaSystemNamespace),
			executables.WithSelector("app=tinkerbell"),
		)
		if err != nil {
			return false, nil
		}
		for _, pod := range pods {
			for _, status := range pod.Status.ContainerStatuses {
				if status.Name == "tinkerbell" && status.Ready {
					return true, nil
				}
			}
		}
		return false, nil
	})
}

func waitForSpareWorkerConnection(
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig string,
	createCommand *framework.RunningCommand,
) (rufiov1alpha1.Connection, error) {
	var connection rufiov1alpha1.Connection
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		if err := commandExitError(createCommand); err != nil {
			return false, err
		}
		var found bool
		var err error
		connection, found, err = findSpareWorkerConnection(ctx, test, kubeconfig)
		return found, err
	})
	return connection, err
}

func findSpareWorkerConnection(
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig string,
) (rufiov1alpha1.Connection, bool, error) {
	hardware, err := test.KubectlClient.GetUnprovisionedTinkerbellHardware(ctx, kubeconfig, constants.EksaSystemNamespace)
	if err != nil {
		return rufiov1alpha1.Connection{}, false, nil
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
			return rufiov1alpha1.Connection{}, false, fmt.Errorf(
				"getting spare worker BMCMachine %q: %w",
				hardware[i].Spec.BMCRef.Name,
				err,
			)
		}
		connection := machine.Spec.Connection
		if connection.AuthSecretRef.Name != "" && connection.AuthSecretRef.Namespace == "" {
			connection.AuthSecretRef.Namespace = constants.EksaSystemNamespace
		}
		return connection, true, nil
	}

	return rufiov1alpha1.Connection{}, false, nil
}

func commandExitError(command *framework.RunningCommand) error {
	select {
	case <-command.Done():
		err := command.Wait()
		output := command.Output()
		const maxOutput = 8 * 1024
		if len(output) > maxOutput {
			output = output[len(output)-maxOutput:]
		}
		return fmt.Errorf("cluster creation exited before fault injection was ready: %v\n%s", err, output)
	default:
		return nil
	}
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

func rufioFaultServicePorts(
	t *testing.T,
	connection rufiov1alpha1.Connection,
) ([]corev1.ServicePort, []corev1.EndpointPort) {
	t.Helper()

	type portKey struct {
		protocol corev1.Protocol
		port     int
	}
	ports := map[portKey]struct{}{}
	addPort := func(protocol corev1.Protocol, port int) {
		if port < 1 || port > 65535 {
			t.Fatalf("Invalid BMC %s port %d", protocol, port)
		}
		ports[portKey{protocol: protocol, port: port}] = struct{}{}
	}

	addPort(corev1.ProtocolTCP, 443)
	addPort(corev1.ProtocolUDP, 623)
	addPort(corev1.ProtocolTCP, 16992)
	if connection.Port != 0 {
		addPort(corev1.ProtocolTCP, connection.Port)
		addPort(corev1.ProtocolUDP, connection.Port)
	}
	if options := connection.ProviderOptions; options != nil {
		if options.Redfish != nil && options.Redfish.Port != 0 {
			addPort(corev1.ProtocolTCP, options.Redfish.Port)
		}
		if options.IPMITOOL != nil && options.IPMITOOL.Port != 0 {
			addPort(corev1.ProtocolUDP, options.IPMITOOL.Port)
		}
		if options.IntelAMT != nil && options.IntelAMT.Port != 0 {
			addPort(corev1.ProtocolTCP, options.IntelAMT.Port)
		}
	}

	keys := make([]portKey, 0, len(ports))
	for key := range ports {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].port != keys[j].port {
			return keys[i].port < keys[j].port
		}
		return keys[i].protocol < keys[j].protocol
	})

	servicePorts := make([]corev1.ServicePort, 0, len(keys))
	endpointPorts := make([]corev1.EndpointPort, 0, len(keys))
	for _, key := range keys {
		name := fmt.Sprintf("%s-%d", strings.ToLower(string(key.protocol)), key.port)
		servicePorts = append(servicePorts, corev1.ServicePort{
			Name:     name,
			Protocol: key.protocol,
			Port:     int32(key.port),
		})
		endpointPorts = append(endpointPorts, corev1.EndpointPort{
			Name:     name,
			Protocol: key.protocol,
			Port:     int32(key.port),
		})
	}
	return servicePorts, endpointPorts
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
	return waitForRufioTaskConditionWithin(
		ctx,
		test,
		kubeconfig,
		taskName,
		conditionType,
		rufioTaskConditionTimeout,
	)
}

func waitForRufioTaskConditionWithin(
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig, taskName, conditionType string,
	timeout time.Duration,
) (*unstructured.Unstructured, error) {
	var task *unstructured.Unstructured
	var lastGetErr error
	err := wait.PollUntilContextTimeout(ctx, 3*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		currentTask := &unstructured.Unstructured{}
		if err := test.KubectlClient.GetObject(
			ctx,
			rufioTaskResource,
			taskName,
			constants.EksaSystemNamespace,
			kubeconfig,
			currentTask,
		); err != nil {
			lastGetErr = err
			return false, nil
		}
		task = currentTask
		lastGetErr = nil
		if conditionTrue(task, "Failed") && conditionType != "Failed" {
			return false, fmt.Errorf("Task %s unexpectedly failed: %s", taskName, conditionMessage(task, "Failed"))
		}
		return conditionTrue(task, conditionType), nil
	})
	if err != nil {
		if lastGetErr != nil {
			err = fmt.Errorf("%w; last Task read failed: %v", err, lastGetErr)
		}
		return task, err
	}
	return task, nil
}

func rufioTaskStatusSummary(task *unstructured.Unstructured) string {
	if task == nil {
		return "<nil>"
	}
	status, _, _ := unstructured.NestedMap(task.Object, "status")
	summary, err := json.Marshal(map[string]any{
		"uid":         task.GetUID(),
		"annotations": task.GetAnnotations(),
		"status":      status,
	})
	if err != nil {
		return fmt.Sprintf("<unavailable: %v>", err)
	}
	return string(summary)
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

type rufioRetryLog struct {
	Time         time.Time
	RequeueAfter time.Duration
}

func waitForRufioRetryLogs(
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig, podName, taskName string,
	since time.Time,
	want int,
) ([]rufioRetryLog, error) {
	var retryLogs []rufioRetryLog
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		var err error
		retryLogs, err = rufioRetryLogs(ctx, test, kubeconfig, podName, taskName, since)
		if err != nil {
			return false, nil
		}
		return len(retryLogs) >= want, nil
	})
	return retryLogs, err
}

func rufioRetryLogs(
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig, podName, taskName string,
	since time.Time,
) ([]rufioRetryLog, error) {
	logs, err := test.KubectlClient.GetPodLogsSince(
		ctx,
		constants.EksaSystemNamespace,
		podName,
		"tinkerbell",
		kubeconfig,
		since,
	)
	if err != nil {
		return nil, err
	}
	retryLogs := make([]rufioRetryLog, 0)
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, taskName) || !strings.Contains(line, rufioRetryLogMessage) {
			continue
		}
		var entry struct {
			Time         string          `json:"time"`
			RequeueAfter json.RawMessage `json:"requeueAfter"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return nil, fmt.Errorf("parsing Rufio retry log: %w", err)
		}
		logTime, err := time.Parse(time.RFC3339Nano, entry.Time)
		if err != nil {
			return nil, fmt.Errorf("parsing Rufio retry log time %q: %w", entry.Time, err)
		}
		requeueAfter, err := parseRufioRetryDuration(entry.RequeueAfter)
		if err != nil {
			return nil, fmt.Errorf("parsing Rufio retry log requeueAfter: %w", err)
		}
		retryLogs = append(retryLogs, rufioRetryLog{
			Time:         logTime,
			RequeueAfter: requeueAfter,
		})
	}
	sort.Slice(retryLogs, func(i, j int) bool {
		return retryLogs[i].Time.Before(retryLogs[j].Time)
	})
	return retryLogs, nil
}

func parseRufioRetryDuration(raw json.RawMessage) (time.Duration, error) {
	var duration string
	if err := json.Unmarshal(raw, &duration); err == nil {
		return time.ParseDuration(duration)
	}

	var nanoseconds int64
	if err := json.Unmarshal(raw, &nanoseconds); err != nil {
		return 0, fmt.Errorf("expected duration string or numeric nanoseconds: %s", raw)
	}
	return time.Duration(nanoseconds), nil
}

func TestRufioFaultServicePorts(t *testing.T) {
	connection := rufiov1alpha1.Connection{
		Port: 8443,
		ProviderOptions: &rufiov1alpha1.ProviderOptions{
			Redfish: &rufiov1alpha1.RedfishOptions{Port: 9443},
			IPMITOOL: &rufiov1alpha1.IPMITOOLOptions{
				Port: 7623,
			},
			IntelAMT: &rufiov1alpha1.IntelAMTOptions{Port: 16993},
		},
	}
	servicePorts, endpointPorts := rufioFaultServicePorts(t, connection)
	if len(servicePorts) != len(endpointPorts) {
		t.Fatalf("Service ports = %d, endpoint ports = %d", len(servicePorts), len(endpointPorts))
	}

	got := make(map[string]struct{}, len(servicePorts))
	for i := range servicePorts {
		servicePort := servicePorts[i]
		endpointPort := endpointPorts[i]
		if servicePort.Name != endpointPort.Name ||
			servicePort.Protocol != endpointPort.Protocol ||
			servicePort.Port != endpointPort.Port {
			t.Fatalf("Service port %#v does not match endpoint port %#v", servicePort, endpointPort)
		}
		got[fmt.Sprintf("%s/%d", servicePort.Protocol, servicePort.Port)] = struct{}{}
	}

	for _, expected := range []string{
		"TCP/443",
		"UDP/623",
		"TCP/8443",
		"UDP/8443",
		"TCP/9443",
		"UDP/7623",
		"TCP/16992",
		"TCP/16993",
	} {
		if _, ok := got[expected]; !ok {
			t.Errorf("Missing fault Service port %s; got %v", expected, mapKeys(got))
		}
	}
}

func TestParseRufioRetryDuration(t *testing.T) {
	for _, testCase := range []struct {
		name string
		raw  string
		want time.Duration
	}{
		{name: "string", raw: `"30s"`, want: 30 * time.Second},
		{name: "numeric nanoseconds", raw: `30500000000`, want: 30*time.Second + 500*time.Millisecond},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseRufioRetryDuration(json.RawMessage(testCase.raw))
			if err != nil {
				t.Fatalf("parseRufioRetryDuration() error = %v", err)
			}
			if got != testCase.want {
				t.Fatalf("parseRufioRetryDuration() = %s, want %s", got, testCase.want)
			}
		})
	}
}

func rufioTaskStartTime(t *testing.T, task *unstructured.Unstructured) time.Time {
	t.Helper()
	value, found, err := unstructured.NestedString(task.Object, "status", "startTime")
	if err != nil {
		t.Fatalf("Reading Power-off Task startTime: %v", err)
	}
	if !found {
		t.Fatal("Power-off Task has no status.startTime")
	}
	startTime, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("Parsing Power-off Task startTime %q: %v", value, err)
	}
	return startTime
}

func assertRufioRetryBackoff(t *testing.T, taskStartTime time.Time, retryLog rufioRetryLog) {
	t.Helper()
	minimum, maximum := rufioRetryBackoffRange(taskStartTime, retryLog.Time)
	elapsed := retryLog.Time.Sub(taskStartTime)
	if retryLog.RequeueAfter < minimum || retryLog.RequeueAfter > maximum {
		t.Fatalf(
			"Power-off Task scheduled %s retry delay at age %s, outside the expected %s-%s backoff",
			retryLog.RequeueAfter,
			elapsed,
			minimum,
			maximum,
		)
	}
}

func rufioRetryBackoffRange(taskStartTime, retryTime time.Time) (time.Duration, time.Duration) {
	minimum := rufioRetryInitialInterval
	elapsed := retryTime.Sub(taskStartTime)
	for steps := int(elapsed / rufioRetryStepInterval); steps > 0 && minimum < rufioRetryMaxInterval; steps-- {
		minimum *= 2
		if minimum > rufioRetryMaxInterval {
			minimum = rufioRetryMaxInterval
		}
	}
	maximum := minimum + rufioRetryJitter
	if maximum > rufioRetryMaxInterval {
		maximum = rufioRetryMaxInterval
	}
	return minimum, maximum
}

func TestRufioRetryBackoffRange(t *testing.T) {
	start := time.Date(2026, time.September, 30, 10, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		name    string
		elapsed time.Duration
		wantMin time.Duration
		wantMax time.Duration
	}{
		{name: "initial", elapsed: 0, wantMin: 30 * time.Second, wantMax: 40 * time.Second},
		{name: "second tier", elapsed: 2 * time.Minute, wantMin: time.Minute, wantMax: 70 * time.Second},
		{name: "third tier", elapsed: 4 * time.Minute, wantMin: 2 * time.Minute, wantMax: 130 * time.Second},
		{name: "maximum", elapsed: 8 * time.Minute, wantMin: 5 * time.Minute, wantMax: 5 * time.Minute},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			minimum, maximum := rufioRetryBackoffRange(start, start.Add(testCase.elapsed))
			if minimum != testCase.wantMin || maximum != testCase.wantMax {
				t.Fatalf(
					"rufioRetryBackoffRange() = %s-%s, want %s-%s",
					minimum,
					maximum,
					testCase.wantMin,
					testCase.wantMax,
				)
			}
		})
	}
}

func restartTinkerbellController(
	t *testing.T,
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig string,
) string {
	t.Helper()
	oldPod, err := test.KubectlClient.GetPodNameByLabel(ctx, constants.EksaSystemNamespace, "app=tinkerbell", kubeconfig)
	if err != nil {
		t.Fatalf("Failed to find Tinkerbell pod before restart: %v", err)
	}
	if _, err := test.KubectlClient.ExecuteCommand(
		ctx,
		"delete",
		"pod",
		oldPod,
		"--namespace", constants.EksaSystemNamespace,
		"--kubeconfig", kubeconfig,
		"--wait=true",
	); err != nil {
		t.Fatalf("Failed to restart Tinkerbell controller: %v", err)
	}
	if err := test.KubectlClient.WaitForResourceRolledout(
		ctx,
		&types.Cluster{
			Name:           test.ClusterName,
			KubeconfigFile: kubeconfig,
		},
		"5m",
		"tinkerbell",
		constants.EksaSystemNamespace,
		"deployment",
	); err != nil {
		t.Fatalf("Tinkerbell controller did not become ready after restart: %v", err)
	}
	newPod, err := test.KubectlClient.GetPodNameByLabel(ctx, constants.EksaSystemNamespace, "app=tinkerbell", kubeconfig)
	if err != nil {
		t.Fatalf("Failed to find Tinkerbell pod after restart: %v", err)
	}
	if newPod == oldPod {
		t.Fatalf("Tinkerbell controller pod %q was not replaced", oldPod)
	}
	return newPod
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
