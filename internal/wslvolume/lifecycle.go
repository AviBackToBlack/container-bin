package wslvolume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"github.com/AviBackToBlack/container-bin/internal/wsldocker"
)

type executeFunc func(context.Context, wsldocker.Request) (wsldocker.Response, error)

type engineVolume struct {
	Name   string            `json:"Name"`
	Driver string            `json:"Driver"`
	Labels map[string]string `json:"Labels"`
	Scope  string            `json:"Scope"`
}

// Candidate is untrusted namespace-discovery output. Callers must pass it to
// ProveCandidate before adoption, mutation, backup, or restore.
type Candidate struct {
	name   string
	driver string
	scope  string
	labels map[string]string
}

func (c Candidate) Name() string              { return c.name }
func (c Candidate) Driver() string            { return c.driver }
func (c Candidate) Scope() string             { return c.scope }
func (c Candidate) Labels() map[string]string { return cloneLabels(c.labels) }

// Inspect proves whether the exact constructed local volume currently exists.
// A same-name volume with any different ownership label fails closed.
func Inspect(ctx context.Context, volume Volume) (bool, error) {
	return inspect(ctx, volume, wsldocker.Execute)
}

// Ensure creates the exact local volume only when absent and re-inspects it
// after creation. Docker's create response is never accepted as ownership proof
// by itself.
func Ensure(ctx context.Context, volume Volume) error {
	return ensure(ctx, volume, wsldocker.Execute)
}

// Remove deletes only a volume whose exact constructed identity has first been
// proven. Force removal is deliberately unavailable, and absence is an error.
func Remove(ctx context.Context, volume Volume) error {
	return remove(ctx, volume, wsldocker.Execute)
}

// Discover lists candidates returned by both the namespace-label and name-
// prefix filters. The result is discovery-only and does not establish ownership.
func Discover(ctx context.Context, scope Scope) ([]Candidate, error) {
	return discover(ctx, scope, wsldocker.Execute)
}

func inspect(ctx context.Context, volume Volume, execute executeFunc) (bool, error) {
	if err := validateLifecycleInput(ctx, volume, execute); err != nil {
		return false, err
	}
	observed, exists, err := inspectVolume(ctx, volume, execute)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	if err := requireExactVolume(volume, observed); err != nil {
		return false, err
	}
	return true, nil
}

func ensure(ctx context.Context, volume Volume, execute executeFunc) error {
	if err := validateLifecycleInput(ctx, volume, execute); err != nil {
		return err
	}
	observed, exists, err := inspectVolume(ctx, volume, execute)
	if err != nil {
		return err
	}
	if exists {
		return requireExactVolume(volume, observed)
	}

	body, err := json.Marshal(struct {
		Name   string            `json:"Name"`
		Driver string            `json:"Driver"`
		Labels map[string]string `json:"Labels"`
	}{Name: volume.Name(), Driver: "local", Labels: volume.Labels()})
	if err != nil {
		return fmt.Errorf("encode native WSL volume creation request: %w", err)
	}
	response, err := execute(ctx, wsldocker.Request{
		Method:          http.MethodPost,
		Path:            "/volumes/create",
		Body:            body,
		SuccessStatuses: []int{http.StatusCreated},
	})
	if err != nil {
		return fmt.Errorf("create native WSL volume %s: %w", volume.Name(), err)
	}
	created, err := decodeVolume(response.Body)
	if err != nil {
		return fmt.Errorf("decode created native WSL volume %s: %w", volume.Name(), err)
	}
	if err := requireExactVolume(volume, created); err != nil {
		return fmt.Errorf("validate created native WSL volume: %w", err)
	}

	observed, exists, err = inspectVolume(ctx, volume, execute)
	if err != nil {
		return fmt.Errorf("reinspect created native WSL volume %s: %w", volume.Name(), err)
	}
	if !exists {
		return fmt.Errorf("native WSL volume %s disappeared after creation", volume.Name())
	}
	if err := requireExactVolume(volume, observed); err != nil {
		return fmt.Errorf("revalidate created native WSL volume: %w", err)
	}
	return nil
}

func remove(ctx context.Context, volume Volume, execute executeFunc) error {
	if err := validateLifecycleInput(ctx, volume, execute); err != nil {
		return err
	}
	observed, exists, err := inspectVolume(ctx, volume, execute)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("native WSL volume %s: %w", volume.Name(), fs.ErrNotExist)
	}
	if err := requireExactVolume(volume, observed); err != nil {
		return err
	}
	if _, err := execute(ctx, wsldocker.Request{
		Method:          http.MethodDelete,
		Path:            volumePath(volume.Name()),
		SuccessStatuses: []int{http.StatusNoContent},
	}); err != nil {
		return fmt.Errorf("remove native WSL volume %s without force: %w", volume.Name(), err)
	}
	_, exists, err = inspectVolume(ctx, volume, execute)
	if err != nil {
		return fmt.Errorf("verify native WSL volume %s removal: %w", volume.Name(), err)
	}
	if exists {
		return fmt.Errorf("native WSL volume %s still exists after removal", volume.Name())
	}
	return nil
}

func discover(ctx context.Context, scope Scope, execute executeFunc) ([]Candidate, error) {
	if ctx == nil {
		return nil, errors.New("native WSL volume discovery requires a context")
	}
	if execute == nil {
		return nil, errors.New("native WSL volume lifecycle executor is unavailable")
	}
	if !validNamespace(scope.namespace) || scope.prefix != "cb-"+scope.namespace+"-" {
		return nil, errors.New("native WSL volume discovery requires a valid scope")
	}
	filters, err := json.Marshal(map[string][]string{
		"label": {scope.FilterLabel()},
		"name":  {scope.Prefix()},
	})
	if err != nil {
		return nil, fmt.Errorf("encode native WSL volume discovery filters: %w", err)
	}
	response, err := execute(ctx, wsldocker.Request{
		Method: http.MethodGet,
		Path:   "/volumes",
		Query: url.Values{
			"filters": []string{string(filters)},
		},
		SuccessStatuses: []int{http.StatusOK},
	})
	if err != nil {
		return nil, fmt.Errorf("discover native WSL volumes: %w", err)
	}
	var listed struct {
		Volumes  []engineVolume `json:"Volumes"`
		Warnings []string       `json:"Warnings"`
	}
	if err := json.Unmarshal(response.Body, &listed); err != nil {
		return nil, fmt.Errorf("decode native WSL volume discovery response: %w", err)
	}
	if len(listed.Warnings) != 0 {
		return nil, fmt.Errorf("Docker Desktop returned volume discovery warnings: %q", listed.Warnings)
	}
	seen := make(map[string]bool, len(listed.Volumes))
	candidates := make([]Candidate, 0, len(listed.Volumes))
	for _, observed := range listed.Volumes {
		if err := validateDiscoveredVolume(scope, observed); err != nil {
			return nil, err
		}
		if seen[observed.Name] {
			return nil, fmt.Errorf("Docker Desktop returned duplicate native WSL volume %q", observed.Name)
		}
		seen[observed.Name] = true
		candidates = append(candidates, Candidate{
			name: observed.Name, driver: observed.Driver, scope: observed.Scope, labels: cloneLabels(observed.Labels),
		})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].name < candidates[j].name })
	return candidates, nil
}

func inspectVolume(ctx context.Context, volume Volume, execute executeFunc) (engineVolume, bool, error) {
	response, err := execute(ctx, wsldocker.Request{
		Method:          http.MethodGet,
		Path:            volumePath(volume.Name()),
		SuccessStatuses: []int{http.StatusOK},
	})
	if err != nil {
		var apiError *wsldocker.APIError
		if errors.As(err, &apiError) && apiError.StatusCode == http.StatusNotFound {
			return engineVolume{}, false, nil
		}
		return engineVolume{}, false, fmt.Errorf("inspect native WSL volume %s: %w", volume.Name(), err)
	}
	observed, err := decodeVolume(response.Body)
	if err != nil {
		return engineVolume{}, false, fmt.Errorf("decode native WSL volume %s: %w", volume.Name(), err)
	}
	return observed, true, nil
}

func decodeVolume(raw []byte) (engineVolume, error) {
	var volume engineVolume
	if err := json.Unmarshal(raw, &volume); err != nil {
		return engineVolume{}, err
	}
	if volume.Name == "" || volume.Driver == "" || volume.Scope == "" || volume.Labels == nil {
		return engineVolume{}, errors.New("Docker Desktop volume response is missing required identity fields")
	}
	return volume, nil
}

func requireExactVolume(expected Volume, observed engineVolume) error {
	if observed.Driver != "local" || observed.Scope != "local" {
		return fmt.Errorf("native WSL volume %s must use local driver and scope, got driver %q scope %q", expected.Name(), observed.Driver, observed.Scope)
	}
	if !expected.Matches(observed.Name, observed.Labels) {
		return fmt.Errorf("native WSL volume %s does not match its exact constructed name and ownership labels", expected.Name())
	}
	return nil
}

func validateDiscoveredVolume(scope Scope, observed engineVolume) error {
	if observed.Name == "" || observed.Driver == "" || observed.Scope == "" || observed.Labels == nil {
		return errors.New("Docker Desktop volume discovery returned an incomplete identity")
	}
	if !strings.HasPrefix(observed.Name, scope.Prefix()) || observed.Labels[NamespaceLabel] != scope.Namespace() {
		return fmt.Errorf("Docker Desktop volume discovery returned out-of-scope volume %q", observed.Name)
	}
	for _, value := range []string{observed.Name, observed.Driver, observed.Scope} {
		if strings.TrimSpace(value) != value {
			return fmt.Errorf("Docker Desktop volume discovery returned non-canonical identity %q", value)
		}
		for _, r := range value {
			if unicode.IsControl(r) {
				return fmt.Errorf("Docker Desktop volume discovery returned a control character in identity %q", value)
			}
		}
	}
	return nil
}

func validateLifecycleInput(ctx context.Context, volume Volume, execute executeFunc) error {
	if ctx == nil {
		return errors.New("native WSL volume lifecycle requires a context")
	}
	if execute == nil {
		return errors.New("native WSL volume lifecycle executor is unavailable")
	}
	if err := validateConstructedVolume(volume); err != nil {
		return err
	}
	return nil
}

func validateConstructedVolume(volume Volume) error {
	labels := volume.Labels()
	namespace := labels[NamespaceLabel]
	if !validNamespace(namespace) {
		return errors.New("native WSL volume lifecycle requires a constructed volume identity")
	}
	owner := labels["cb.owner"]
	group, logical, ok := strings.Cut(owner, "/")
	if !ok || strings.ContainsRune(logical, '/') {
		return errors.New("native WSL volume lifecycle requires a constructed volume owner")
	}
	scope := Scope{namespace: namespace, prefix: "cb-" + namespace + "-"}
	var expected Volume
	var err error
	switch labels["cb.kind"] {
	case "shared":
		expected, err = scope.Shared(group, logical)
	case "project":
		expected, err = scope.Project(group, logical, labels["cb.project_path"])
	default:
		return errors.New("native WSL volume lifecycle requires a shared or project volume identity")
	}
	if err != nil || !expected.Matches(volume.Name(), labels) {
		return errors.New("native WSL volume lifecycle requires a valid constructed volume identity")
	}
	return nil
}

func volumePath(name string) string { return "/volumes/" + name }
