package instance

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strconv"

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

	blob, keyID, err := sealed.SealJSON(env, keys)
	if errors.Is(err, sealed.ErrNoKey) {
		return "", "", fault.Conflict("no_sealing_key",
			"this control plane has no key to seal an environment with, and storing "+
				"credentials in the clear is not something it will do quietly: start it with "+
				"--backup-key-file, or leave env off")
	}
	if err != nil {
		return "", "", fault.Internal(fmt.Errorf("seal the environment: %w", err))
	}
	return blob, keyID, nil
}

func openEnv(blob, keyID string, keys *sealed.Keyring) (map[string]string, error) {
	env := map[string]string{}

	err := sealed.OpenJSON(blob, keyID, keys, &env)
	if errors.Is(err, sealed.ErrKeyMissing) {
		return nil, fault.Conflict("env_key_missing",
			"this instance's environment was sealed with key "+keyID+
				", which this control plane does not hold")
	}
	if err != nil {
		return nil, fault.Internal(fmt.Errorf("unseal the environment: %w", err))
	}
	if blob == "" {
		return nil, nil
	}
	return env, nil
}

func validateFiles(files []File) error {
	if len(files) > MaxFiles {
		return fault.Invalid("invalid_files", fmt.Sprintf(
			"an instance carries at most %d files, and %d were given", MaxFiles, len(files)))
	}

	seen := make(map[string]bool, len(files))
	for _, f := range files {
		if !path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path {
			return fault.Invalid("invalid_files", fmt.Sprintf(
				"%q is not an absolute cleaned path, and a relative one has no meaning "+
					"before the workload has a working directory", f.Path))
		}
		if len(f.Path) > MaxFilePathLen {
			return fault.Invalid("invalid_files", "a file path is longer than the limit")
		}
		if seen[f.Path] {
			return fault.Invalid("invalid_files",
				"two files both write "+f.Path+", and which one wins would be undefined")
		}
		seen[f.Path] = true

		raw, err := base64.StdEncoding.DecodeString(f.Content)
		if err != nil {
			return fault.Invalid("invalid_files",
				"the content of "+f.Path+" is not base64")
		}
		if len(raw) > MaxFileBytes {
			return fault.Invalid("invalid_files", fmt.Sprintf(
				"%s is %d bytes, and the limit is %d: a config file is not a disk image",
				f.Path, len(raw), MaxFileBytes))
		}
		if _, err := parseMode(f.Mode); err != nil {
			return err
		}
	}
	return nil
}

func parseMode(mode string) (fs.FileMode, error) {
	if mode == "" {
		return 0o600, nil
	}

	parsed, err := strconv.ParseUint(mode, 8, 32)
	if err != nil || parsed > 0o777 {
		return 0, fault.Invalid("invalid_files",
			"a file mode is three or four octal digits, like 0644, and "+mode+" is not")
	}
	return fs.FileMode(parsed), nil
}

func pathsOf(files []File) []string {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	sort.Strings(paths)
	return paths
}

func sealFiles(files []File, keys *sealed.Keyring) (string, error) {
	if len(files) == 0 {
		return "", nil
	}

	blob, _, err := sealed.SealJSON(files, keys)
	if errors.Is(err, sealed.ErrNoKey) {
		return "", fault.Conflict("no_sealing_key",
			"this control plane has no key to seal a config file with, and a config file is "+
				"where credentials end up: start it with --backup-key-file, or leave files off")
	}
	if err != nil {
		return "", fault.Internal(fmt.Errorf("seal the files: %w", err))
	}
	return blob, nil
}

func openFiles(blob, keyID string, keys *sealed.Keyring) ([]File, error) {
	var files []File

	err := sealed.OpenJSON(blob, keyID, keys, &files)
	if errors.Is(err, sealed.ErrKeyMissing) {
		return nil, fault.Conflict("seal_key_missing",
			"this instance's files were sealed with key "+keyID+
				", which this control plane does not hold")
	}
	if err != nil {
		return nil, fault.Internal(fmt.Errorf("unseal the files: %w", err))
	}
	return files, nil
}
