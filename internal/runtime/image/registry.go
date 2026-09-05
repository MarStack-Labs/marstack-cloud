package image

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	manifestListV2 = "application/vnd.docker.distribution.manifest.list.v2+json"
	manifestV2     = "application/vnd.docker.distribution.manifest.v2+json"
	ociIndex       = "application/vnd.oci.image.index.v1+json"
	ociManifest    = "application/vnd.oci.image.manifest.v1+json"

	requestTimeout  = 2 * time.Minute
	manifestTimeout = 20 * time.Second
	maxManifest     = 4 << 20
	maxLayer        = 2 << 30
)

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
	} `json:"platform"`
}

type manifest struct {
	MediaType string       `json:"mediaType"`
	Manifests []descriptor `json:"manifests"`
	Config    descriptor   `json:"config"`
	Layers    []descriptor `json:"layers"`
}

type Credential struct {
	Host     string
	Username string
	Password string
}

type registry struct {
	http     *http.Client
	insecure bool

	mu     sync.Mutex
	logins map[string]Credential
	auth   map[string]string
}

func newRegistry(insecure bool) *registry {
	return &registry{
		http:     &http.Client{Timeout: requestTimeout},
		insecure: insecure,
		logins:   map[string]Credential{},
		auth:     map[string]string{},
	}
}

func (r *registry) useCredentials(held []Credential) {
	logins := make(map[string]Credential, len(held))
	for _, one := range held {
		logins[strings.ToLower(one.Host)] = one
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if sameLogins(r.logins, logins) {
		return
	}
	r.logins = logins
	r.auth = map[string]string{}
}

func sameLogins(held, wanted map[string]Credential) bool {
	if len(held) != len(wanted) {
		return false
	}
	for host, one := range wanted {
		if held[host] != one {
			return false
		}
	}
	return true
}

func (r *registry) authorization(repository string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.auth[repository]
}

func (r *registry) setAuth(repository, header string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.auth[repository] = header
}

func (r *registry) login(host string) (Credential, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	one, found := r.logins[strings.ToLower(host)]
	return one, found
}

func (r *registry) scheme() string {
	if r.insecure {
		return "http"
	}
	return "https"
}

func (r *registry) get(ctx context.Context, ref Reference, path string, accept []string) (*http.Response, error) {
	endpoint := r.scheme() + "://" + ref.Registry + "/v2/" + ref.Repository + path

	for attempt := range 2 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		for _, media := range accept {
			req.Header.Add("Accept", media)
		}
		if header := r.authorization(ref.Repository); header != "" {
			req.Header.Set("Authorization", header)
		}

		res, err := r.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("call registry: %w", err)
		}
		if res.StatusCode != http.StatusUnauthorized || attempt == 1 {
			if res.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
				res.Body.Close()
				return nil, fmt.Errorf("registry returned %d for %s: %s",
					res.StatusCode, endpoint, strings.TrimSpace(string(body)))
			}
			return res, nil
		}

		challenge := res.Header.Get("WWW-Authenticate")
		res.Body.Close()

		if err := r.authenticate(ctx, ref, challenge); err != nil {
			return nil, err
		}
	}

	return nil, fmt.Errorf("registry kept refusing the request for %s", ref)
}

func (r *registry) authenticate(ctx context.Context, ref Reference, challenge string) error {
	if strings.HasPrefix(strings.ToLower(challenge), "basic ") {
		held, found := r.login(ref.Registry)
		if !found {
			return fmt.Errorf(
				"%s asks for a username and password and this node holds none for it",
				ref.Registry)
		}
		r.setAuth(ref.Repository, basicHeader(held))
		return nil
	}
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return fmt.Errorf("registry asked for an unsupported authentication scheme: %q", challenge)
	}

	params := map[string]string{}
	for _, field := range splitChallenge(challenge[len("bearer "):]) {
		key, value, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		params[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}

	realm := params["realm"]
	if realm == "" {
		return fmt.Errorf("registry authentication challenge has no realm")
	}

	query := url.Values{}
	if service := params["service"]; service != "" {
		query.Set("service", service)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + ref.Repository + ":pull"
	}
	query.Set("scope", scope)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm+"?"+query.Encode(), nil)
	if err != nil {
		return fmt.Errorf("build token request: %w", err)
	}
	if held, found := r.login(ref.Registry); found {
		req.SetBasicAuth(held.Username, held.Password)
	}

	res, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("fetch token: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("token endpoint returned %d", res.StatusCode)
	}

	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxManifest)).Decode(&body); err != nil {
		return fmt.Errorf("decode token: %w", err)
	}

	token := body.Token
	if token == "" {
		token = body.AccessToken
	}
	if token == "" {
		return fmt.Errorf("token endpoint returned no token")
	}

	r.setAuth(ref.Repository, "Bearer "+token)
	return nil
}

func splitChallenge(value string) []string {
	fields := make([]string, 0, 3)
	quoted := false
	current := strings.Builder{}

	for _, char := range value {
		switch {
		case char == '"':
			quoted = !quoted
			current.WriteRune(char)
		case char == ',' && !quoted:
			fields = append(fields, current.String())
			current.Reset()
		default:
			current.WriteRune(char)
		}
	}
	if current.Len() > 0 {
		fields = append(fields, current.String())
	}
	return fields
}

func (r *registry) manifest(ctx context.Context, ref Reference) (manifest, error) {
	accept := []string{manifestListV2, manifestV2, ociIndex, ociManifest}

	ctx, cancel := context.WithTimeout(ctx, manifestTimeout)
	defer cancel()

	res, err := r.get(ctx, ref, "/manifests/"+ref.target(), accept)
	if err != nil {
		return manifest{}, err
	}
	defer res.Body.Close()

	var parsed manifest
	if err := json.NewDecoder(io.LimitReader(res.Body, maxManifest)).Decode(&parsed); err != nil {
		return manifest{}, fmt.Errorf("decode manifest: %w", err)
	}

	if len(parsed.Manifests) == 0 {
		return parsed, nil
	}

	chosen, err := selectPlatform(parsed.Manifests)
	if err != nil {
		return manifest{}, err
	}

	child := ref
	child.Digest = chosen.Digest
	child.Tag = ""
	return r.manifest(ctx, child)
}

func selectPlatform(candidates []descriptor) (descriptor, error) {
	for _, candidate := range candidates {
		if candidate.Platform.OS == "linux" && candidate.Platform.Architecture == runtime.GOARCH {
			return candidate, nil
		}
	}
	return descriptor{}, fmt.Errorf("image has no linux/%s manifest", runtime.GOARCH)
}

func (r *registry) blob(ctx context.Context, ref Reference, digest string) (io.ReadCloser, error) {
	if err := validateDigest(digest); err != nil {
		return nil, err
	}

	res, err := r.get(ctx, ref, "/blobs/"+digest, nil)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func validateDigest(digest string) error {
	if !digestPattern.MatchString(digest) {
		return fmt.Errorf("registry returned an unusable digest %q", digest)
	}
	return nil
}

func verifyDigest(data []byte, digest string) error {
	if err := validateDigest(digest); err != nil {
		return err
	}
	expected := strings.TrimPrefix(digest, "sha256:")

	sum := sha256.Sum256(data)
	if actual := hex.EncodeToString(sum[:]); actual != expected {
		return fmt.Errorf("blob digest mismatch: got sha256:%s, want %s", actual, digest)
	}
	return nil
}

func basicHeader(c Credential) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Username+":"+c.Password))
}
