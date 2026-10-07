package wsldocker

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/AviBackToBlack/container-bin/internal/policy"
)

const (
	maxImageInspectOutput = 1 << 20
	maxImagePullOutput    = 16 << 20
	maxImageReference     = 1024
)

// ImageSnapshot is the bounded immutable image identity returned by Docker
// Desktop after a pull or for an explicitly selected local image.
type ImageSnapshot struct {
	id          string
	repoDigests []string
}

func (s ImageSnapshot) ID() string            { return s.id }
func (s ImageSnapshot) RepoDigests() []string { return append([]string(nil), s.repoDigests...) }

func pullImage(ctx context.Context, reference string, deps operationDependencies) error {
	if ctx == nil {
		return errors.New("Docker Desktop WSL image pull requires a context")
	}
	if err := validateEngineImageReference(reference, false); err != nil {
		return fmt.Errorf("Docker Desktop WSL image pull: %w", err)
	}
	if deps.check == nil || deps.statSocket == nil || deps.perform == nil {
		return errors.New("Docker Desktop WSL image pull dependencies are incomplete")
	}
	response, err := execute(ctx, Request{
		Method:          http.MethodPost,
		Path:            "/images/create",
		Query:           url.Values{"fromImage": {reference}},
		SuccessStatuses: []int{http.StatusOK},
	}, deps)
	if err != nil {
		return err
	}
	return decodeImagePullResponse(response.Body, reference)
}

func inspectImage(ctx context.Context, reference string, deps operationDependencies) (ImageSnapshot, error) {
	if ctx == nil {
		return ImageSnapshot{}, errors.New("Docker Desktop WSL image inspect requires a context")
	}
	if err := validateEngineImageReference(reference, true); err != nil {
		return ImageSnapshot{}, fmt.Errorf("Docker Desktop WSL image inspect: %w", err)
	}
	if deps.check == nil || deps.statSocket == nil || deps.perform == nil {
		return ImageSnapshot{}, errors.New("Docker Desktop WSL image inspect dependencies are incomplete")
	}
	response, err := execute(ctx, Request{
		Method:          http.MethodGet,
		Path:            "/images/" + reference + "/json",
		SuccessStatuses: []int{http.StatusOK},
	}, deps)
	if err != nil {
		return ImageSnapshot{}, err
	}
	return decodeImageInspectResponse(response.Body)
}

func decodeImagePullResponse(raw []byte, reference string) error {
	if len(raw) > maxImagePullOutput {
		return fmt.Errorf("Docker image pull response exceeds %d bytes", maxImagePullOutput)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	frames := 0
	for {
		var frame struct {
			Error       string `json:"error"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail"`
		}
		if err := decoder.Decode(&frame); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return fmt.Errorf("decode Docker image pull response for %s: %w", reference, err)
		}
		frames++
		message := frame.Error
		if frame.ErrorDetail != nil && frame.ErrorDetail.Message != "" {
			message = frame.ErrorDetail.Message
		}
		if message != "" {
			if validDockerMessage(message) {
				return fmt.Errorf("Docker image pull for %s failed: %s", reference, message)
			}
			return fmt.Errorf("Docker image pull for %s failed with an unsafe or malformed daemon error", reference)
		}
	}
	if frames == 0 {
		return fmt.Errorf("Docker image pull for %s returned no progress records", reference)
	}
	return nil
}

func decodeImageInspectResponse(raw []byte) (ImageSnapshot, error) {
	if len(raw) > maxImageInspectOutput {
		return ImageSnapshot{}, fmt.Errorf("Docker image inspect response exceeds %d bytes", maxImageInspectOutput)
	}
	var response struct {
		ID          string   `json:"Id"`
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return ImageSnapshot{}, fmt.Errorf("decode Docker image inspect response: %w", err)
	}
	if !validImageID(response.ID) {
		return ImageSnapshot{}, fmt.Errorf("Docker image inspect returned invalid image ID %q", response.ID)
	}
	for _, digest := range response.RepoDigests {
		if err := validateRepoDigestText(digest); err != nil {
			return ImageSnapshot{}, fmt.Errorf("Docker image inspect returned invalid RepoDigest %q: %w", digest, err)
		}
	}
	return ImageSnapshot{id: response.ID, repoDigests: append([]string(nil), response.RepoDigests...)}, nil
}

func validateEngineImageReference(reference string, allowLocalID bool) error {
	if reference == "" || len(reference) > maxImageReference || strings.TrimSpace(reference) != reference {
		return fmt.Errorf("invalid image reference %q", reference)
	}
	for _, r := range reference {
		if unicode.IsControl(r) {
			return fmt.Errorf("invalid image reference %q", reference)
		}
	}
	if validImageID(reference) {
		if allowLocalID {
			return nil
		}
		return errors.New("image pull requires a registry reference, not a local image ID")
	}
	if _, err := policy.CanonicalRepository(reference); err != nil {
		return fmt.Errorf("invalid image reference %q: %w", reference, err)
	}
	return nil
}

func validateRepoDigestText(value string) error {
	if strings.Count(value, "@") != 1 {
		return errors.New("expected one repository digest separator")
	}
	repository, digest, _ := strings.Cut(value, "@")
	lastSlash := strings.LastIndexByte(repository, '/')
	if strings.LastIndexByte(repository, ':') > lastSlash {
		return errors.New("repository digest cannot include a tag")
	}
	if _, err := policy.CanonicalRepository(repository); err != nil {
		return err
	}
	if !validImageID(digest) {
		return errors.New("digest is not a canonical sha256 image ID")
	}
	return nil
}

func validImageID(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
