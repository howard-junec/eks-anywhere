package e2e

import (
	"encoding/base64"
	"fmt"
	"os"
	"regexp"

	"github.com/go-logr/logr"

	"github.com/aws/eks-anywhere/internal/pkg/ssm"
	e2etests "github.com/aws/eks-anywhere/test/framework"
)

const sshPrivateKeyVar = "T_SSH_PRIVATE_KEY"

func (e *E2ESession) setupEtcdEncryption(testRegex string) error {
	re := regexp.MustCompile(`^.*EtcdEncryption.*$`)
	if !re.MatchString(testRegex) {
		return nil
	}

	for _, eVar := range e2etests.RequiredEtcdEncryptionEnvVars() {
		if val, ok := os.LookupEnv(eVar); ok {
			e.testEnvVars[eVar] = val
		}
	}

	return e.installSSHPrivateKey()
}

func (e *E2ESession) installSSHPrivateKey() error {
	sshPrivateKey, ok := os.LookupEnv(sshPrivateKeyVar)
	if !ok {
		return fmt.Errorf("required env var %s is not set", sshPrivateKeyVar)
	}

	if _, err := base64.StdEncoding.DecodeString(sshPrivateKey); err != nil {
		return fmt.Errorf("decoding ssh key: %v", err)
	}

	command := fmt.Sprintf(
		"umask 077; printf '%%s' '%s' | base64 --decode > %s",
		sshPrivateKey,
		e2etests.SSHKeyPath,
	)
	if err := ssm.Run(e.session, logr.Discard(), e.instanceId, command, ssmTimeout); err != nil {
		return fmt.Errorf("mounting ssh key in instance: %v", err)
	}

	return nil
}
