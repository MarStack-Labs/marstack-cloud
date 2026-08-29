package instance

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
)

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validateEnv(env map[string]string) error {
	if len(env) > MaxEnv {
		return fault.Invalid("invalid_env", fmt.Sprintf(
			"an instance carries at most %d environment variables, and %d were given",
			MaxEnv, len(env)))
	}

	for name, value := range env {
		if !envNamePattern.MatchString(name) || len(name) > MaxEnvNameLen {
			return fault.Invalid("invalid_env", fmt.Sprintf(
				"%q is not an environment variable name: use letters, digits and underscores, "+
					"not starting with a digit, at most %d characters", name, MaxEnvNameLen))
		}
		if len(value) > MaxEnvValueLen {
			return fault.Invalid("invalid_env", fmt.Sprintf(
				"the value of %s is %d bytes, and the limit is %d",
				name, len(value), MaxEnvValueLen))
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

	active, sealing := keys.Active()
	if !sealing {
		return "", "", fault.Conflict("no_sealing_key",
			"this control plane has no key to seal an environment with, and storing "+
				"credentials in the clear is not something it will do quietly: start it with "+
				"--backup-key-file, or leave env off")
	}

	plain, err := json.Marshal(env)
	if err != nil {
		return "", "", fmt.Errorf("encode the environment: %w", err)
	}

	blob, err := sealed.SealBytes(plain, active)
	if err != nil {
		return "", "", fmt.Errorf("seal the environment: %w", err)
	}
	return blob, keys.ActiveID(), nil
}

func openEnv(blob, keyID string, keys *sealed.Keyring) (map[string]string, error) {
	if blob == "" {
		return nil, nil
	}

	plain := []byte(blob)
	if keyID != "" {
		k, held := keys.Find(keyID)
		if !held {
			return nil, fault.Conflict("env_key_missing",
				"this instance's environment was sealed with key "+keyID+
					", which this control plane does not hold")
		}

		opened, err := sealed.OpenBytes(blob, k)
		if err != nil {
			return nil, fault.Internal(fmt.Errorf("unseal the environment: %w", err))
		}
		plain = opened
	}

	env := map[string]string{}
	if err := json.Unmarshal(plain, &env); err != nil {
		return nil, fault.Internal(fmt.Errorf("decode the environment: %w", err))
	}
	return env, nil
}
