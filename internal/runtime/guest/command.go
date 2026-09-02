package guest

import (
	"fmt"

	"github.com/marstack-labs/marstack-cloud/internal/workload"
)

func CommandFor(wanted, image []string) ([]string, error) {
	if len(wanted) > 0 {
		return wanted, nil
	}
	if len(image) > 0 {
		return image, nil
	}
	return nil, fmt.Errorf("%w: the image declares no command and none was given",
		workload.ErrUnstartable)
}
