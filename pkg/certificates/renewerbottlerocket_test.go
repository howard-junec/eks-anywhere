package certificates

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/golang/mock/gomock"

	"github.com/aws/eks-anywhere/pkg/certificates/mocks"
)

func prepareLocalEtcdFiles(t *testing.T, dir string) {
	t.Helper()
	local := filepath.Join(dir, tempLocalEtcdCertsDir)
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatalf("prep: %v", err)
	}
	os.WriteFile(filepath.Join(local, "apiserver-etcd-client.crt"), []byte("crt"), 0o600)
	os.WriteFile(filepath.Join(local, "apiserver-etcd-client.key"), []byte("key"), 0o600)
}

func TestBR_TransferCerts_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	prepareLocalEtcdFiles(t, tmp)

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(tmp)

	ctx, node := context.Background(), "cp"

	ssh.EXPECT().
		RunCommand(ctx, node, gomock.Any()).
		Return("", nil)

	if err := r.TransferCertsToControlPlane(ctx, node, ssh); err != nil {
		t.Fatalf("TransferCertsToControlPlane() expected no error, got: %v", err)
	}
}

func TestBR_TransferCerts_ReadCertError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	local := filepath.Join(tmp, tempLocalEtcdCertsDir)
	os.MkdirAll(local, 0o700)
	os.WriteFile(filepath.Join(local, "apiserver-etcd-client.key"), []byte("key"), 0o600)

	r := NewBottlerocketRenewer(tmp)
	if err := r.TransferCertsToControlPlane(context.Background(), "cp", mocks.NewMockSSHRunner(ctrl)); err == nil {
		t.Fatalf("TransferCertsToControlPlane() expected error, got nil")
	}
}

func TestBR_TransferCerts_ReadKeyError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	local := filepath.Join(tmp, tempLocalEtcdCertsDir)
	os.MkdirAll(local, 0o700)
	os.WriteFile(filepath.Join(local, "apiserver-etcd-client.crt"), []byte("crt"), 0o600)

	r := NewBottlerocketRenewer(tmp)
	if err := r.TransferCertsToControlPlane(context.Background(), "cp", mocks.NewMockSSHRunner(ctrl)); err == nil {
		t.Fatalf("TransferCertsToControlPlane() expected error, got nil")
	}
}

func TestBR_TransferCerts_SSHError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	prepareLocalEtcdFiles(t, tmp)

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(tmp)

	ssh.EXPECT().
		RunCommand(context.Background(), "cp", gomock.Any()).
		Return("", errBoom)

	if err := r.TransferCertsToControlPlane(context.Background(), "cp", ssh); err == nil {
		t.Fatalf("TransferCertsToControlPlane() expected error, got nil")
	}
}

func TestBR_CopyEtcdCerts_CopyTmpFail(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(t.TempDir())

	ctx, node := context.Background(), "etcd"

	ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", errBoom)

	if err := r.CopyEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
}

func TestBR_CopyEtcdCerts_ReadCertFail(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(t.TempDir())

	ctx, node := context.Background(), "etcd"

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("", errBoom),
	)

	if err := r.CopyEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
}

func TestBR_CopyEtcdCerts_CertEmpty(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(t.TempDir())

	ctx, node := context.Background(), "etcd"

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("", nil),
	)

	if err := r.CopyEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
}

func TestBR_CopyEtcdCerts_KeyReadFail(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(t.TempDir())

	ctx, node := context.Background(), "etcd"

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("crt", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key")).Return("", errBoom),
	)

	if err := r.CopyEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
}

func TestBR_CopyEtcdCerts_KeyEmpty(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(t.TempDir())

	ctx, node := context.Background(), "etcd"

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("crt", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key")).Return("", nil),
	)

	if err := r.CopyEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
}

func TestBR_CopyEtcdCerts_LocalDirCreateFail(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()

	bad := filepath.Join(tmp, tempLocalEtcdCertsDir)
	os.WriteFile(bad, []byte("x"), 0o600)

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(tmp)

	ctx, node := context.Background(), "etcd"

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("crt", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key")).Return("key", nil),
	)

	if err := r.CopyEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
}

func TestBR_RenewEtcdCerts_BackupError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(t.TempDir())

	ctx, node := context.Background(), "etcd"

	ssh.EXPECT().RunCommand(ctx, node, containsMatcher("cp -r")).Return("", errBoom)

	if err := r.RenewEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("RenewEtcdCerts() expected error, got nil")
	}
}

func TestBR_RenewEtcdCerts_RenewError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(t.TempDir())

	ctx, node := context.Background(), "etcd"

	ssh.EXPECT().
		RunCommand(ctx, node, containsMatcher("etcdadm join")).
		Return("", errBoom)

	if err := r.RenewEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("RenewEtcdCerts() expected error, got nil")
	}
}

func TestBR_RenewEtcdCerts_ValidateError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(t.TempDir())

	ctx, node := context.Background(), "etcd"

	first := ssh.EXPECT().
		RunCommand(ctx, node, containsMatcher("etcdadm join")).
		Return("", nil)

	ssh.EXPECT().
		RunCommand(ctx, node, containsMatcher("etcdctl")).
		After(first).
		Return("", errBoom)

	if err := r.RenewEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("RenewEtcdCerts() expected error, got nil")
	}
}

func TestBR_RenewCP_NoEtcd_SheltieFail(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(t.TempDir())

	ctx, node := context.Background(), "cp"

	cfg := &RenewalConfig{}

	ssh.EXPECT().
		RunCommand(ctx, node, gomock.Any()).
		Return("", errBoom)

	if err := r.RenewControlPlaneCerts(ctx, node, cfg, "", ssh); err == nil {
		t.Fatalf("RenewControlPlaneCerts() expected error, got nil")
	}
}

func TestBR_RenewCP_WithEtcd_TransferFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(t.TempDir())

	ctx, node := context.Background(), "cp"
	cfg := &RenewalConfig{Etcd: NodeConfig{Nodes: []string{"etcd"}}}

	if err := r.RenewControlPlaneCerts(ctx, node, cfg, "", ssh); err == nil {
		t.Fatalf("RenewControlPlaneCerts() expected error, got nil")
	}
}

func TestBR_CopyEtcdCerts_WriteCertFail(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	tmp := t.TempDir()
	localDir := filepath.Join(tmp, tempLocalEtcdCertsDir)
	if err := os.MkdirAll(localDir, 0o500); err != nil {
		t.Fatalf("prep: %v", err)
	}

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(tmp)

	ctx, node := context.Background(), "etcd"

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("crt", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key")).Return("key", nil),
	)

	if err := r.CopyEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
}

func TestBR_CopyEtcdCerts_CleanupFail(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(tmp)

	ctx, node := context.Background(), "etcd"

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("crt", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key")).Return("key", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("rm -f")).Return("", errBoom),
	)

	if err := r.CopyEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
}

func TestBR_RenewEtcdCerts_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(t.TempDir())

	ctx, node := context.Background(), "etcd"

	first := ssh.EXPECT().
		RunCommand(ctx, node, containsMatcher("etcdadm join")).
		Return("", nil)
	ssh.EXPECT().
		RunCommand(ctx, node, containsMatcher("etcdctl")).
		After(first).
		Return("", nil)

	if err := r.RenewEtcdCerts(ctx, node, ssh); err != nil {
		t.Fatalf("RenewEtcdCerts() expected no error, got: %v", err)
	}
}

func TestBR_RenewCP_NoEtcd_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(t.TempDir())

	ctx, node := context.Background(), "cp"
	cfg := &RenewalConfig{}

	ssh.EXPECT().
		RunCommand(ctx, node, gomock.Any()).
		Return("", nil)

	if err := r.RenewControlPlaneCerts(ctx, node, cfg, "", ssh); err != nil {
		t.Fatalf("RenewControlPlaneCerts() expected no error, got: %v", err)
	}
}

func TestBR_RenewCP_WithEtcd_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	prepareLocalEtcdFiles(t, tmp)

	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(tmp)

	ctx, node := context.Background(), "cp"
	cfg := &RenewalConfig{Etcd: NodeConfig{Nodes: []string{"n"}}}

	transfer := ssh.EXPECT().
		RunCommand(ctx, node, containsMatcher("TARGET_DIR")).
		Return("", nil)
	ssh.EXPECT().
		RunCommand(ctx, node, containsMatcher("kubeadm certs renew")).
		After(transfer).
		Return("", nil)

	if err := r.RenewControlPlaneCerts(ctx, node, cfg, "", ssh); err != nil {
		t.Fatalf("RenewControlPlaneCerts() expected no error, got: %v", err)
	}
}

func TestBR_CopyEtcdCerts_WriteKeyFail(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(tmp)

	localDir := filepath.Join(tmp, tempLocalEtcdCertsDir)
	if err := os.MkdirAll(filepath.Join(localDir, "apiserver-etcd-client.key"), 0o700); err != nil {
		t.Fatalf("prep: %v", err)
	}

	ctx, node := context.Background(), "etcd"

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil), // copy /tmp
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("crt", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key")).Return("key", nil),
	)

	if err := r.CopyEtcdCerts(ctx, node, ssh); err == nil {
		t.Fatalf("CopyEtcdCerts() expected error, got nil")
	}
}

func TestBR_CopyEtcdCerts_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tmp := t.TempDir()
	ssh := mocks.NewMockSSHRunner(ctrl)
	r := NewBottlerocketRenewer(tmp)

	ctx, node := context.Background(), "etcd"

	gomock.InOrder(
		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("crt", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key")).Return("key", nil),
		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("rm -f")).Return("", nil),
	)

	if err := r.CopyEtcdCerts(ctx, node, ssh); err != nil {
		t.Fatalf("CopyEtcdCerts() expected no error, got: %v", err)
	}

	for _, f := range []string{"apiserver-etcd-client.crt", "apiserver-etcd-client.key"} {
		if _, err := os.Stat(filepath.Join(tmp, tempLocalEtcdCertsDir, f)); err != nil {
			t.Fatalf("expect local %s: %v", f, err)
		}
	}
}
