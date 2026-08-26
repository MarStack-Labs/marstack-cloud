package image

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/marstack-labs/marstack-cloud/internal/kernel/fault"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/ids"
	"github.com/marstack-labs/marstack-cloud/internal/kernel/validate"
)

var (
	checksumPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	namePattern     = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,61}[a-z0-9])?$`)
)

type clock func() time.Time

type service struct {
	repo *repository
	now  clock
}

func newService(repo *repository, now clock) *service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &service{repo: repo, now: now}
}

func (s *service) create(ctx context.Context, params CreateParams) (Image, error) {
	if err := checkName(params.Name); err != nil {
		return Image{}, err
	}
	if err := validate.OneOf("kind", params.Kind, Kinds()...); err != nil {
		return Image{}, err
	}

	if params.Arch == "" {
		params.Arch = ArchARM64
	}
	if err := validate.OneOf("arch", params.Arch, Arches()...); err != nil {
		return Image{}, err
	}

	source, err := checkSource(params.Source)
	if err != nil {
		return Image{}, err
	}

	checksum := strings.ToLower(strings.TrimSpace(params.Checksum))
	if checksum != "" && !checksumPattern.MatchString(checksum) {
		return Image{}, fault.Invalid("invalid_checksum",
			"the checksum must look like sha256:<64 hex characters>")
	}

	in := Image{
		ID:        ids.New("img"),
		Name:      params.Name,
		Kind:      params.Kind,
		Arch:      params.Arch,
		Source:    source,
		Checksum:  checksum,
		CreatedAt: s.now(),
	}

	if err := s.repo.insert(ctx, in); err != nil {
		return Image{}, translate(err)
	}
	return in, nil
}

func checkName(name string) error {
	if !namePattern.MatchString(name) {
		return fault.Invalid("invalid_name",
			"name must be 1-63 characters of lowercase letters, digits, dots or hyphens, "+
				"starting and ending with a letter or digit")
	}
	if strings.Contains(name, "..") {
		return fault.Invalid("invalid_name",
			"name must not contain .. because a node turns it into a file name")
	}
	return nil
}

func checkSource(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fault.Invalid("invalid_source", "source must not be empty")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fault.Invalid("invalid_source", "source is not a valid URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fault.Invalid("invalid_source",
			"source must be an http or https URL a node can download from")
	}
	if parsed.Host == "" {
		return "", fault.Invalid("invalid_source", "source has no host")
	}
	return parsed.String(), nil
}

func (s *service) resolve(ctx context.Context, nameOrID string) (Image, error) {
	in, err := s.repo.byName(ctx, nameOrID)
	if err == nil {
		return in, nil
	}
	if !errors.Is(err, errNotFound) {
		return Image{}, translate(err)
	}

	in, err = s.repo.byID(ctx, nameOrID)
	if err != nil {
		return Image{}, translate(err)
	}
	return in, nil
}

func (s *service) list(ctx context.Context) ([]Image, error) {
	images, err := s.repo.list(ctx)
	if err != nil {
		return nil, translate(err)
	}
	return images, nil
}

func (s *service) remove(ctx context.Context, id string) error {
	if err := s.repo.delete(ctx, id); err != nil {
		return translate(err)
	}
	return nil
}

func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errNotFound):
		return fault.NotFound("image_not_found", "no image with that name or id exists")
	case errors.Is(err, errNameTaken):
		return fault.Conflict("image_name_taken", "an image with that name already exists")
	default:
		return err
	}
}
