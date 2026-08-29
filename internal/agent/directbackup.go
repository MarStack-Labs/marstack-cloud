package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/sealed"
)

const maxBackupBytes = 512 << 30

func (a *Agent) uploadDirect(
	ctx context.Context, target transferView, content io.Reader,
) (uploadedBody, error) {
	staged, err := os.CreateTemp("", "marstack-backup-*")
	if err != nil {
		return uploadedBody{}, fmt.Errorf("open a staging file: %w", err)
	}
	path := staged.Name()
	defer os.Remove(path)

	measured, err := sealForUpload(staged, content, target.Key)
	if err != nil {
		staged.Close()
		return uploadedBody{}, err
	}
	if err := staged.Close(); err != nil {
		return uploadedBody{}, fmt.Errorf("close the staging file: %w", err)
	}

	body, err := os.Open(path)
	if err != nil {
		return uploadedBody{}, fmt.Errorf("reopen the staging file: %w", err)
	}
	defer body.Close()

	info, err := body.Stat()
	if err != nil {
		return uploadedBody{}, fmt.Errorf("measure the staging file: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target.URL, body)
	if err != nil {
		return uploadedBody{}, fmt.Errorf("build the upload: %w", err)
	}
	req.ContentLength = info.Size()
	req.Header.Set("Content-Type", "application/octet-stream")

	res, err := a.client.transfer.Do(req)
	if err != nil {
		return uploadedBody{}, fmt.Errorf("upload to the object store: %w", err)
	}
	defer res.Body.Close()

	detail, _ := io.ReadAll(io.LimitReader(res.Body, 4*1024))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return uploadedBody{}, fmt.Errorf("the object store answered %d: %s",
			res.StatusCode, detail)
	}

	return uploadedBody{SizeBytes: measured.Size, Checksum: measured.Checksum}, nil
}

func sealForUpload(dst io.Writer, src io.Reader, key string) (sealed.Measured, error) {
	if key == "" {
		return sealed.CopyMeasured(dst, src, maxBackupBytes)
	}

	k, err := sealed.ParseKey(key)
	if err != nil {
		return sealed.Measured{}, fmt.Errorf("read the content key: %w", err)
	}
	return sealed.SealMeasured(dst, src, k, maxBackupBytes)
}
