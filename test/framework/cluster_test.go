package framework

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"go.uber.org/mock/gomock"

	packagesv1 "github.com/aws/eks-anywhere-packages/api/v1alpha1"
	"github.com/aws/eks-anywhere/pkg/executables"
	mockexecutables "github.com/aws/eks-anywhere/pkg/executables/mocks"
)

func TestBootstrapKubeconfigFilePath(t *testing.T) {
	e := &ClusterE2ETest{
		ClusterConfigFolder: "test",
		ClusterName:         "cluster",
	}

	want := filepath.Join("test", "generated", "cluster.kind.kubeconfig")
	if got := e.BootstrapKubeconfigFilePath(); got != want {
		t.Fatalf("BootstrapKubeconfigFilePath() = %q, want %q", got, want)
	}
}

func TestValidatePackageBundleControllerRegistry(t *testing.T) {
	ctrl := gomock.NewController(t)
	executable := mockexecutables.NewMockExecutable(ctrl)
	testPbc := &packagesv1.PackageBundleController{
		Spec: packagesv1.PackageBundleControllerSpec{
			DefaultRegistry:      "123.ecr",
			DefaultImageRegistry: "123.ecr",
		},
	}
	respJSON, err := json.Marshal(testPbc)
	if err != nil {
		t.Errorf("marshaling test service: %s", err)
	}
	ret := bytes.NewBuffer(respJSON)
	expectedParam := []string{"get", "packagebundlecontroller.packages.eks.amazonaws.com", "test", "-o", "json", "--kubeconfig", "test/test-eks-a-cluster.kubeconfig", "--namespace", "eksa-packages", "--ignore-not-found=true"}
	t.Run("CuratedPackagesTest", func(t *testing.T) {
		e := &ClusterE2ETest{
			T:                   t,
			ClusterConfigFolder: "test",
			ClusterName:         "test",
			KubectlClient:       executables.NewKubectl(executable),
		}

		executable.EXPECT().Execute(gomock.Any(), gomock.Eq(expectedParam)).Return(*ret, nil).AnyTimes()
		e.ValidatePackageBundleControllerRegistry()
	})
}
