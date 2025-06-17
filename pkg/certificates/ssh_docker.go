package certificates

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/aws/eks-anywhere/pkg/logger"
)

// DockerSSHRunner implements SSHRunner by running ssh commands inside a long-running tools container via `docker exec`.
type DockerSSHRunner struct {
	containerName string
	sshConfig     SSHConfig
	useAgent      bool
}

// NewDockerSSHRunner creates a new SSH runner that executes commands inside a Docker container.
func NewDockerSSHRunner(containerName string, cfg SSHConfig) (*DockerSSHRunner, error) {
	r := &DockerSSHRunner{containerName: containerName}
	if err := r.InitSSHConfig(cfg); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *DockerSSHRunner) InitSSHConfig(cfg SSHConfig) error {
	if _, err := os.Stat(cfg.KeyPath); err != nil {
		return fmt.Errorf("ssh key %s: %v", cfg.KeyPath, err)
	}
	r.sshConfig = cfg
	r.useAgent = false
	return nil
}

// RunCommand executes cmds (joined with &&) on node via ssh, no output returned.
func (r *DockerSSHRunner) RunCommand(ctx context.Context, node string, cmds []string) error {
	_, err := r.run(ctx, node, cmds, false)
	return err
}

// RunCommandWithOutput executes cmds and returns combined stdout+stderr.
func (r *DockerSSHRunner) RunCommandWithOutput(
	ctx context.Context,
	node string,
	cmds []string,
) (string, error) {
	out, err := r.run(ctx, node, cmds, true)
	return out, err
}

// DownloadFile copies remote file to local path using `cat` piping.
func (r *DockerSSHRunner) DownloadFile(
	ctx context.Context,
	node, remote, local string,
) error {
	out, err := r.RunCommandWithOutput(ctx, node, []string{fmt.Sprintf("sudo cat %s", remote)})
	if err != nil {
		return err
	}
	return os.WriteFile(local, []byte(out), 0o600)
}

// internal helper.
func (r *DockerSSHRunner) run(
	ctx context.Context,
	node string,
	cmds []string,
	capture bool,
) (string, error) {
	if len(cmds) == 0 {
		return "", fmt.Errorf("no command provided")
	}

	cmdStr := strings.Join(cmds, " && ")
	dockerArgs := r.buildDockerSSHCommand(node, cmdStr)

	// Build exec.Cmd
	c := exec.CommandContext(ctx, "docker", dockerArgs...)

	var stdout, stderr bytes.Buffer
	if capture || VerbosityLevel < 2 {
		c.Stdout = &stdout
		c.Stderr = &stderr
	} else {
		c.Stdout = os.Stdout
		c.Stderr = os.Stderr
	}

	// Run with timeout awareness
	err := r.executeWithTimeout(ctx, c)
	if err != nil {
		return stderr.String(), fmt.Errorf("ssh error: %v; stderr: %s", err, stderr.String())
	}

	// For cert-check verbosity mimicking DefaultSSHRunner
	if capture &&
		strings.Contains(cmdStr, "kubeadm certs check-expiration") &&
		VerbosityLevel >= 1 {
		logger.Info("Certificate check results", "node", node)
		for _, l := range strings.Split(stdout.String(), "\n") {
			if l != "" {
				logger.Info(l)
			}
		}
	}

	return strings.TrimSpace(stdout.String()), nil
}

func (r *DockerSSHRunner) buildDockerSSHCommand(node string, cmdStr string) []string {
	sshPassword := getSSHPassword(r.sshConfig)

	if sshPassword != "" {
		dockerArgs := []string{
			"exec", "-i",
			r.containerName,
			"sshpass", "-p", sshPassword,
			"ssh", "-i", r.sshConfig.KeyPath,
			"-o", "StrictHostKeyChecking=no",
			fmt.Sprintf("%s@%s", r.sshConfig.User, node),
			cmdStr,
		}
		return dockerArgs
	}

	dockerArgs := []string{
		"exec", "-i",
		r.containerName,
		"ssh", "-i", r.sshConfig.KeyPath,
		"-o", "StrictHostKeyChecking=no",
		fmt.Sprintf("%s@%s", r.sshConfig.User, node),
		cmdStr,
	}

	return dockerArgs
}

func (r *DockerSSHRunner) executeWithTimeout(ctx context.Context, cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill() // ensure cleanup
		return fmt.Errorf("cancelling command: %v", ctx.Err())
	case err := <-done:
		return err
	}
}

func getSSHPassword(cfg SSHConfig) string {
	if cfg.Password != "" {
		return cfg.Password
	}

	if cfg.component != "" {
		componentEnvVar := "EKSA_SSH_KEY_PASSPHRASE_" + cfg.component
		if envPassword := os.Getenv(componentEnvVar); envPassword != "" {
			return envPassword
		}
	} else {
		if envPassword := os.Getenv("EKSA_SSH_KEY_PASSPHRASE_ETCD"); envPassword != "" {
			return envPassword
		}
		if envPassword := os.Getenv("EKSA_SSH_KEY_PASSPHRASE_CP"); envPassword != "" {
			return envPassword
		}
	}

	if envPassword := os.Getenv("EKSA_SSH_KEY_PASSPHRASE"); envPassword != "" {
		return envPassword
	}

	return ""
}
