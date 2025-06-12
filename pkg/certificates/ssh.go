package certificates

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"github.com/aws/eks-anywhere/pkg/logger"
)

// sshClient interface defines the methods we need from ssh.Client.
type sshClient interface {
	Close() error
	NewSession() (*ssh.Session, error)
}

// sshDialer is a function type for dialing SSH connections.
type sshDialer func(network, addr string, config *ssh.ClientConfig) (sshClient, error)

// SSHRunner provides methods for running commands over SSH.
type SSHRunner interface {
	// RunCommand runs a command on the remote host
	RunCommand(ctx context.Context, node string, cmd string) error

	// RunCommandWithOutput runs a command on the remote host and returns the output
	RunCommandWithOutput(ctx context.Context, node string, cmd string) (string, error)

	// InitSSHConfig initializes the SSH configuration
	InitSSHConfig(user, keyPath, passwd string) error
}

// DefaultSSHRunner is the default implementation of SSHRunner.
type DefaultSSHRunner struct {
	sshConfig  *ssh.ClientConfig
	sshDialer  sshDialer
	sshKeyPath string
}

// NewSSHRunner creates a new DefaultSSHRunner.
func NewSSHRunner() *DefaultSSHRunner {
	return &DefaultSSHRunner{
		sshDialer: func(network, addr string, config *ssh.ClientConfig) (sshClient, error) {
			return ssh.Dial(network, addr, config)
		},
	}
}

// InitSSHConfig initializes the SSH configuration.
func (r *DefaultSSHRunner) InitSSHConfig(user, keyPath, passwd string) error {
	r.sshKeyPath = keyPath // Store SSH key path.
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("reading SSH key: %v", err)
	}

	var signer ssh.Signer
	signer, err = ssh.ParsePrivateKey(key)
	if err != nil {
		if err.Error() == "ssh: this private key is passphrase protected" {
			if passwd == "" {
				// fmt.Printf("Enter passphrase for SSH key '%s': ", keyPath)
				logger.Info("Enter passphrase for SSH key", "path", keyPath)
				var passphrase []byte
				passphrase, err = term.ReadPassword(int(os.Stdin.Fd()))
				if err != nil {
					return fmt.Errorf("reading passphrase: %v", err)
				}
				// fmt.Println()
				logger.Info("")
				passwd = string(passphrase)
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(passwd))
			if err != nil {
				return fmt.Errorf("parsing SSH key with passphrase: %v", err)
			}
		} else {
			return fmt.Errorf("parsing SSH key: %v", err)
		}
	}

	r.sshConfig = &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(signer),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	return nil
}

// RunCommand runs a command on the remote host.
func (r *DefaultSSHRunner) RunCommand(ctx context.Context, node string, cmd string) error {
	client, err := r.sshDialer("tcp", fmt.Sprintf("%s:22", node), r.sshConfig)
	if err != nil {
		return fmt.Errorf("failed to connect to node %s: %v", node, err)
	}
	defer client.Close()

	done := make(chan error, 1)
	go func() {
		session, err := client.NewSession()
		if err != nil {
			done <- fmt.Errorf("creating session: %v", err)
			return
		}
		defer session.Close()

		if VerbosityLevel >= 2 {
			session.Stdout = os.Stdout
			session.Stderr = os.Stderr
			done <- session.Run(cmd)
			return
		}

		var stdout, stderr bytes.Buffer
		session.Stdout = &stdout
		session.Stderr = &stderr

		err = session.Run(cmd)

		if strings.Contains(cmd, "kubeadm certs check-expiration") && VerbosityLevel >= 1 {
			lines := strings.Split(stdout.String(), "\n")
			logger.Info("Certificate check results", "node", node)
			for _, line := range lines {
				if line != "" {
					logger.Info(line)
				}
			}
		}

		done <- err

	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("cancelling command: %v", ctx.Err())
	case err := <-done:
		if err != nil {
			return fmt.Errorf("executing command: %v", err)
		}
		return nil
	}
}

// RunCommandWithOutput runs a command on the remote host and returns the output.
func (r *DefaultSSHRunner) RunCommandWithOutput(ctx context.Context, node string, cmd string) (string, error) {
	client, err := r.sshDialer("tcp", fmt.Sprintf("%s:22", node), r.sshConfig)
	if err != nil {
		return "", fmt.Errorf("failed to connect to node %s: %v", node, err)
	}
	defer client.Close()

	type result struct {
		output string
		err    error
	}
	done := make(chan result, 1)

	go func() {
		session, err := client.NewSession()
		if err != nil {
			done <- result{"", fmt.Errorf("creating session: %v", err)}
			return
		}
		defer session.Close()

		// 	output, err := session.Output(cmd)
		// 	if err != nil {
		// 		done <- result{"", fmt.Errorf("executing command: %v", err)}
		// 		return
		// 	}
		// 	done <- result{strings.TrimSpace(string(output)), nil}
		// }()
		outputBytes, err := session.CombinedOutput(cmd)
		output := strings.TrimSpace(string(outputBytes))

		if err != nil {
			if output != "" {
				done <- result{output, fmt.Errorf("executing command: %v, output: %s", err, output)}
			} else {
				done <- result{"", fmt.Errorf("executing command: %v", err)}
			}
			return
		}

		done <- result{output, nil}
	}()

	select {
	case <-ctx.Done():
		return "", fmt.Errorf("cancelling command: %v", ctx.Err())
	case res := <-done:
		return res.output, res.err
	}
}
