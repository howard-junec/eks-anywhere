package certificates

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/eks-anywhere/pkg/certificates/mocks"
	"github.com/golang/mock/gomock"
)

var errBoom = fmt.Errorf("boom")

type containsMatcher string

func (c containsMatcher) Matches(x interface{}) bool {
	s, ok := x.(string)
	return ok && strings.Contains(s, string(c))
}
func (c containsMatcher) String() string { return "string contains " + string(c) }

func TestLinuxRenewer_CopyEtcdCerts_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(tmp)

	ctx, node := context.Background(), "10.0.0.10"

	ssh.EXPECT().
		RunCommand(ctx, node, "sudo cat /etc/etcd/pki/apiserver-etcd-client.crt").
		Return("cert", nil)
	ssh.EXPECT().
		RunCommand(ctx, node, "sudo cat /etc/etcd/pki/apiserver-etcd-client.key").
		Return("key", nil)

	if err := r.CopyEtcdCerts(ctx, node, ssh); err != nil {
		t.Fatalf("CopyEtcdCerts() expected no error, got: %v", err)
	}

	for _, f := range []string{"crt", "key"} {
		if _, err := os.Stat(filepath.Join(tmp, tempLocalEtcdCertsDir, "apiserver-etcd-client."+f)); err != nil {
			t.Fatalf("expected local %s: %v", f, err)
		}
	}
}

func TestLinuxRenewer_CopyEtcdCerts_ReadCertError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(t.TempDir())

	ctx, node := context.Background(), "n1"

	ssh.EXPECT().
		RunCommand(ctx, node, containsMatcher("apiserver-etcd-client.crt")).
		Return("", errBoom)

	if err := r.CopyEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
}

func TestLinuxRenewer_CopyEtcdCerts_ReadKeyError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(t.TempDir())

	ctx, node := context.Background(), "n2"

	ssh.EXPECT().
		RunCommand(ctx, node, containsMatcher(".crt")).
		Return("cert-bytes", nil)
	ssh.EXPECT().
		RunCommand(ctx, node, containsMatcher(".key")).
		Return("", errBoom)

	if err := r.CopyEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
}

func TestLinuxRenewer_CopyEtcdCerts_KeyEmpty(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(t.TempDir())

	ctx, node := context.Background(), "n3"

	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("cert", nil)
	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key")).Return("", nil)

	if err := r.CopyEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
}

func TestLinuxRenewer_CopyEtcdCerts_DirCreateError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()

	badDir := filepath.Join(tmp, tempLocalEtcdCertsDir)
	if err := os.WriteFile(badDir, []byte("x"), 0600); err != nil {
		t.Fatalf("prep: %v", err)
	}

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(tmp)

	ctx, node := context.Background(), "n4"
	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("cert", nil)
	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key")).Return("key", nil)

	if err := r.CopyEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
}

func TestLinuxRenewer_RenewEtcdCerts_JoinPhaseFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(t.TempDir())

	ctx, node := context.Background(), "etcd-join"

	ssh.EXPECT().RunCommand(ctx, node, containsMatcher("cp -r pki")).Return("", nil)

	ssh.EXPECT().RunCommand(ctx, node, containsMatcher("etcdadm join")).Return("", errBoom)

	if err := r.RenewEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("RenewEtcdCerts() expected error, got nil")
	}
}

func TestLinuxRenewer_RenewEtcdCerts_ValidateFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(t.TempDir())

	ctx, node := context.Background(), "etcd-validate"

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("cp -r pki")).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("etcdadm join")).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("etcdctl")).Return("", errBoom),
	)

	if err := r.RenewEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("RenewEtcdCerts() expected error, got nil")
	}
}

func TestLinuxRenewer_RenewControlPlaneCerts_BackupFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(t.TempDir())

	ctx, node := context.Background(), "cp-backup"
	cfg := &RenewalConfig{}

	ssh.EXPECT().RunCommand(ctx, node,
		r.backupControlPlaneCerts("", false, r.backup)).
		Return("", errBoom)

	if err := r.RenewControlPlaneCerts(ctx, node, cfg, "", ssh); err == nil {
		t.Fatalf("RenewControlPlaneCerts() expected error, got nil")
	}
}

func TestLinuxRenewer_RenewControlPlaneCerts_RenewStepFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(t.TempDir())

	ctx, node := context.Background(), "cp-renew"
	cfg := &RenewalConfig{}

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node,
			r.backupControlPlaneCerts("", false, r.backup)).Return("", nil),

		ssh.EXPECT().RunCommand(ctx, node,
			r.renewControlPlaneCerts("", false)).Return("", errBoom),
	)

	if err := r.RenewControlPlaneCerts(ctx, node, cfg, "", ssh); err == nil {
		t.Fatalf("RenewControlPlaneCerts() expected error, got nil")
	}
}

func TestLinuxRenewer_RenewControlPlaneCerts_ValidateStepFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(t.TempDir())

	ctx, node := context.Background(), "cp-validate"
	cfg := &RenewalConfig{}

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node,
			r.backupControlPlaneCerts("", false, r.backup)).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node,
			r.renewControlPlaneCerts("", false)).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node,
			"sudo kubeadm certs check-expiration").Return("", errBoom),
	)

	if err := r.RenewControlPlaneCerts(ctx, node, cfg, "", ssh); err == nil {
		t.Fatalf("RenewControlPlaneCerts() expected error, got nil")
	}
}

func TestLinuxRenewer_RenewControlPlaneCerts_TransferFilesFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	tmp := t.TempDir()
	r := NewLinuxRenewer(tmp)

	ctx, node := context.Background(), "cp-transfer"
	cfg := &RenewalConfig{Etcd: NodeConfig{Nodes: []string{"x"}}}

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node,
			r.backupControlPlaneCerts("", true, r.backup)).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node,
			r.renewControlPlaneCerts("", true)).Return("", nil),
	)

	if err := r.RenewControlPlaneCerts(ctx, node, cfg, "", ssh); err == nil {
		t.Fatalf("RenewControlPlaneCerts() expected error, got nil")
	}
}

func TestLinuxRenewer_RenewControlPlaneCerts_CopyEtcdCertFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	tmp := t.TempDir()

	localDir := filepath.Join(tmp, tempLocalEtcdCertsDir)
	if err := os.MkdirAll(localDir, 0700); err != nil {
		t.Fatalf("prep: %v", err)
	}
	os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.crt"), []byte("crt"), 0600)
	os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.key"), []byte("key"), 0600)

	r := NewLinuxRenewer(tmp)

	ctx, node := context.Background(), "cp-copy-fail"
	cfg := &RenewalConfig{Etcd: NodeConfig{Nodes: []string{"x"}}}

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node,
			r.backupControlPlaneCerts("", true, r.backup)).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node,
			r.renewControlPlaneCerts("", true)).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("tee /tmp/apiserver-etcd-client.crt")).
			Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("tee /tmp/apiserver-etcd-client.key")).
			Return("", nil),

		ssh.EXPECT().RunCommand(ctx, node,
			r.copyExternalEtcdCerts(true)).Return("", errBoom),
	)

	if err := r.RenewControlPlaneCerts(ctx, node, cfg, "", ssh); err == nil {
		t.Fatalf("RenewControlPlaneCerts() expected error, got nil")
	}
}

func TestLinuxRenewer_TransferCerts_ReadCertError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	localDir := filepath.Join(tmp, tempLocalEtcdCertsDir)
	os.MkdirAll(localDir, 0700)
	os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.key"), []byte("key"), 0600)

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(tmp)

	if err := r.TransferCertsToControlPlane(context.Background(), "n", ssh); err == nil {
		t.Fatalf("TransferCertsToControlPlane() expected error, got nil")
	}
}

func TestLinuxRenewer_TransferCerts_ReadKeyError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	localDir := filepath.Join(tmp, tempLocalEtcdCertsDir)
	os.MkdirAll(localDir, 0700)
	os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.crt"), []byte("crt"), 0600)

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(tmp)

	if err := r.TransferCertsToControlPlane(context.Background(), "n", ssh); err == nil {
		t.Fatalf("TransferCertsToControlPlane() expected error, got nil")
	}
}

func TestLinuxRenewer_TransferCerts_CopyCertCmdFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	localDir := filepath.Join(tmp, tempLocalEtcdCertsDir)
	os.MkdirAll(localDir, 0700)
	os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.crt"), []byte("crt"), 0600)
	os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.key"), []byte("key"), 0600)

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(tmp)

	ctx, node := context.Background(), "cp"

	ssh.EXPECT().
		RunCommand(ctx, node, containsMatcher("tee /tmp/apiserver-etcd-client.crt")).
		Return("", errBoom)

	if err := r.TransferCertsToControlPlane(ctx, node, ssh); err == nil {
		t.Fatalf("TransferCertsToControlPlane() expected error, got nil")
	}
}

func TestLinuxRenewer_TransferCerts_CopyKeyCmdFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	localDir := filepath.Join(tmp, tempLocalEtcdCertsDir)
	os.MkdirAll(localDir, 0700)
	os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.crt"), []byte("crt"), 0600)
	os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.key"), []byte("key"), 0600)

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(tmp)

	ctx, node := context.Background(), "cp"

	gomock.InOrder(
		ssh.EXPECT().
			RunCommand(ctx, node, containsMatcher("tee /tmp/apiserver-etcd-client.crt")).
			Return("", nil),
		ssh.EXPECT().
			RunCommand(ctx, node, containsMatcher("tee /tmp/apiserver-etcd-client.key")).
			Return("", errBoom),
	)

	if err := r.TransferCertsToControlPlane(ctx, node, ssh); err == nil {
		t.Fatalf("TransferCertsToControlPlane() expected error, got nil")
	}
}

func TestLinuxRenewer_RenewControlPlaneCerts_RestartPodsFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(t.TempDir())

	ctx, node := context.Background(), "cp-restart"
	cfg := &RenewalConfig{}

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node,
			r.backupControlPlaneCerts("", false, r.backup)).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node,
			r.renewControlPlaneCerts("", false)).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node,
			"sudo kubeadm certs check-expiration").Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node,
			r.restartControlPlaneStaticPods()).Return("", errBoom),
	)

	if err := r.RenewControlPlaneCerts(ctx, node, cfg, "", ssh); err == nil {
		t.Fatalf("RenewControlPlaneCerts() expected error, got nil")
	}
}

func TestLinuxRenewer_RenewEtcdCerts_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(t.TempDir())

	ctx, node := context.Background(), "etcd-success"

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("cp -r pki")).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("etcdadm join")).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("etcdctl")).Return("", nil),
	)

	if err := r.RenewEtcdCerts(ctx, node, ssh); err != nil {
		t.Fatalf("RenewEtcdCerts() expected no error, got: %v", err)
	}
}

func TestLinuxRenewer_RenewControlPlaneCerts_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(t.TempDir())

	ctx, node := context.Background(), "cp-success"
	cfg := &RenewalConfig{}

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node,
			r.backupControlPlaneCerts("", false, r.backup)).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node,
			r.renewControlPlaneCerts("", false)).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node,
			"sudo kubeadm certs check-expiration").Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node,
			r.restartControlPlaneStaticPods()).Return("", nil),
	)

	if err := r.RenewControlPlaneCerts(ctx, node, cfg, "", ssh); err != nil {
		t.Fatalf("RenewControlPlaneCerts() expected no error, got: %v", err)
	}
}

func TestLinuxRenewer_TransferCertsToControlPlane_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	localDir := filepath.Join(tmp, tempLocalEtcdCertsDir)
	os.MkdirAll(localDir, 0700)
	os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.crt"), []byte("crt"), 0600)
	os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.key"), []byte("key"), 0600)

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(tmp)

	ctx, node := context.Background(), "cp-success"

	gomock.InOrder(
		ssh.EXPECT().
			RunCommand(ctx, node, containsMatcher("tee /tmp/apiserver-etcd-client.crt")).
			Return("", nil),
		ssh.EXPECT().
			RunCommand(ctx, node, containsMatcher("tee /tmp/apiserver-etcd-client.key")).
			Return("", nil),
	)

	if err := r.TransferCertsToControlPlane(ctx, node, ssh); err != nil {
		t.Fatalf("TransferCertsToControlPlane() expected no error, got: %v", err)
	}
}

func TestLinuxRenewer_CopyEtcdCerts_CertEmpty(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(t.TempDir())

	ctx, node := context.Background(), "cert-empty"

	ssh.EXPECT().
		RunCommand(ctx, node, containsMatcher("apiserver-etcd-client.crt")).
		Return("", nil)

	err := r.CopyEtcdCerts(ctx, node, ssh)
	if err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "etcd certificate file is empty") {
		t.Fatalf("expected error containing 'etcd certificate file is empty', got: %v", err)
	}
}

func TestLinuxRenewer_CopyEtcdCerts_WriteCertFileError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	readOnlyDir := filepath.Join(tmp, tempLocalEtcdCertsDir)
	if err := os.MkdirAll(readOnlyDir, 0o444); err != nil {
		t.Fatalf("setup: %v", err)
	}

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(tmp)

	ctx, node := context.Background(), "write-cert-fail"

	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("cert-content", nil)
	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key")).Return("key-content", nil)

	err := r.CopyEtcdCerts(ctx, node, ssh)
	if err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "writing etcd certificate file") {
		t.Fatalf("expected error containing 'writing etcd certificate file', got: %v", err)
	}
}

func TestLinuxRenewer_CopyEtcdCerts_WriteKeyFileError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	localDir := filepath.Join(tmp, tempLocalEtcdCertsDir)
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	certPath := filepath.Join(localDir, "apiserver-etcd-client.crt")
	if err := os.WriteFile(certPath, []byte("cert"), 0o600); err != nil {
		t.Fatalf("setup cert: %v", err)
	}

	keyPath := filepath.Join(localDir, "apiserver-etcd-client.key")
	if err := os.Mkdir(keyPath, 0o755); err != nil {
		t.Fatalf("setup key conflict: %v", err)
	}

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(tmp)

	ctx, node := context.Background(), "write-key-fail"

	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("cert-content", nil)
	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key")).Return("key-content", nil)

	err := r.CopyEtcdCerts(ctx, node, ssh)
	if err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
	if !strings.Contains(err.Error(), "writing etcd key file") {
		t.Fatalf("expected error containing 'writing etcd key file', got: %v", err)
	}
}

func TestLinuxRenewer_RenewControlPlaneCerts_NoExternalEtcd(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewLinuxRenewer(t.TempDir())

	ctx, node := context.Background(), "no-external-etcd"
	cfg := &RenewalConfig{}

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node,
			r.backupControlPlaneCerts("", false, r.backup)).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node,
			r.renewControlPlaneCerts("", false)).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node,
			"sudo kubeadm certs check-expiration").Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node,
			r.restartControlPlaneStaticPods()).Return("", nil),
	)

	if err := r.RenewControlPlaneCerts(ctx, node, cfg, "", ssh); err != nil {
		t.Fatalf("RenewControlPlaneCerts() expected no error, got: %v", err)
	}
}

func TestLinuxRenewer_CopyExternalEtcdCerts_ReturnTrue(t *testing.T) {
	r := NewLinuxRenewer(t.TempDir())

	result := r.copyExternalEtcdCerts(false)
	if result != "true" {
		t.Fatalf("copyExternalEtcdCerts(false) expected 'true', got: %s", result)
	}
}
