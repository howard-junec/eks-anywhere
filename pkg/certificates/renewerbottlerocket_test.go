package certificates_test

// import (
// 	"context"
// 	"os"
// 	"path/filepath"
// 	"testing"

// 	"github.com/golang/mock/gomock"

// 	"github.com/aws/eks-anywhere/pkg/certificates"
// 	"github.com/aws/eks-anywhere/pkg/certificates/mocks"
// )

// func prepareLocalEtcdFiles(t *testing.T, dir string) {
// 	t.Helper()
// 	local := filepath.Join(dir, certificates.TempLocalEtcdCertsDir)
// 	if err := os.MkdirAll(local, 0o700); err != nil {
// 		t.Fatalf("failed to create directory: %v", err)
// 	}
// 	if err := os.WriteFile(filepath.Join(local, "apiserver-etcd-client.crt"), []byte("crt"), 0o600); err != nil {
// 		t.Fatalf("failed to write certificate file: %v", err)
// 	}
// 	if err := os.WriteFile(filepath.Join(local, "apiserver-etcd-client.key"), []byte("key"), 0o600); err != nil {
// 		t.Fatalf("failed to write key file: %v", err)
// 	}
// }

// func TestBR_TransferCerts_Success(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	prepareLocalEtcdFiles(t, tmp)

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(tmp)

// 	ctx, node := context.Background(), "cp"

// 	ssh.EXPECT().
// 		RunCommand(ctx, node, gomock.Any(), gomock.Any()).
// 		Return("", nil)

// 	if err := r.TransferCertsToControlPlaneOS(ctx, node, ssh); err != nil {
// 		t.Fatalf("TransferCertsToControlPlaneOS() expected no error, got: %v", err)
// 	}
// }

// func TestBR_TransferCerts_ReadCertError(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	local := filepath.Join(tmp, certificates.TempLocalEtcdCertsDir)
// 	if err := os.MkdirAll(local, 0o700); err != nil {
// 		t.Fatalf("failed to create directory: %v", err)
// 	}

// 	if err := os.WriteFile(filepath.Join(local, "apiserver-etcd-client.key"), []byte("key"), 0o600); err != nil {
// 		t.Fatalf("failed to write key file: %v", err)
// 	}

// 	r := certificates.NewBottlerocketRenewer(tmp)
// 	if err := r.TransferCertsToControlPlaneOS(context.Background(), "cp", mocks.NewMockSSHRunner(ctrl)); err == nil {
// 		t.Fatalf("TransferCertsToControlPlaneOS() expected error, got nil")
// 	}
// }

// func TestBR_TransferCerts_ReadKeyError(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	local := filepath.Join(tmp, certificates.TempLocalEtcdCertsDir)
// 	if err := os.MkdirAll(local, 0o700); err != nil {
// 		t.Fatalf("failed to create directory: %v", err)
// 	}
// 	if err := os.WriteFile(filepath.Join(local, "apiserver-etcd-client.crt"), []byte("crt"), 0o600); err != nil {
// 		t.Fatalf("failed to write certificate file: %v", err)
// 	}

// 	r := certificates.NewBottlerocketRenewer(tmp)
// 	if err := r.TransferCertsToControlPlaneOS(context.Background(), "cp", mocks.NewMockSSHRunner(ctrl)); err == nil {
// 		t.Fatalf("TransferCertsToControlPlaneOS() expected error, got nil")
// 	}
// }

// func TestBR_TransferCerts_SSHError(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	prepareLocalEtcdFiles(t, tmp)

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(tmp)

// 	ssh.EXPECT().
// 		RunCommand(context.Background(), "cp", gomock.Any(), gomock.Any()).
// 		Return("", errBoom)

// 	if err := r.TransferCertsToControlPlaneOS(context.Background(), "cp", ssh); err == nil {
// 		t.Fatalf("TransferCertsToControlPlaneOS() expected error, got nil")
// 	}
// }

// func TestBR_CopyEtcdCerts_CopyTmpFail(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(t.TempDir())

// 	ctx, node := context.Background(), "etcd"

// 	ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", errBoom)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestBR_CopyEtcdCerts_ReadCertFail(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(t.TempDir())

// 	ctx, node := context.Background(), "etcd"

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt"), gomock.Any()).Return("", errBoom),
// 	)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestBR_CopyEtcdCerts_CertEmpty(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(t.TempDir())

// 	ctx, node := context.Background(), "etcd"

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt"), gomock.Any()).Return("", nil),
// 	)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestBR_CopyEtcdCerts_KeyReadFail(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(t.TempDir())

// 	ctx, node := context.Background(), "etcd"

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt"), gomock.Any()).Return("crt", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key"), gomock.Any()).Return("", errBoom),
// 	)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestBR_CopyEtcdCerts_KeyEmpty(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(t.TempDir())

// 	ctx, node := context.Background(), "etcd"

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt"), gomock.Any()).Return("crt", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key"), gomock.Any()).Return("", nil),
// 	)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestBR_CopyEtcdCerts_LocalDirCreateFail(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()

// 	bad := filepath.Join(tmp, certificates.TempLocalEtcdCertsDir)
// 	if err := os.WriteFile(bad, []byte("x"), 0o600); err != nil {
// 		t.Fatalf("failed to write file: %v", err)
// 	}

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(tmp)

// 	ctx, node := context.Background(), "etcd"

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt"), gomock.Any()).Return("crt", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key"), gomock.Any()).Return("key", nil),
// 	)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestBR_RenewEtcdCerts_BackupError(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(t.TempDir())

// 	ctx, node := context.Background(), "etcd"

// 	ssh.EXPECT().RunCommand(ctx, node, containsMatcher("cp -r")).Return("", errBoom)

// 	if err := r.RenewEtcdCertsOnOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("RenewEtcdCertsOnOS() expected error, got nil")
// 	}
// }

// func TestBR_RenewEtcdCerts_RenewError(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(t.TempDir())

// 	ctx, node := context.Background(), "etcd"

// 	ssh.EXPECT().
// 		RunCommand(ctx, node, containsMatcher("etcdadm join")).
// 		Return("", errBoom)

// 	if err := r.RenewEtcdCertsOnOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("RenewEtcdCertsOnOS() expected error, got nil")
// 	}
// }

// func TestBR_RenewEtcdCerts_ValidateError(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(t.TempDir())

// 	ctx, node := context.Background(), "etcd"

// 	first := ssh.EXPECT().
// 		RunCommand(ctx, node, containsMatcher("etcdadm join")).
// 		Return("", nil)

// 	ssh.EXPECT().
// 		RunCommand(ctx, node, containsMatcher("etcdctl")).
// 		After(first).
// 		Return("", errBoom)

// 	if err := r.RenewEtcdCertsOnOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("RenewEtcdCertsOnOS() expected error, got nil")
// 	}
// }

// func TestBR_RenewCP_NoEtcd_ShellCommandError(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(t.TempDir())

// 	ctx, node := context.Background(), "cp"

// 	cfg := &certificates.RenewalConfig{}

// 	ssh.EXPECT().
// 		RunCommand(ctx, node, gomock.Any()).
// 		Return("", errBoom)

// 	if err := r.RenewControlPlaneCertsOnOS(ctx, node, cfg, "", ssh); err == nil {
// 		t.Fatalf("RenewControlPlaneCertsOnOS() expected error, got nil")
// 	}
// }

// func TestBR_RenewCP_WithEtcd_TransferFails(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(t.TempDir())

// 	ctx, node := context.Background(), "cp"
// 	cfg := &certificates.RenewalConfig{Etcd: certificates.NodeConfig{Nodes: []string{"etcd"}}}

// 	if err := r.RenewControlPlaneCertsOnOS(ctx, node, cfg, "", ssh); err == nil {
// 		t.Fatalf("RenewControlPlaneCertsOnOS() expected error, got nil")
// 	}
// }

// func TestBR_CopyEtcdCerts_WriteCertFail(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()
// 	tmp := t.TempDir()
// 	localDir := filepath.Join(tmp, certificates.TempLocalEtcdCertsDir)
// 	if err := os.MkdirAll(localDir, 0o500); err != nil {
// 		t.Fatalf("prep: %v", err)
// 	}

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(tmp)

// 	ctx, node := context.Background(), "etcd"

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt"), gomock.Any()).Return("crt", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key"), gomock.Any()).Return("key", nil),
// 	)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestBR_CopyEtcdCerts_CleanupFail(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(tmp)

// 	ctx, node := context.Background(), "etcd"

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt"), gomock.Any()).Return("crt", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key"), gomock.Any()).Return("key", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("rm -f")).Return("", errBoom),
// 	)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestBR_RenewEtcdCerts_Success(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(t.TempDir())

// 	ctx, node := context.Background(), "etcd"

// 	first := ssh.EXPECT().
// 		RunCommand(ctx, node, containsMatcher("etcdadm join")).
// 		Return("", nil)
// 	ssh.EXPECT().
// 		RunCommand(ctx, node, containsMatcher("etcdctl")).
// 		After(first).
// 		Return("", nil)

// 	if err := r.RenewEtcdCertsOnOS(ctx, node, ssh); err != nil {
// 		t.Fatalf("RenewEtcdCertsOnOS() expected no error, got: %v", err)
// 	}
// }

// func TestBR_RenewCP_NoEtcd_Success(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(t.TempDir())

// 	ctx, node := context.Background(), "cp"
// 	cfg := &certificates.RenewalConfig{}

// 	ssh.EXPECT().
// 		RunCommand(ctx, node, gomock.Any()).
// 		Return("", nil)

// 	if err := r.RenewControlPlaneCertsOnOS(ctx, node, cfg, "", ssh); err != nil {
// 		t.Fatalf("RenewControlPlaneCertsOnOS() expected no error, got: %v", err)
// 	}
// }

// func TestBR_RenewCP_WithEtcd_Success(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	prepareLocalEtcdFiles(t, tmp)

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(tmp)

// 	ctx, node := context.Background(), "cp"
// 	cfg := &certificates.RenewalConfig{Etcd: certificates.NodeConfig{Nodes: []string{"n"}}}

// 	transfer := ssh.EXPECT().
// 		RunCommand(ctx, node, containsMatcher("TARGET_DIR"), gomock.Any()).
// 		Return("", nil)
// 	ssh.EXPECT().
// 		RunCommand(ctx, node, containsMatcher("kubeadm certs renew")).
// 		After(transfer).
// 		Return("", nil)

// 	if err := r.RenewControlPlaneCertsOnOS(ctx, node, cfg, "", ssh); err != nil {
// 		t.Fatalf("RenewControlPlaneCertsOnOS() expected no error, got: %v", err)
// 	}
// }

// func TestBR_CopyEtcdCerts_WriteKeyFail(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(tmp)

// 	localDir := filepath.Join(tmp, certificates.TempLocalEtcdCertsDir)
// 	if err := os.MkdirAll(filepath.Join(localDir, "apiserver-etcd-client.key"), 0o700); err != nil {
// 		t.Fatalf("prep: %v", err)
// 	}

// 	ctx, node := context.Background(), "etcd"

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil), // copy /tmp
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt"), gomock.Any()).Return("crt", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key"), gomock.Any()).Return("key", nil),
// 	)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestBR_CopyEtcdCerts_Success(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewBottlerocketRenewer(tmp)

// 	ctx, node := context.Background(), "etcd"

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node, gomock.Any()).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt"), gomock.Any()).Return("crt", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key"), gomock.Any()).Return("key", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("rm -f")).Return("", nil),
// 	)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err != nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected no error, got: %v", err)
// 	}

// 	for _, f := range []string{"apiserver-etcd-client.crt", "apiserver-etcd-client.key"} {
// 		if _, err := os.Stat(filepath.Join(tmp, certificates.TempLocalEtcdCertsDir, f)); err != nil {
// 			t.Fatalf("expect local %s: %v", f, err)
// 		}
// 	}
// }
