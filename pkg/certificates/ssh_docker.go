package certificates

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/aws/eks-anywhere/pkg/logger"
)

// DockerSSHRunner implements SSHRunner by running ssh commands inside a long-running tools container via `docker exec`.
type DockerSSHRunner struct {
	containerName string
	sshConfig     SSHConfig
	useAgent      bool
}

// NewDockerSSHRunner returns an instance bound to a specific tools container.
// func NewDockerSSHRunner(containerName string) *DockerSSHRunner {
// 	return &DockerSSHRunner{containerName: containerName}
// }

func NewDockerSSHRunner(containerName string, cfg SSHConfig) (*DockerSSHRunner, error) {
	r := &DockerSSHRunner{containerName: containerName}
	if err := r.InitSSHConfig(cfg); err != nil {
		return nil, err
	}
	return r, nil
}

// InitSSHConfig just stores the config; ssh key presence is checked lazily.
// func (r *DockerSSHRunner) InitSSHConfig(cfg SSHConfig) error {
// 	// Basic key existence check to fail fast.
// 	if _, err := os.Stat(cfg.KeyPath); err != nil {
// 		return fmt.Errorf("ssh key %s: %v", cfg.KeyPath, err)
// 	}
// 	r.sshConfig = cfg
// 	return nil
// }

func (r *DockerSSHRunner) InitSSHConfig(cfg SSHConfig) error {
	if r.useAgent && r.sshConfig.KeyPath == cfg.KeyPath {
		return nil
	}

	if _, err := os.Stat(cfg.KeyPath); err != nil {
		return fmt.Errorf("ssh key %s: %v", cfg.KeyPath, err)
	}
	r.sshConfig = cfg

	// ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	// defer cancel()

	//fast path, ssh-agent already exists in container
	ctx, cancelFast := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelFast()
	if r.isAgentLoaded(ctx) {
		r.useAgent = true
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const sentinel = "/tmp/agent_ready"
	agentReady := exec.CommandContext(ctx, "docker", "exec", r.containerName, "test", "-f", sentinel).Run() == nil

	var dockerCmd []string
	if agentReady {
		dockerCmd = []string{"exec", "-i", r.containerName, "ssh-add", r.sshConfig.KeyPath}
	} else {
		dockerCmd = []string{
			"exec", "-i", r.containerName,
			"bash", "-c",
			fmt.Sprintf(`eval $(ssh-agent) && ssh-add %s && touch %s`, r.sshConfig.KeyPath, sentinel),
		}
	}

	cmd := exec.CommandContext(ctx, "docker", dockerCmd...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("initializing ssh-agent in container: %v", err)
	}

	r.useAgent = true
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

// internal helper
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
	dockerArgs := []string{
		"exec", "-i",
		r.containerName,
		"ssh",
	}
	if !r.useAgent {
		dockerArgs = append(dockerArgs, "-i", r.sshConfig.KeyPath)
	}
	dockerArgs = append(dockerArgs,
		"-o", "StrictHostKeyChecking=no",
		fmt.Sprintf("%s@%s", r.sshConfig.User, node),
		cmdStr,
	)

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

	// Run with timeout awareness.
	done := make(chan error, 1)
	go func() { done <- c.Run() }()

	select {
	case <-ctx.Done():
		_ = c.Process.Kill() // ensure cleanup
		return "", fmt.Errorf("cancelling command: %v", ctx.Err())
	case err := <-done:
		if err != nil {
			return stderr.String(), fmt.Errorf("ssh error: %v; stderr: %s", err, stderr.String())
		}
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

func (r *DockerSSHRunner) isAgentLoaded(ctx context.Context) bool {
	cmd := exec.CommandContext(
		ctx, "docker", "exec", "-i", r.containerName, "ssh-add", "-l",
	)
	return cmd.Run() == nil
}
