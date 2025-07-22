package certificates_test

// import (
// 	"context"
// 	"fmt"
// 	"os"
// 	"path/filepath"
// 	"strings"
// 	"testing"

// 	"github.com/golang/mock/gomock"

// 	"github.com/aws/eks-anywhere/pkg/certificates"
// 	"github.com/aws/eks-anywhere/pkg/certificates/mocks"
// )

// var errBoom = fmt.Errorf("boom")

// type containsMatcher string

// func (c containsMatcher) Matches(x interface{}) bool {
// 	s, ok := x.(string)
// 	return ok && strings.Contains(s, string(c))
// }
// func (c containsMatcher) String() string { return "string contains " + string(c) }

// func TestLinuxRenewer_CopyEtcdCerts_Success(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(tmp)

// 	ctx, node := context.Background(), "10.0.0.10"

// 	ssh.EXPECT().
// 		RunCommand(ctx, node, "sudo cat /etc/etcd/pki/apiserver-etcd-client.crt").
// 		Return("cert", nil)
// 	ssh.EXPECT().
// 		RunCommand(ctx, node, "sudo cat /etc/etcd/pki/apiserver-etcd-client.key").
// 		Return("key", nil)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err != nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected no error, got: %v", err)
// 	}

// 	for _, f := range []string{"crt", "key"} {
// 		if _, err := os.Stat(filepath.Join(tmp, certificates.TempLocalEtcdCertsDir, "apiserver-etcd-client."+f)); err != nil {
// 			t.Fatalf("expected local %s: %v", f, err)
// 		}
// 	}
// }

// func TestLinuxRenewer_CopyEtcdCerts_ReadCertError(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(t.TempDir())

// 	ctx, node := context.Background(), "n1"

// 	ssh.EXPECT().
// 		RunCommand(ctx, node, containsMatcher("apiserver-etcd-client.crt")).
// 		Return("", errBoom)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_CopyEtcdCerts_ReadKeyError(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(t.TempDir())

// 	ctx, node := context.Background(), "n2"

// 	ssh.EXPECT().
// 		RunCommand(ctx, node, containsMatcher(".crt")).
// 		Return("cert-bytes", nil)
// 	ssh.EXPECT().
// 		RunCommand(ctx, node, containsMatcher(".key")).
// 		Return("", errBoom)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_CopyEtcdCerts_KeyEmpty(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(t.TempDir())

// 	ctx, node := context.Background(), "n3"

// 	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("cert", nil)
// 	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key")).Return("", nil)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_CopyEtcdCerts_DirCreateError(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()

// 	badDir := filepath.Join(tmp, certificates.TempLocalEtcdCertsDir)
// 	if err := os.WriteFile(badDir, []byte("x"), 0o600); err != nil {
// 		t.Fatalf("prep: %v", err)
// 	}

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(tmp)

// 	ctx, node := context.Background(), "n4"
// 	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("cert", nil)
// 	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key")).Return("key", nil)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_RenewEtcdCerts_JoinPhaseFails(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(t.TempDir())

// 	ctx, node := context.Background(), "etcd-join"

// 	ssh.EXPECT().RunCommand(ctx, node, containsMatcher("cp -r pki")).Return("", nil)

// 	ssh.EXPECT().RunCommand(ctx, node, containsMatcher("etcdadm join")).Return("", errBoom)

// 	if err := r.RenewEtcdCertsOnOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("RenewEtcdCertsOnOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_RenewEtcdCerts_ValidateFails(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(t.TempDir())

// 	ctx, node := context.Background(), "etcd-validate"

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("cp -r pki")).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("etcdadm join")).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("etcdctl")).Return("", errBoom),
// 	)

// 	if err := r.RenewEtcdCertsOnOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("RenewEtcdCertsOnOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_RenewControlPlaneCerts_BackupFails(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(t.TempDir())

// 	ctx, node := context.Background(), "cp-backup"
// 	cfg := &certificates.RenewalConfig{}

// 	ssh.EXPECT().RunCommand(ctx, node,
// 		r.BackupControlPlaneCerts("", false, r.Backup)).
// 		Return("", errBoom)

// 	if err := r.RenewControlPlaneCertsOnOS(ctx, node, cfg, "", ssh); err == nil {
// 		t.Fatalf("RenewControlPlaneCertsOnOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_RenewControlPlaneCerts_RenewStepFails(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(t.TempDir())

// 	ctx, node := context.Background(), "cp-renew"
// 	cfg := &certificates.RenewalConfig{}

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.BackupControlPlaneCerts("", false, r.Backup)).Return("", nil),

// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.RenewControlPlaneCerts("", false)).Return("", errBoom),
// 	)

// 	if err := r.RenewControlPlaneCertsOnOS(ctx, node, cfg, "", ssh); err == nil {
// 		t.Fatalf("RenewControlPlaneCertsOnOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_RenewControlPlaneCerts_ValidateStepFails(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(t.TempDir())

// 	ctx, node := context.Background(), "cp-validate"
// 	cfg := &certificates.RenewalConfig{}

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.BackupControlPlaneCerts("", false, r.Backup)).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.RenewControlPlaneCerts("", false)).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			"sudo kubeadm certs check-expiration").Return("", errBoom),
// 	)

// 	if err := r.RenewControlPlaneCertsOnOS(ctx, node, cfg, "", ssh); err == nil {
// 		t.Fatalf("RenewControlPlaneCertsOnOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_RenewControlPlaneCerts_TransferFilesFails(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	tmp := t.TempDir()
// 	r := certificates.NewLinuxRenewer(tmp)

// 	ctx, node := context.Background(), "cp-transfer"
// 	cfg := &certificates.RenewalConfig{Etcd: certificates.NodeConfig{Nodes: []string{"x"}}}

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.BackupControlPlaneCerts("", true, r.Backup)).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.RenewControlPlaneCerts("", true)).Return("", nil),
// 	)

// 	if err := r.RenewControlPlaneCertsOnOS(ctx, node, cfg, "", ssh); err == nil {
// 		t.Fatalf("RenewControlPlaneCertsOnOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_RenewControlPlaneCerts_CopyEtcdCertFails(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	tmp := t.TempDir()

// 	localDir := filepath.Join(tmp, certificates.TempLocalEtcdCertsDir)
// 	if err := os.MkdirAll(localDir, 0o700); err != nil {
// 		t.Fatalf("prep: %v", err)
// 	}
// 	if err := os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.crt"), []byte("crt"), 0o600); err != nil {
// 		t.Fatalf("failed to write certificate file: %v", err)
// 	}
// 	if err := os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.key"), []byte("key"), 0o600); err != nil {
// 		t.Fatalf("failed to write key file: %v", err)
// 	}

// 	r := certificates.NewLinuxRenewer(tmp)

// 	ctx, node := context.Background(), "cp-copy-fail"
// 	cfg := &certificates.RenewalConfig{Etcd: certificates.NodeConfig{Nodes: []string{"x"}}}

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.BackupControlPlaneCerts("", true, r.Backup)).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.RenewControlPlaneCerts("", true)).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("tee /tmp/apiserver-etcd-client.crt")).
// 			Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("tee /tmp/apiserver-etcd-client.key")).
// 			Return("", nil),

// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.CopyExternalEtcdCerts(true)).Return("", errBoom),
// 	)

// 	if err := r.RenewControlPlaneCertsOnOS(ctx, node, cfg, "", ssh); err == nil {
// 		t.Fatalf("RenewControlPlaneCertsOnOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_TransferCerts_ReadCertError(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	localDir := filepath.Join(tmp, certificates.TempLocalEtcdCertsDir)
// 	if err := os.MkdirAll(localDir, 0o700); err != nil {
// 		t.Fatalf("failed to create directory: %v", err)
// 	}
// 	if err := os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.key"), []byte("key"), 0o600); err != nil {
// 		t.Fatalf("failed to write key file: %v", err)
// 	}

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(tmp)

// 	if err := r.TransferCertsToControlPlaneOS(context.Background(), "n", ssh); err == nil {
// 		t.Fatalf("TransferCertsToControlPlaneOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_TransferCerts_ReadKeyError(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	localDir := filepath.Join(tmp, certificates.TempLocalEtcdCertsDir)
// 	if err := os.MkdirAll(localDir, 0o700); err != nil {
// 		t.Fatalf("failed to create directory: %v", err)
// 	}
// 	if err := os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.crt"), []byte("crt"), 0o600); err != nil {
// 		t.Fatalf("failed to write certificate file: %v", err)
// 	}

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(tmp)

// 	if err := r.TransferCertsToControlPlaneOS(context.Background(), "n", ssh); err == nil {
// 		t.Fatalf("TransferCertsToControlPlaneOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_TransferCerts_CopyCertCmdFails(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	localDir := filepath.Join(tmp, certificates.TempLocalEtcdCertsDir)
// 	if err := os.MkdirAll(localDir, 0o700); err != nil {
// 		t.Fatalf("failed to create directory: %v", err)
// 	}
// 	if err := os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.crt"), []byte("crt"), 0o600); err != nil {
// 		t.Fatalf("failed to write certificate file: %v", err)
// 	}
// 	if err := os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.key"), []byte("key"), 0o600); err != nil {
// 		t.Fatalf("failed to write key file: %v", err)
// 	}

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(tmp)

// 	ctx, node := context.Background(), "cp"

// 	ssh.EXPECT().
// 		RunCommand(ctx, node, containsMatcher("tee /tmp/apiserver-etcd-client.crt")).
// 		Return("", errBoom)

// 	if err := r.TransferCertsToControlPlaneOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("TransferCertsToControlPlaneOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_TransferCerts_CopyKeyCmdFails(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	localDir := filepath.Join(tmp, certificates.TempLocalEtcdCertsDir)
// 	if err := os.MkdirAll(localDir, 0o700); err != nil {
// 		t.Fatalf("failed to create directory: %v", err)
// 	}
// 	if err := os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.crt"), []byte("crt"), 0o600); err != nil {
// 		t.Fatalf("failed to write certificate file: %v", err)
// 	}
// 	if err := os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.key"), []byte("key"), 0o600); err != nil {
// 		t.Fatalf("failed to write key file: %v", err)
// 	}

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(tmp)

// 	ctx, node := context.Background(), "cp"

// 	gomock.InOrder(
// 		ssh.EXPECT().
// 			RunCommand(ctx, node, containsMatcher("tee /tmp/apiserver-etcd-client.crt")).
// 			Return("", nil),
// 		ssh.EXPECT().
// 			RunCommand(ctx, node, containsMatcher("tee /tmp/apiserver-etcd-client.key")).
// 			Return("", errBoom),
// 	)

// 	if err := r.TransferCertsToControlPlaneOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("TransferCertsToControlPlaneOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_RenewControlPlaneCerts_RestartPodsFails(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(t.TempDir())

// 	ctx, node := context.Background(), "cp-restart"
// 	cfg := &certificates.RenewalConfig{}

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.BackupControlPlaneCerts("", false, r.Backup)).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.RenewControlPlaneCerts("", false)).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			"sudo kubeadm certs check-expiration").Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.RestartControlPlaneStaticPods()).Return("", errBoom),
// 	)

// 	if err := r.RenewControlPlaneCertsOnOS(ctx, node, cfg, "", ssh); err == nil {
// 		t.Fatalf("RenewControlPlaneCertsOnOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_RenewEtcdCerts_Success(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(t.TempDir())

// 	ctx, node := context.Background(), "etcd-success"

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("cp -r pki")).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("etcdadm join")).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node, containsMatcher("etcdctl")).Return("", nil),
// 	)

// 	if err := r.RenewEtcdCertsOnOS(ctx, node, ssh); err != nil {
// 		t.Fatalf("RenewEtcdCertsOnOS() expected no error, got: %v", err)
// 	}
// }

// func TestLinuxRenewer_RenewControlPlaneCerts_Success(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(t.TempDir())

// 	ctx, node := context.Background(), "cp-success"
// 	cfg := &certificates.RenewalConfig{}

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.BackupControlPlaneCerts("", false, r.Backup)).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.RenewControlPlaneCerts("", false)).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			"sudo kubeadm certs check-expiration").Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.RestartControlPlaneStaticPods()).Return("", nil),
// 	)

// 	if err := r.RenewControlPlaneCertsOnOS(ctx, node, cfg, "", ssh); err != nil {
// 		t.Fatalf("RenewControlPlaneCertsOnOS() expected no error, got: %v", err)
// 	}
// }

// func TestLinuxRenewer_TransferCertsToControlPlane_Success(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	localDir := filepath.Join(tmp, certificates.TempLocalEtcdCertsDir)
// 	if err := os.MkdirAll(localDir, 0o700); err != nil {
// 		t.Fatalf("failed to create directory: %v", err)
// 	}
// 	if err := os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.crt"), []byte("crt"), 0o600); err != nil {
// 		t.Fatalf("failed to write certificate file: %v", err)
// 	}
// 	if err := os.WriteFile(filepath.Join(localDir, "apiserver-etcd-client.key"), []byte("key"), 0o600); err != nil {
// 		t.Fatalf("failed to write key file: %v", err)
// 	}

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(tmp)

// 	ctx, node := context.Background(), "cp-success"

// 	gomock.InOrder(
// 		ssh.EXPECT().
// 			RunCommand(ctx, node, containsMatcher("tee /tmp/apiserver-etcd-client.crt")).
// 			Return("", nil),
// 		ssh.EXPECT().
// 			RunCommand(ctx, node, containsMatcher("tee /tmp/apiserver-etcd-client.key")).
// 			Return("", nil),
// 	)

// 	if err := r.TransferCertsToControlPlaneOS(ctx, node, ssh); err != nil {
// 		t.Fatalf("TransferCertsToControlPlaneOS() expected no error, got: %v", err)
// 	}
// }

// func TestLinuxRenewer_CopyEtcdCerts_CertEmpty(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(t.TempDir())

// 	ctx, node := context.Background(), "cert-empty"

// 	ssh.EXPECT().
// 		RunCommand(ctx, node, containsMatcher("apiserver-etcd-client.crt")).
// 		Return("", nil)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_CopyEtcdCerts_WriteCertFileError(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	readOnlyDir := filepath.Join(tmp, certificates.TempLocalEtcdCertsDir)
// 	if err := os.MkdirAll(readOnlyDir, 0o444); err != nil {
// 		t.Fatalf("setup: %v", err)
// 	}

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(tmp)

// 	ctx, node := context.Background(), "write-cert-fail"

// 	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("cert-content", nil)
// 	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key")).Return("key-content", nil)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_CopyEtcdCerts_WriteKeyFileError(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	tmp := t.TempDir()
// 	localDir := filepath.Join(tmp, certificates.TempLocalEtcdCertsDir)
// 	if err := os.MkdirAll(localDir, 0o755); err != nil {
// 		t.Fatalf("setup: %v", err)
// 	}

// 	certPath := filepath.Join(localDir, "apiserver-etcd-client.crt")
// 	if err := os.WriteFile(certPath, []byte("cert"), 0o600); err != nil {
// 		t.Fatalf("setup cert: %v", err)
// 	}

// 	keyPath := filepath.Join(localDir, "apiserver-etcd-client.key")
// 	if err := os.Mkdir(keyPath, 0o755); err != nil {
// 		t.Fatalf("setup key conflict: %v", err)
// 	}

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(tmp)

// 	ctx, node := context.Background(), "write-key-fail"

// 	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".crt")).Return("cert-content", nil)
// 	ssh.EXPECT().RunCommand(ctx, node, containsMatcher(".key")).Return("key-content", nil)

// 	if err := r.CopyEtcdCertsFromOS(ctx, node, ssh); err == nil {
// 		t.Fatalf("CopyEtcdCertsFromOS() expected error, got nil")
// 	}
// }

// func TestLinuxRenewer_RenewControlPlaneCerts_NoExternalEtcd_Success(t *testing.T) {
// 	ctrl := gomock.NewController(t)
// 	defer ctrl.Finish()

// 	ssh := mocks.NewMockSSHRunner(ctrl)
// 	r := certificates.NewLinuxRenewer(t.TempDir())

// 	ctx, node := context.Background(), "no-external-etcd"
// 	cfg := &certificates.RenewalConfig{}

// 	gomock.InOrder(
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.BackupControlPlaneCerts("", false, r.Backup)).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.RenewControlPlaneCerts("", false)).Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			"sudo kubeadm certs check-expiration").Return("", nil),
// 		ssh.EXPECT().RunCommand(ctx, node,
// 			r.RestartControlPlaneStaticPods()).Return("", nil),
// 	)

// 	if err := r.RenewControlPlaneCertsOnOS(ctx, node, cfg, "", ssh); err != nil {
// 		t.Fatalf("RenewControlPlaneCertsOnOS() expected no error, got: %v", err)
// 	}
// }

// func TestLinuxRenewer_CopyExternalEtcdCerts_NoExternalEtcd_ReturnsTrue(t *testing.T) {
// 	r := certificates.NewLinuxRenewer(t.TempDir())

// 	result := r.CopyExternalEtcdCerts(false)
// 	if result != "true" {
// 		t.Fatalf("copyExternalEtcdCerts(false) expected 'true', got: %s", result)
// 	}
// }
