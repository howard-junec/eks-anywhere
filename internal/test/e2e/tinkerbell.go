package e2e

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/go-logr/logr"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"

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
	rufioHardOffRetryTestName                  = "TestTinkerbellKubernetes136UbuntuRufioHardOffRetryRegistryMirror"
)

type privateECRImage struct {
	registry   string
	registryID string
	region     string
	repository string
	tag        string
}

type registryMirrorConfig struct {
	registry string
	username string
	password string
	caCert   []byte
}

type mirroredRufioCandidateImage struct {
	mirrorImage    string
	bundleImage    string
	runtimeDigests []string
	repository     string
	reference      string
	digest         string
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
	if testRegex == rufioHardOffRetryTestName {
		candidateImage, err := prepareRufioCandidateImage(e.jobId)
		if err != nil {
			return fmt.Errorf("preparing Rufio candidate image: %w", err)
		}
		e.rufioCandidateImage = candidateImage
		e.testEnvVars[tinkerbellMirrorImageEnvVar] = candidateImage.mirrorImage
		e.testEnvVars[tinkerbellBundleImageEnvVar] = candidateImage.bundleImage
		e.testEnvVars[tinkerbellRuntimeDigestsEnvVar] = strings.Join(candidateImage.runtimeDigests, ",")
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

func prepareRufioCandidateImage(jobID string) (candidateImage *mirroredRufioCandidateImage, err error) {
	sourceImage := strings.TrimSpace(os.Getenv(tinkerbellExpectedImageEnvVar))
	expectedDigest := strings.TrimSpace(os.Getenv(tinkerbellExpectedImageDigestEnvVar))
	if strings.TrimSpace(jobID) == "" {
		return nil, fmt.Errorf("E2E job ID must be set for the Rufio candidate image")
	}
	if sourceImage == "" || expectedDigest == "" {
		return nil, fmt.Errorf(
			"%s and %s must be set",
			tinkerbellExpectedImageEnvVar,
			tinkerbellExpectedImageDigestEnvVar,
		)
	}
	if !strings.HasPrefix(expectedDigest, "sha256:") {
		return nil, fmt.Errorf("candidate digest %q is not a sha256 digest", expectedDigest)
	}

	source, err := parsePrivateECRImage(sourceImage)
	if err != nil {
		return nil, err
	}

	accessKey, secretKey, sessionToken, err := assumeRoleAndGetCredentials(
		"PACKAGES_ROLE_ARN",
		"test-rufio-candidate-image",
	)
	if err != nil {
		return nil, fmt.Errorf("getting candidate image credentials: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cfg, err := config.LoadDefaultConfig(
		ctx,
		config.WithRegion(source.region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, sessionToken)),
	)
	if err != nil {
		return nil, fmt.Errorf("loading candidate ECR config: %w", err)
	}
	ecrClient := ecr.NewFromConfig(cfg)
	describeOutput, err := ecrClient.DescribeImages(ctx, &ecr.DescribeImagesInput{
		RepositoryName: aws.String(source.repository),
		ImageIds: []types.ImageIdentifier{
			{ImageTag: aws.String(source.tag)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("resolving candidate image digest: %w", err)
	}
	if len(describeOutput.ImageDetails) == 0 || describeOutput.ImageDetails[0].ImageDigest == nil {
		return nil, fmt.Errorf("candidate ECR image %s has no digest", sourceImage)
	}
	if actualDigest := aws.ToString(describeOutput.ImageDetails[0].ImageDigest); actualDigest != expectedDigest {
		return nil, fmt.Errorf(
			"candidate ECR image digest is %q, want %q",
			actualDigest,
			expectedDigest,
		)
	}

	authOutput, err := ecrClient.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{
		RegistryIds: []string{source.registryID},
	})
	if err != nil {
		return nil, fmt.Errorf("getting candidate ECR authorization token: %w", err)
	}
	var authorizationToken string
	for _, authorizationData := range authOutput.AuthorizationData {
		if strings.TrimPrefix(aws.ToString(authorizationData.ProxyEndpoint), "https://") == source.registry {
			authorizationToken = aws.ToString(authorizationData.AuthorizationToken)
			break
		}
	}
	if authorizationToken == "" {
		return nil, fmt.Errorf("candidate ECR returned no authorization token for %s", source.registry)
	}
	decodedToken, err := base64.StdEncoding.DecodeString(authorizationToken)
	if err != nil {
		return nil, fmt.Errorf("decoding candidate ECR authorization token: %w", err)
	}
	ecrCredentials := strings.SplitN(string(decodedToken), ":", 2)
	if len(ecrCredentials) != 2 {
		return nil, fmt.Errorf("candidate ECR returned an invalid authorization token")
	}

	mirror, err := loadTinkerbellRegistryMirrorConfig()
	if err != nil {
		return nil, err
	}
	if err := installDockerRegistryCA(mirror.registry, mirror.caCert); err != nil {
		return nil, fmt.Errorf("installing Tinkerbell registry mirror CA: %w", err)
	}
	runHash := sha256.Sum256([]byte(jobID))
	repository := fmt.Sprintf("eks-anywhere/e2e/rufio-%x/tinkerbell", runHash[:6])
	imagePath := fmt.Sprintf("%s:%s", repository, source.tag)
	mirrorImage := fmt.Sprintf("%s/%s", mirror.registry, imagePath)
	bundleImage := fmt.Sprintf("%s/%s", constants.DefaultCoreEKSARegistry, imagePath)

	docker := executables.BuildDockerExecutable()
	if err := docker.Login(ctx, source.registry, ecrCredentials[0], ecrCredentials[1]); err != nil {
		return nil, fmt.Errorf("logging in to candidate ECR registry: %w", err)
	}
	if err := docker.Login(ctx, mirror.registry, mirror.username, mirror.password); err != nil {
		return nil, fmt.Errorf("logging in to Tinkerbell registry mirror: %w", err)
	}

	sourceReference := sourceImage + "@" + expectedDigest
	sourceDigest, _, err := inspectRegistryImage(ctx, docker, sourceReference)
	if err != nil {
		return nil, fmt.Errorf("inspecting candidate image: %w", err)
	}
	if sourceDigest != expectedDigest {
		return nil, fmt.Errorf("candidate registry returned digest %q, want %q", sourceDigest, expectedDigest)
	}
	if err := docker.PullImage(ctx, sourceReference); err != nil {
		return nil, fmt.Errorf("pulling candidate image: %w", err)
	}
	sourceConfigDigest, err := dockerImageConfigDigest(ctx, docker, sourceReference)
	if err != nil {
		return nil, fmt.Errorf("reading candidate image config digest: %w", err)
	}
	if _, err := docker.Execute(ctx, "tag", sourceReference, mirrorImage); err != nil {
		return nil, fmt.Errorf("tagging candidate image for mirror: %w", err)
	}
	pushOutput, err := docker.Execute(ctx, "push", mirrorImage)
	if err != nil {
		return nil, fmt.Errorf("pushing candidate image to mirror: %w", err)
	}
	candidateImage = &mirroredRufioCandidateImage{
		mirrorImage: mirrorImage,
		bundleImage: bundleImage,
		repository:  repository,
		reference:   source.tag,
	}
	rollbackImage := candidateImage
	defer func(image *mirroredRufioCandidateImage) {
		if err == nil {
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if cleanupErr := deleteMirroredRufioCandidateImage(rollbackCtx, mirror, image); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("rolling back mirrored Rufio candidate image: %w", cleanupErr))
		}
	}(rollbackImage)
	pushedDigest, err := dockerPushDigest(pushOutput.String())
	if err != nil {
		return nil, fmt.Errorf("reading pushed candidate image digest: %w", err)
	}
	candidateImage.digest = pushedDigest

	mirrorDigest, mirrorPlatformDigests, mirrorConfigDigest, err := inspectMirroredRufioCandidateImage(
		ctx,
		mirror,
		candidateImage,
	)
	if err != nil {
		return nil, fmt.Errorf("inspecting mirrored candidate image: %w", err)
	}
	if mirrorDigest != pushedDigest {
		return nil, fmt.Errorf("pushed candidate image digest is %q, registry reported %q", pushedDigest, mirrorDigest)
	}
	if mirrorConfigDigest != sourceConfigDigest {
		return nil, fmt.Errorf(
			"mirrored image config digest is %q, want candidate config digest %q",
			mirrorConfigDigest,
			sourceConfigDigest,
		)
	}
	if len(mirrorPlatformDigests) == 0 {
		mirrorPlatformDigests[mirrorDigest] = struct{}{}
	}
	runtimeDigests := make([]string, 0, len(mirrorPlatformDigests))
	for digest := range mirrorPlatformDigests {
		runtimeDigests = append(runtimeDigests, digest)
	}
	sort.Strings(runtimeDigests)

	candidateImage.runtimeDigests = runtimeDigests
	return candidateImage, nil
}

func dockerPushDigest(output string) (string, error) {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		fields := strings.Fields(lines[i])
		for j := 0; j+1 < len(fields); j++ {
			if fields[j] != "digest:" {
				continue
			}
			digest, err := godigest.Parse(fields[j+1])
			if err != nil {
				continue
			}
			return digest.String(), nil
		}
	}
	return "", fmt.Errorf("docker push output did not report a manifest digest")
}

func loadTinkerbellRegistryMirrorConfig() (*registryMirrorConfig, error) {
	endpoint := strings.TrimSpace(os.Getenv(e2etests.RegistryEndpointTinkerbellVar))
	port := strings.TrimSpace(os.Getenv(e2etests.RegistryPortTinkerbellVar))
	username := os.Getenv(e2etests.RegistryUsernameTinkerbellVar)
	password := os.Getenv(e2etests.RegistryPasswordTinkerbellVar)
	encodedCACert := strings.TrimSpace(os.Getenv(e2etests.RegistryCACertTinkerbellVar))
	if port == "" {
		port = "443"
	}
	if endpoint == "" || username == "" || password == "" || encodedCACert == "" {
		return nil, fmt.Errorf("Tinkerbell registry mirror configuration is incomplete")
	}
	if strings.ContainsAny(endpoint, `/\`) || strings.Contains(endpoint, "..") {
		return nil, fmt.Errorf("Tinkerbell registry mirror endpoint %q is invalid", endpoint)
	}
	caCert, err := base64.StdEncoding.DecodeString(encodedCACert)
	if err != nil {
		return nil, fmt.Errorf("decoding Tinkerbell registry mirror CA: %w", err)
	}
	return &registryMirrorConfig{
		registry: net.JoinHostPort(endpoint, port),
		username: username,
		password: password,
		caCert:   caCert,
	}, nil
}

func installDockerRegistryCA(registry string, caCert []byte) error {
	certDirectory := filepath.Join("/etc/docker/certs.d", registry)
	if err := os.MkdirAll(certDirectory, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(certDirectory, "ca.crt"), caCert, 0o644)
}

func (e *E2ESession) cleanupRufioCandidateImage() error {
	if e.rufioCandidateImage == nil {
		return nil
	}
	mirror, err := loadTinkerbellRegistryMirrorConfig()
	if err != nil {
		return fmt.Errorf("loading Tinkerbell registry mirror config for cleanup: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return deleteMirroredRufioCandidateImage(ctx, mirror, e.rufioCandidateImage)
}

func deleteMirroredRufioCandidateImage(
	ctx context.Context,
	mirror *registryMirrorConfig,
	candidateImage *mirroredRufioCandidateImage,
) error {
	if candidateImage == nil {
		return nil
	}
	repository, err := newMirroredRufioRepository(mirror, candidateImage.repository)
	if err != nil {
		return err
	}
	reference := candidateImage.digest
	if reference == "" {
		reference = candidateImage.reference
	}
	descriptor, err := repository.Resolve(ctx, reference)
	if err != nil {
		if errors.Is(err, errdef.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("resolving mirrored Rufio candidate image for deletion: %w", err)
	}
	if err := repository.Delete(ctx, descriptor); err != nil && !errors.Is(err, errdef.ErrNotFound) {
		return fmt.Errorf("deleting mirrored Rufio candidate image: %w", err)
	}
	return nil
}

func inspectMirroredRufioCandidateImage(
	ctx context.Context,
	mirror *registryMirrorConfig,
	candidateImage *mirroredRufioCandidateImage,
) (string, map[string]struct{}, string, error) {
	repository, err := newMirroredRufioRepository(mirror, candidateImage.repository)
	if err != nil {
		return "", nil, "", err
	}
	descriptor, err := repository.Resolve(ctx, candidateImage.reference)
	if err != nil {
		return "", nil, "", fmt.Errorf("resolving mirrored Rufio candidate image: %w", err)
	}
	manifestData, err := content.FetchAll(ctx, repository, descriptor)
	if err != nil {
		return "", nil, "", fmt.Errorf("fetching mirrored Rufio candidate manifest: %w", err)
	}
	var manifest struct {
		Config    ocispec.Descriptor   `json:"config"`
		Manifests []ocispec.Descriptor `json:"manifests"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return "", nil, "", fmt.Errorf("parsing mirrored Rufio candidate manifest: %w", err)
	}
	platformDigests := make(map[string]struct{}, len(manifest.Manifests))
	for _, platform := range manifest.Manifests {
		if platform.Digest.String() != "" {
			platformDigests[platform.Digest.String()] = struct{}{}
		}
	}
	configDigest := manifest.Config.Digest.String()
	if configDigest == "" && len(manifest.Manifests) > 0 {
		var platformDescriptor *ocispec.Descriptor
		for i := range manifest.Manifests {
			platform := manifest.Manifests[i].Platform
			if platform != nil && platform.OS == runtime.GOOS && platform.Architecture == runtime.GOARCH {
				platformDescriptor = &manifest.Manifests[i]
				break
			}
		}
		if platformDescriptor == nil {
			return "", nil, "", fmt.Errorf(
				"mirrored candidate image has no manifest for %s/%s",
				runtime.GOOS,
				runtime.GOARCH,
			)
		}
		platformManifestData, err := content.FetchAll(ctx, repository, *platformDescriptor)
		if err != nil {
			return "", nil, "", fmt.Errorf("fetching mirrored Rufio platform manifest: %w", err)
		}
		var platformManifest ocispec.Manifest
		if err := json.Unmarshal(platformManifestData, &platformManifest); err != nil {
			return "", nil, "", fmt.Errorf("parsing mirrored Rufio platform manifest: %w", err)
		}
		configDigest = platformManifest.Config.Digest.String()
	}
	if configDigest == "" {
		return "", nil, "", fmt.Errorf("mirrored candidate image has no config digest")
	}
	if len(platformDigests) == 0 {
		platformDigests[descriptor.Digest.String()] = struct{}{}
	}
	return descriptor.Digest.String(), platformDigests, configDigest, nil
}

func newMirroredRufioRepository(
	mirror *registryMirrorConfig,
	repositoryName string,
) (*remote.Repository, error) {
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(mirror.caCert) {
		return nil, fmt.Errorf("Tinkerbell registry mirror CA contains no certificates")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   2 * time.Minute,
	}

	repository, err := remote.NewRepository(mirror.registry + "/" + repositoryName)
	if err != nil {
		return nil, fmt.Errorf("creating candidate image repository client: %w", err)
	}
	repository.Client = &auth.Client{
		Client: httpClient,
		Cache:  auth.NewCache(),
		Credential: auth.StaticCredential(mirror.registry, auth.Credential{
			Username: mirror.username,
			Password: mirror.password,
		}),
	}
	return repository, nil
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

func dockerImageConfigDigest(
	ctx context.Context,
	docker *executables.Docker,
	image string,
) (string, error) {
	output, err := docker.Execute(ctx, "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil {
		return "", err
	}
	digest, err := godigest.Parse(strings.TrimSpace(output.String()))
	if err != nil {
		return "", fmt.Errorf("parsing Docker image ID: %w", err)
	}
	return digest.String(), nil
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
