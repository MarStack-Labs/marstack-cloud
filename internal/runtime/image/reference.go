package image

import (
	"fmt"
	"strings"
)

const (
	defaultRegistry  = "registry-1.docker.io"
	defaultNamespace = "library"
	defaultTag       = "latest"
)

type Reference struct {
	Registry   string
	Repository string
	Tag        string
	Digest     string
}

func (r Reference) String() string {
	if r.Digest != "" {
		return r.Registry + "/" + r.Repository + "@" + r.Digest
	}
	return r.Registry + "/" + r.Repository + ":" + r.Tag
}

func (r Reference) target() string {
	if r.Digest != "" {
		return r.Digest
	}
	return r.Tag
}

func ParseReference(raw string) (Reference, error) {
	if raw == "" {
		return Reference{}, fmt.Errorf("image reference is empty")
	}

	remainder := raw
	registry := defaultRegistry

	if head, tail, found := strings.Cut(remainder, "/"); found && looksLikeRegistry(head) {
		registry = head
		remainder = tail
	}

	var (
		tag    string
		digest string
	)

	if name, hash, found := strings.Cut(remainder, "@"); found {
		remainder = name
		digest = hash
	} else {
		remainder, tag = splitTag(remainder)
	}

	if remainder == "" {
		return Reference{}, fmt.Errorf("image reference %q has no repository", raw)
	}
	if registry == defaultRegistry && !strings.Contains(remainder, "/") {
		remainder = defaultNamespace + "/" + remainder
	}
	if digest == "" && tag == "" {
		tag = defaultTag
	}
	if digest != "" && !strings.HasPrefix(digest, "sha256:") {
		return Reference{}, fmt.Errorf("image reference %q has an unsupported digest", raw)
	}

	return Reference{Registry: registry, Repository: remainder, Tag: tag, Digest: digest}, nil
}

func splitTag(repository string) (string, string) {
	slash := strings.LastIndexByte(repository, '/')
	colon := strings.LastIndexByte(repository, ':')

	if colon < 0 || colon < slash {
		return repository, ""
	}
	return repository[:colon], repository[colon+1:]
}

func looksLikeRegistry(candidate string) bool {
	if candidate == "localhost" {
		return true
	}
	return strings.ContainsAny(candidate, ".:")
}
