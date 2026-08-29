package cli

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
)

type fileSpec struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    string `json:"mode,omitempty"`
}

func readFiles(pairs []string) ([]fileSpec, error) {
	if len(pairs) == 0 {
		return nil, nil
	}

	files := make([]fileSpec, 0, len(pairs))
	for _, pair := range pairs {
		target, rest, found := strings.Cut(pair, "=")
		if !found {
			return nil, errors.New(
				"a file is /path/in/the/workload=local-file, and " + pair + " has no local file")
		}

		source, mode := rest, ""
		if cut := strings.LastIndex(rest, ":"); cut != -1 {
			source, mode = rest[:cut], rest[cut+1:]
		}

		content, err := os.ReadFile(source)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", source, err)
		}

		files = append(files, fileSpec{
			Path:    target,
			Content: base64.StdEncoding.EncodeToString(content),
			Mode:    mode,
		})
	}
	return files, nil
}
