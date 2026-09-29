package e2e

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/go-logr/logr"

	"github.com/aws/eks-anywhere/internal/pkg/api"
	"github.com/aws/eks-anywhere/internal/pkg/ssm"
	"github.com/aws/eks-anywhere/pkg/constants"
	"github.com/aws/eks-anywhere/pkg/executables"
	e2etests "github.com/aws/eks-anywhere/test/framework"
)

const (
	tinkerbellInventoryCsvFilePathEnvVar       = "T_TINKERBELL_INVENTORY_CSV"
	tinkerbellControlPlaneNetworkCidrEnvVar    = "T_TINKERBELL_CP_NETWORK_CIDR"
	tinkerbellHardwareS3FileKeyEnvVar          = "T_TINKERBELL_S3_INVENTORY_CSV_KEY"
	tinkerbellAirgappedHardwareS3FileKeyEnvVar = "T_TINKERBELL_S3_AG_INVENTORY_CSV_KEY"
	tinkerbellTestsRe                          = `^.*Tinkerbell.*$`
	e2eHardwareCsvFilePath                     = "e2e-inventory.csv"
	e2eAirgappedHardwareCsvFilePath            = "e2e-ag-inventory.csv"
	maxHardwarePerE2ETestEnvVar                = "T_TINKERBELL_MAX_HARDWARE_PER_TEST"
	tinkerbellDefaultMaxHardwarePerE2ETest     = 4
	tinkerbellBootstrapInterfaceEnvVar         = "T_TINKERBELL_BOOTSTRAP_INTERFACE"
	tinkerbellCIEnvironmentEnvVar              = "T_TINKERBELL_CI_ENVIRONMENT"
	tinkerbellExpectedImageEnvVar              = "EXPECTED_TINKERBELL_IMAGE"
	tinkerbellExpectedImageDigestEnvVar        = "EXPECTED_TINKERBELL_IMAGE_DIGEST"
	tinkerbellMirrorImageEnvVar                = "EXPECTED_TINKERBELL_MIRROR_IMAGE"
	tinkerbellBundleImageEnvVar                = "EXPECTED_TINKERBELL_BUNDLE_IMAGE"
	tinkerbellRuntimeDigestsEnvVar             = "EXPECTED_TINKERBELL_RUNTIME_DIGESTS"
	rufioHardOffRetryTestRegex                 = "^TestTinkerbellKubernetes136UbuntuRufioHardOffRetryRegistryMirror$"
)

type privateECRImage struct {
	registry   string
	registryID string
	region     string
	repository string
	tag        string
}

// TinkerbellTest maps each Tinkbell test with the hardware count needed for the test.
type TinkerbellTest struct {
	Name  string `yaml:"name"`
	Count int    `yaml:"count"`
}

func (e *E2ESession) setupTinkerbellEnv(testRegex string) error {
	re := regexp.MustCompile(tinkerbellTestsRe)
	if !re.MatchString(testRegex) {
		return nil
	}

	requiredEnvVars := e2etests.RequiredTinkerbellEnvVars()
	for _, eVar := range requiredEnvVars {
		if val, ok := os.LookupEnv(eVar); ok {
			e.testEnvVars[eVar] = val
		}
	}
	if testRegex == rufioHardOffRetryTestRegex {
		mirrorImage, bundleImage, runtimeDigests, err := prepareRufioCandidateImage()
		if err != nil {
			return fmt.Errorf("preparing Rufio candidate image: %w", err)
		}
		e.testEnvVars[tinkerbellMirrorImageEnvVar] = mirrorImage
		e.testEnvVars[tinkerbellBundleImageEnvVar] = bundleImage
		e.testEnvVars[tinkerbellRuntimeDigestsEnvVar] = strings.Join(runtimeDigests, ",")
	}

	inventoryFileName := fmt.Sprintf("%s.csv", getTestRunnerName(e.logger, e.jobId))
	inventoryFilePath := fmt.Sprintf("bin/%s", inventoryFileName)

	if _, err := os.Stat(inventoryFilePath); err == nil {
		err = os.Remove(inventoryFilePath)
		if err != nil {
			e.logger.V(1).Info("WARN: Failed to clean up existing inventory csv", "file", inventoryFilePath)
		}
	}

	err := api.WriteHardwareSliceToCSV(e.hardware, inventoryFilePath)
	if err != nil {
		return fmt.Errorf("failed to setup tinkerbell test environment: %v", err)
	}

	err = e.uploadRequiredFile(inventoryFileName)
	if err != nil {
		return fmt.Errorf("failed to upload tinkerbell inventory file (%s) : %v", inventoryFileName, err)
	}

	err = e.downloadRequiredFileInInstance(inventoryFileName)
	if err != nil {
		return fmt.Errorf("failed to download tinkerbell inventory file (%s) to test instance : %v", inventoryFileName, err)
	}

	tinkInterface := os.Getenv(tinkerbellBootstrapInterfaceEnvVar)
	if tinkInterface == "" {
		return fmt.Errorf("tinkerbell bootstrap interface env var is required: %s", tinkerbellBootstrapInterfaceEnvVar)
	}

	err = e.setTinkerbellBootstrapIPInInstance(tinkInterface)
	if err != nil {
		return fmt.Errorf("failed to set tinkerbell boostrap ip on interface (%s) in test instance : %v", tinkInterface, err)
	}

	e.testEnvVars[tinkerbellInventoryCsvFilePathEnvVar] = inventoryFilePath
	e.testEnvVars[tinkerbellCIEnvironmentEnvVar] = "true"

	return nil
}

func prepareRufioCandidateImage() (mirrorImage, bundleImage string, runtimeDigests []string, err error) {
	sourceImage := strings.TrimSpace(os.Getenv(tinkerbellExpectedImageEnvVar))
	expectedDigest := strings.TrimSpace(os.Getenv(tinkerbellExpectedImageDigestEnvVar))
	if sourceImage == "" || expectedDigest == "" {
		return "", "", nil, fmt.Errorf(
			"%s and %s must be set",
			tinkerbellExpectedImageEnvVar,
			tinkerbellExpectedImageDigestEnvVar,
		)
	}
	if !strings.HasPrefix(expectedDigest, "sha256:") {
		return "", "", nil, fmt.Errorf("candidate digest %q is not a sha256 digest", expectedDigest)
	}

	source, err := parsePrivateECRImage(sourceImage)
	if err != nil {
		return "", "", nil, err
	}

	accessKey, secretKey, sessionToken, err := assumeRoleAndGetCredentials(
		"PACKAGES_ROLE_ARN",
		"test-rufio-candidate-image",
	)
	if err != nil {
		return "", "", nil, fmt.Errorf("getting candidate image credentials: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cfg, err := config.LoadDefaultConfig(
		ctx,
		config.WithRegion(source.region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, sessionToken)),
	)
	if err != nil {
		return "", "", nil, fmt.Errorf("loading candidate ECR config: %w", err)
	}
	ecrClient := ecr.NewFromConfig(cfg)
	describeOutput, err := ecrClient.DescribeImages(ctx, &ecr.DescribeImagesInput{
		RepositoryName: aws.String(source.repository),
		ImageIds: []types.ImageIdentifier{
			{ImageTag: aws.String(source.tag)},
		},
	})
	if err != nil {
		return "", "", nil, fmt.Errorf("resolving candidate image digest: %w", err)
	}
	if len(describeOutput.ImageDetails) == 0 || describeOutput.ImageDetails[0].ImageDigest == nil {
		return "", "", nil, fmt.Errorf("candidate ECR image %s has no digest", sourceImage)
	}
	if actualDigest := aws.ToString(describeOutput.ImageDetails[0].ImageDigest); actualDigest != expectedDigest {
		return "", "", nil, fmt.Errorf(
			"candidate ECR image digest is %q, want %q",
			actualDigest,
			expectedDigest,
		)
	}

	authOutput, err := ecrClient.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{
		RegistryIds: []string{source.registryID},
	})
	if err != nil {
		return "", "", nil, fmt.Errorf("getting candidate ECR authorization token: %w", err)
	}
	var authorizationToken string
	for _, authorizationData := range authOutput.AuthorizationData {
		if strings.TrimPrefix(aws.ToString(authorizationData.ProxyEndpoint), "https://") == source.registry {
			authorizationToken = aws.ToString(authorizationData.AuthorizationToken)
			break
		}
	}
	if authorizationToken == "" {
		return "", "", nil, fmt.Errorf("candidate ECR returned no authorization token for %s", source.registry)
	}
	decodedToken, err := base64.StdEncoding.DecodeString(authorizationToken)
	if err != nil {
		return "", "", nil, fmt.Errorf("decoding candidate ECR authorization token: %w", err)
	}
	ecrCredentials := strings.SplitN(string(decodedToken), ":", 2)
	if len(ecrCredentials) != 2 {
		return "", "", nil, fmt.Errorf("candidate ECR returned an invalid authorization token")
	}

	mirrorEndpoint := strings.TrimSpace(os.Getenv(e2etests.RegistryEndpointTinkerbellVar))
	mirrorPort := strings.TrimSpace(os.Getenv(e2etests.RegistryPortTinkerbellVar))
	mirrorUsername := os.Getenv(e2etests.RegistryUsernameTinkerbellVar)
	mirrorPassword := os.Getenv(e2etests.RegistryPasswordTinkerbellVar)
	if mirrorPort == "" {
		mirrorPort = "443"
	}
	if mirrorEndpoint == "" || mirrorUsername == "" || mirrorPassword == "" {
		return "", "", nil, fmt.Errorf("Tinkerbell registry mirror configuration is incomplete")
	}
	mirrorRegistry := net.JoinHostPort(mirrorEndpoint, mirrorPort)
	imagePath := fmt.Sprintf("eks-anywhere/tinkerbell/tinkerbell:%s", source.tag)
	mirrorImage = fmt.Sprintf("%s/%s", mirrorRegistry, imagePath)
	bundleImage = fmt.Sprintf("%s/%s", constants.DefaultCoreEKSARegistry, imagePath)

	docker := executables.BuildDockerExecutable()
	if err := docker.Login(ctx, source.registry, ecrCredentials[0], ecrCredentials[1]); err != nil {
		return "", "", nil, fmt.Errorf("logging in to candidate ECR registry: %w", err)
	}
	if err := docker.Login(ctx, mirrorRegistry, mirrorUsername, mirrorPassword); err != nil {
		return "", "", nil, fmt.Errorf("logging in to Tinkerbell registry mirror: %w", err)
	}

	sourceReference := sourceImage + "@" + expectedDigest
	sourceDigest, sourcePlatformDigests, err := inspectRegistryImage(ctx, docker, sourceReference)
	if err != nil {
		return "", "", nil, fmt.Errorf("inspecting candidate image: %w", err)
	}
	if sourceDigest != expectedDigest {
		return "", "", nil, fmt.Errorf("candidate registry returned digest %q, want %q", sourceDigest, expectedDigest)
	}
	if err := docker.PullImage(ctx, sourceReference); err != nil {
		return "", "", nil, fmt.Errorf("pulling candidate image: %w", err)
	}
	if _, err := docker.Execute(ctx, "tag", sourceReference, mirrorImage); err != nil {
		return "", "", nil, fmt.Errorf("tagging candidate image for mirror: %w", err)
	}
	if _, err := docker.Execute(ctx, "push", mirrorImage); err != nil {
		return "", "", nil, fmt.Errorf("pushing candidate image to mirror: %w", err)
	}

	mirrorDigest, mirrorPlatformDigests, err := inspectRegistryImage(ctx, docker, mirrorImage)
	if err != nil {
		return "", "", nil, fmt.Errorf("inspecting mirrored candidate image: %w", err)
	}
	if _, ok := sourcePlatformDigests[mirrorDigest]; !ok && mirrorDigest != expectedDigest {
		return "", "", nil, fmt.Errorf(
			"mirrored image digest %q is not part of candidate image %q",
			mirrorDigest,
			expectedDigest,
		)
	}
	if len(mirrorPlatformDigests) == 0 {
		mirrorPlatformDigests[mirrorDigest] = struct{}{}
	}
	runtimeDigests = make([]string, 0, len(mirrorPlatformDigests))
	for digest := range mirrorPlatformDigests {
		runtimeDigests = append(runtimeDigests, digest)
	}
	sort.Strings(runtimeDigests)

	return mirrorImage, bundleImage, runtimeDigests, nil
}

func parsePrivateECRImage(image string) (privateECRImage, error) {
	slashIndex := strings.Index(image, "/")
	tagIndex := strings.LastIndex(image, ":")
	if slashIndex <= 0 || tagIndex <= slashIndex+1 || tagIndex == len(image)-1 {
		return privateECRImage{}, fmt.Errorf("invalid private ECR image URI %q", image)
	}

	registry := image[:slashIndex]
	registryParts := strings.Split(registry, ".")
	if len(registryParts) < 6 || registryParts[1] != "dkr" || registryParts[2] != "ecr" {
		return privateECRImage{}, fmt.Errorf("image registry %q is not a private ECR registry", registry)
	}

	return privateECRImage{
		registry:   registry,
		registryID: registryParts[0],
		region:     registryParts[3],
		repository: image[slashIndex+1 : tagIndex],
		tag:        image[tagIndex+1:],
	}, nil
}

func inspectRegistryImage(
	ctx context.Context,
	docker *executables.Docker,
	image string,
) (string, map[string]struct{}, error) {
	inspectOutput, err := docker.Execute(ctx, "buildx", "imagetools", "inspect", image)
	if err != nil {
		return "", nil, err
	}
	var digest string
	for _, line := range strings.Split(inspectOutput.String(), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Digest:") {
			digest = strings.TrimSpace(strings.TrimPrefix(line, "Digest:"))
			break
		}
	}
	if digest == "" {
		return "", nil, fmt.Errorf("registry image %q did not report a digest", image)
	}

	rawOutput, err := docker.Execute(ctx, "buildx", "imagetools", "inspect", "--raw", image)
	if err != nil {
		return "", nil, err
	}
	var manifest struct {
		Manifests []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(rawOutput.Bytes(), &manifest); err != nil {
		return "", nil, fmt.Errorf("parsing registry manifest for %q: %w", image, err)
	}
	platformDigests := make(map[string]struct{}, len(manifest.Manifests))
	for _, descriptor := range manifest.Manifests {
		if descriptor.Digest != "" {
			platformDigests[descriptor.Digest] = struct{}{}
		}
	}
	return digest, platformDigests, nil
}

func (e *E2ESession) setTinkerbellBootstrapIPInInstance(tinkInterface string) error {
	e.logger.V(1).Info("Setting Tinkerbell Bootstrap IP in instance")

	command := fmt.Sprintf("export T_TINKERBELL_BOOTSTRAP_IP=$(/sbin/ip -o -4 addr list %s | awk '{print $4}' | cut -d/ -f1) && echo T_TINKERBELL_BOOTSTRAP_IP=\"$T_TINKERBELL_BOOTSTRAP_IP\" | tee -a /etc/environment", tinkInterface)
	if err := ssm.Run(e.session, logr.Discard(), e.instanceId, command, ssmTimeout); err != nil {
		return fmt.Errorf("setting tinkerbell boostrap ip: %v", err)
	}

	e.logger.V(1).Info("Successfully set tinkerbell boostrap ip")

	return nil
}

// Get non airgapped, normal tinkerbell tests.
func getTinkerbellNonAirgappedTests(tests []string) []string {
	tinkerbellTestsRe := regexp.MustCompile(tinkerbellTestsRe)
	airgappedRe := regexp.MustCompile(`^.*Airgapped.*$`)
	var tinkerbellTests []string

	for _, testName := range tests {
		if tinkerbellTestsRe.MatchString(testName) && !airgappedRe.MatchString(testName) {
			tinkerbellTests = append(tinkerbellTests, testName)
		}
	}
	return tinkerbellTests
}

func getTinkerbellAirgappedTests(tests []string) []string {
	tinkerbellTestsRe := regexp.MustCompile(tinkerbellTestsRe)
	airgappedRe := regexp.MustCompile(`^.*Airgapped.*$`)
	var tinkerbellTests []string

	for _, testName := range tests {
		if tinkerbellTestsRe.MatchString(testName) && airgappedRe.MatchString(testName) {
			tinkerbellTests = append(tinkerbellTests, testName)
		}
	}
	return tinkerbellTests
}
