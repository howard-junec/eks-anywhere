package stack

import (
	"context"
	"testing"

	releasev1alpha1 "github.com/aws/eks-anywhere/release/api/v1alpha1"
)

type recordingDocker struct {
	flags []string
}

func (d *recordingDocker) CheckContainerExistence(context.Context, string) (bool, error) {
	return false, nil
}

func (d *recordingDocker) ForceRemove(context.Context, string) error {
	return nil
}

func (d *recordingDocker) Run(_ context.Context, _, _ string, _ []string, flags ...string) error {
	d.flags = append([]string(nil), flags...)
	return nil
}

func TestInstallSmeeOnDockerUsesRequiredOverrides(t *testing.T) {
	t.Setenv(tinkerbellPublicIPv6OverrideEnvVar, "::")

	docker := &recordingDocker{}
	installer := &Installer{
		docker:       docker,
		namespace:    "eksa-system",
		podCidrRange: "192.168.0.0/16",
		smeeOnDocker: true,
	}
	bundle := releasev1alpha1.TinkerbellStackBundle{
		Boots: releasev1alpha1.Image{
			URI: "public.ecr.aws/eks-anywhere/tinkerbell:latest",
		},
		Hook: releasev1alpha1.HookBundle{
			Initramfs: releasev1alpha1.HookArch{
				Amd: releasev1alpha1.Archive{
					URI: "https://anywhere-assets.eks.amazonaws.com/tinkerbell/hook/initramfs-x86-64",
				},
			},
		},
	}

	if err := installer.installSmeeOnDocker(
		context.Background(),
		bundle,
		"192.0.2.10",
		"/tmp/kubeconfig",
		"",
		"",
	); err != nil {
		t.Fatalf("installing Smee on Docker: %v", err)
	}

	requiredEnvs := map[string]bool{
		"TINKERBELL_HTTP_PORT=7171": false,
		"TINKERBELL_PUBLIC_IPV6=::": false,
	}
	for i := 0; i+1 < len(docker.flags); i++ {
		if docker.flags[i] == "-e" {
			if _, required := requiredEnvs[docker.flags[i+1]]; required {
				requiredEnvs[docker.flags[i+1]] = true
			}
		}
	}

	for env, found := range requiredEnvs {
		if !found {
			t.Fatalf("Docker flags do not contain %q: %v", env, docker.flags)
		}
	}
}
