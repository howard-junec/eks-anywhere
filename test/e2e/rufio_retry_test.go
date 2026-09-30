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
	releasev1alpha1 "github.com/aws/eks-anywhere/release/api/v1alpha1"
	"github.com/aws/eks-anywhere/test/framework"
)

const (
	expectedTinkerbellMirrorImageEnv    = "EXPECTED_TINKERBELL_MIRROR_IMAGE"
	expectedTinkerbellBundleImageEnv    = "EXPECTED_TINKERBELL_BUNDLE_IMAGE"
	expectedTinkerbellRuntimeDigestsEnv = "EXPECTED_TINKERBELL_RUNTIME_DIGESTS"
	tinkerbellPublicIPv6OverrideTestEnv = "EKSA_TEST_TINKERBELL_PUBLIC_IPV6"
	localBundleReleaseFile              = "bin/local-bundle-release.yaml"
	rufioJobResource                    = "jobs.bmc.tinkerbell.org"
	rufioMachineResource                = "machines.bmc.tinkerbell.org"
	rufioTaskResource                   = "tasks.bmc.tinkerbell.org"
	rufioRetryLogMessage                = "power-off attempt failed; requeuing"
)

func TestTinkerbellKubernetes136UbuntuRufioHardOffRetryRegistryMirror(t *testing.T) {
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
		framework.NewTinkerbell(t, framework.WithUbuntu136Tinkerbell()),
		framework.WithClusterSingleNode(v1alpha1.Kube136),
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
	test.CreateCluster(framework.WithControlPlaneWaitTimeout("20m"))
	test.ValidateControlPlaneNodes(framework.ValidateControlPlaneNoTaints, framework.ValidateControlPlaneLabels)

	ctx := context.Background()
	kubeconfig := test.KubeconfigFilePath()
	assertTinkerbellImage(t, ctx, test, kubeconfig, bundleImage, runtimeDigests)

	connection := spareWorkerConnection(t, ctx, test, kubeconfig)
	if connection.ProviderOptions != nil && connection.ProviderOptions.RPC != nil {
		t.Fatal("Safe fault injection does not support BMC connections that can bypass connection.host through RPC")
	}
	connectionObject := toUnstructuredConnection(t, connection)
	namePrefix := test.ClusterName + "-rufio-retry"
	powerOnTask := namePrefix + "-on"
	powerOffJob := namePrefix + "-off"
	powerOffTask := powerOffJob + "-task-0"
	faultMachine := namePrefix + "-machine"
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
		newRufioMachine(faultMachine, toUnstructuredConnection(t, faultConnection)),
	); err != nil {
		t.Fatalf("Failed to create fault-injected BMC Machine: %v", err)
	}
	if err := test.KubectlClient.Apply(
		ctx,
		kubeconfig,
		newRufioJob(powerOffJob, faultMachine, "off"),
	); err != nil {
		t.Fatalf("Failed to create fault-injected power-off Job: %v", err)
	}

	retryingTask, err := waitForRufioTaskCondition(ctx, test, kubeconfig, powerOffTask, "Retrying")
	if err != nil {
		t.Fatalf("Power-off Task did not enter Retrying: %v", err)
	}
	taskUID := retryingTask.GetUID()
	if taskUID == "" {
		t.Fatal("Fault-injected power-off Task has no UID")
	}
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
	assertInitialRufioRetryBackoff(t, retryLogs[0])
	assertInitialRufioRetryBackoff(t, retryLogs[1])
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
	assertInitialRufioRetryBackoff(t, resumedRetryLogs[0])

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
	if _, err := waitForRufioJobCondition(ctx, test, kubeconfig, powerOffJob, "Completed"); err != nil {
		t.Fatalf("Power-off Job did not complete after its Task recovered: %v", err)
	}
	sparePoweredOff = true

	deleteRufioRetryResources(
		t,
		ctx,
		test,
		kubeconfig,
		powerOnTask,
		powerOffTask,
		powerOffJob,
		faultMachine,
		cleanupTask,
		faultService,
	)
	test.DeleteCluster()
	test.ValidateHardwareDecommissioned()
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

func newRufioMachine(name string, connection map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "bmc.tinkerbell.org/v1alpha1",
			"kind":       "Machine",
			"metadata": map[string]any{
				"name":      name,
				"namespace": constants.EksaSystemNamespace,
			},
			"spec": map[string]any{
				"connection": connection,
			},
		},
	}
}

func newRufioJob(name, machineName, powerAction string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "bmc.tinkerbell.org/v1alpha1",
			"kind":       "Job",
			"metadata": map[string]any{
				"name":      name,
				"namespace": constants.EksaSystemNamespace,
			},
			"spec": map[string]any{
				"machineRef": map[string]any{
					"name":      machineName,
					"namespace": constants.EksaSystemNamespace,
				},
				"tasks": []any{
					map[string]any{
						"powerAction": powerAction,
					},
				},
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
			return false, nil
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

func waitForRufioJobCondition(
	ctx context.Context,
	test *framework.ClusterE2ETest,
	kubeconfig, jobName, conditionType string,
) (*unstructured.Unstructured, error) {
	var job *unstructured.Unstructured
	err := wait.PollUntilContextTimeout(ctx, 3*time.Second, 4*time.Minute, true, func(ctx context.Context) (bool, error) {
		job = &unstructured.Unstructured{}
		if err := test.KubectlClient.GetObject(
			ctx,
			rufioJobResource,
			jobName,
			constants.EksaSystemNamespace,
			kubeconfig,
			job,
		); err != nil {
			return false, nil
		}
		if conditionTrue(job, "Failed") && conditionType != "Failed" {
			return false, fmt.Errorf("Job %s unexpectedly failed: %s", jobName, conditionMessage(job, "Failed"))
		}
		return conditionTrue(job, conditionType), nil
	})
	if err != nil {
		return job, err
	}
	return job, nil
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

	var seconds float64
	if err := json.Unmarshal(raw, &seconds); err != nil {
		return 0, fmt.Errorf("expected duration string or numeric seconds: %s", raw)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

func TestParseRufioRetryDuration(t *testing.T) {
	for _, testCase := range []struct {
		name string
		raw  string
		want time.Duration
	}{
		{name: "string", raw: `"30s"`, want: 30 * time.Second},
		{name: "numeric seconds", raw: `30.5`, want: 30*time.Second + 500*time.Millisecond},
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

func assertInitialRufioRetryBackoff(t *testing.T, retryLog rufioRetryLog) {
	t.Helper()
	if retryLog.RequeueAfter < 30*time.Second || retryLog.RequeueAfter > 40*time.Second {
		t.Fatalf(
			"Power-off Task scheduled %s retry delay, outside the expected 30s-40s initial backoff",
			retryLog.RequeueAfter,
		)
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
		test.Cluster(),
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
	powerOnTask, powerOffTask, powerOffJob, faultMachine, cleanupTask, faultService string,
) {
	t.Helper()
	for _, resource := range []struct {
		kind string
		name string
	}{
		{rufioTaskResource, powerOnTask},
		{rufioJobResource, powerOffJob},
		{rufioTaskResource, powerOffTask},
		{rufioMachineResource, faultMachine},
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
