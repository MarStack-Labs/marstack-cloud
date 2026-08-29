package agent

import (
	"encoding/base64"
	"fmt"
	"strconv"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func fileDrops(views []fileView) ([]workload.FileDrop, error) {
	if len(views) == 0 {
		return nil, nil
	}

	drops := make([]workload.FileDrop, 0, len(views))
	for _, v := range views {
		content, err := base64.StdEncoding.DecodeString(v.Content)
		if err != nil {
			return nil, fmt.Errorf("decode the content of %s: %w", v.Path, err)
		}

		mode := uint64(0o600)
		if v.Mode != "" {
			mode, err = strconv.ParseUint(v.Mode, 8, 32)
			if err != nil || mode > 0o777 {
				return nil, fmt.Errorf("the mode of %s is not three or four octal digits", v.Path)
			}
		}

		drops = append(drops, workload.FileDrop{
			Path:    v.Path,
			Content: content,
			Mode:    uint32(mode),
		})
	}
	return drops, nil
}
