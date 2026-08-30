package service

import (
	"errors"
	"fmt"
	"regexp"
	"sort"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func checkEnv(env map[string]string) error {
	if len(env) > MaxEnv {
		return fault.Invalid("invalid_env", fmt.Sprintf(
			"a service carries at most %d environment variables, and %d were given",
			MaxEnv, len(env)))
	}
	for name := range env {
		if !envNamePattern.MatchString(name) {
			return fault.Invalid("invalid_env", fmt.Sprintf(
				"%q is not an environment variable name", name))
		}
	}
	return nil
}

func namesOf(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sealEnv(env map[string]string, keys *sealed.Keyring) (string, string, error) {
	if len(env) == 0 {
		return "", "", nil
	}

	blob, keyID, err := sealed.SealJSON(env, keys)
	if errors.Is(err, sealed.ErrNoKey) {
		return "", "", fault.Conflict("no_sealing_key",
			"this control plane has no key to seal an environment with, and every replica "+
				"would carry it: start it with --backup-key-file, or leave env off")
	}
	if err != nil {
		return "", "", fault.Internal(fmt.Errorf("seal the environment: %w", err))
	}
	return blob, keyID, nil
}

func openEnv(blob, keyID string, keys *sealed.Keyring) (map[string]string, error) {
	if blob == "" {
		return nil, nil
	}

	env := map[string]string{}
	err := sealed.OpenJSON(blob, keyID, keys, &env)

	if errors.Is(err, sealed.ErrKeyMissing) {
		return nil, fault.Conflict("env_key_missing",
			"this service's environment was sealed with key "+keyID+
				", which this control plane does not hold")
	}
	if err != nil {
		return nil, fault.Internal(fmt.Errorf("unseal the environment: %w", err))
	}
	return env, nil
}
