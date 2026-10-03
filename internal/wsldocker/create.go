package wsldocker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxContainerCreateOutput = 64 << 10
	containerRollbackTimeout = 30 * time.Second
	containerNamespaceLabel  = "cb.wsl_namespace"
	containerRunIDLabel      = "cb.run_id"
	containerToolLabel       = "cb.tool"
)

// ContainerMount is one explicit bind or named-volume mount admitted to a
// native WSL container creation request.
type ContainerMount struct {
	Type     string `json:"Type"`
	Source   string `json:"Source"`
	Target   string `json:"Target"`
	ReadOnly bool   `json:"ReadOnly"`
	// VolumeLabels is the complete expected ownership identity for a named
	// volume. It is proof metadata and is never sent in the container request.
	VolumeLabels map[string]string `json:"-"`
}

// ContainerCreateSpec is the already-authorized runtime configuration for one
// native WSL tool invocation. It contains no endpoint or privilege controls.
type ContainerCreateSpec struct {
	Tool             string
	Namespace        string
	Image            string
	Command          []string
	Environment      []string
	WorkingDirectory string
	Mounts           []ContainerMount
	TTY              bool
}

// Container is the immutable identity of one container created and
// re-inspected through the proof-bound Docker Desktop WSL transport.
type Container struct {
	id        string
	runID     string
	namespace string
	tool      string
}

func (c Container) ID() string        { return c.id }
func (c Container) RunID() string     { return c.runID }
func (c Container) Namespace() string { return c.namespace }
func (c Container) Tool() string      { return c.tool }

type createDependencies struct {
	operations        operationDependencies
	newRunID          func() (string, error)
	resolveBindSource func(string) (string, error)
}

type containerCreateBody struct {
	AttachStdin  bool              `json:"AttachStdin"`
	AttachStdout bool              `json:"AttachStdout"`
	AttachStderr bool              `json:"AttachStderr"`
	TTY          bool              `json:"Tty"`
	OpenStdin    bool              `json:"OpenStdin"`
	StdinOnce    bool              `json:"StdinOnce"`
	Environment  []string          `json:"Env,omitempty"`
	Command      []string          `json:"Cmd,omitempty"`
	Image        string            `json:"Image"`
	Labels       map[string]string `json:"Labels"`
	WorkingDir   string            `json:"WorkingDir"`
	HostConfig   struct {
		AutoRemove bool             `json:"AutoRemove"`
		Mounts     []ContainerMount `json:"Mounts,omitempty"`
	} `json:"HostConfig"`
}

func createContainer(ctx context.Context, spec ContainerCreateSpec, deps createDependencies) (Container, error) {
	if ctx == nil {
		return Container{}, errors.New("Docker Desktop WSL container create requires a context")
	}
	if err := validateContainerCreateSpec(spec); err != nil {
		return Container{}, fmt.Errorf("Docker Desktop WSL container create: %w", err)
	}
	if deps.operations.check == nil || deps.operations.statSocket == nil || deps.operations.perform == nil || deps.newRunID == nil || deps.resolveBindSource == nil {
		return Container{}, errors.New("Docker Desktop WSL container create dependencies are incomplete")
	}
	if err := proveContainerMounts(ctx, spec.Mounts, deps.operations, deps.resolveBindSource); err != nil {
		return Container{}, fmt.Errorf("prove Docker mounts before container creation: %w", err)
	}
	runID, err := deps.newRunID()
	if err != nil {
		return Container{}, fmt.Errorf("generate Docker Desktop WSL container run identity: %w", err)
	}
	if !validRunID(runID) {
		return Container{}, errors.New("generated Docker Desktop WSL container run identity is invalid")
	}

	container := Container{runID: runID, namespace: spec.Namespace, tool: spec.Tool}
	body := containerCreateBody{
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		TTY:          spec.TTY,
		OpenStdin:    true,
		StdinOnce:    true,
		Environment:  append([]string(nil), spec.Environment...),
		Command:      append([]string(nil), spec.Command...),
		Image:        spec.Image,
		Labels:       containerLabels(container),
		WorkingDir:   spec.WorkingDirectory,
	}
	body.HostConfig.AutoRemove = true
	body.HostConfig.Mounts = append([]ContainerMount(nil), spec.Mounts...)
	raw, err := json.Marshal(body)
	if err != nil {
		return Container{}, fmt.Errorf("encode Docker Desktop WSL container creation request: %w", err)
	}
	response, err := execute(ctx, Request{
		Method:          http.MethodPost,
		Path:            "/containers/create",
		Body:            raw,
		SuccessStatuses: []int{http.StatusCreated},
	}, deps.operations)
	if err != nil {
		return Container{}, err
	}

	var created struct {
		ID       string   `json:"Id"`
		Warnings []string `json:"Warnings"`
	}
	if len(response.Body) > maxContainerCreateOutput {
		return Container{}, fmt.Errorf("Docker container create response exceeds %d bytes", maxContainerCreateOutput)
	}
	if err := json.Unmarshal(response.Body, &created); err != nil {
		return Container{}, fmt.Errorf("decode Docker container create response: %w", err)
	}
	if err := validateContainerID(created.ID); err != nil {
		return Container{}, fmt.Errorf("Docker container create returned invalid ID: %w", err)
	}
	container.id = created.ID
	if len(created.Warnings) != 0 {
		return Container{}, rollbackCreatedContainer(ctx, container, deps.operations,
			fmt.Errorf("Docker container create returned %d warning(s)", len(created.Warnings)))
	}
	snapshot, err := inspectContainer(ctx, container.id, deps.operations)
	if err != nil {
		return Container{}, rollbackCreatedContainer(ctx, container, deps.operations,
			fmt.Errorf("inspect created Docker container: %w", err))
	}
	if err := requireOwnedContainer(container, snapshot); err != nil {
		return Container{}, rollbackCreatedContainer(ctx, container, deps.operations,
			fmt.Errorf("validate created Docker container: %w", err))
	}
	if snapshot.Running() {
		return Container{}, rollbackCreatedContainer(ctx, container, deps.operations,
			errors.New("created Docker container is already running"))
	}
	if snapshot.TTY() != spec.TTY || !snapshot.AttachStdin() || !snapshot.AttachStdout() || !snapshot.AttachStderr() || !snapshot.OpenStdin() || !snapshot.StdinOnce() || !snapshot.AutoRemove() {
		return Container{}, rollbackCreatedContainer(ctx, container, deps.operations,
			errors.New("created Docker container stdio, terminal or auto-remove configuration does not match the request"))
	}
	return container, nil
}

func rollbackCreatedContainer(ctx context.Context, container Container, operations operationDependencies, cause error) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), containerRollbackTimeout)
	defer cancel()
	if err := removeContainer(cleanupCtx, container, operations); err != nil {
		return fmt.Errorf("%w; rollback of created container %s also failed: %v", cause, container.id, err)
	}
	return cause
}

func newContainerRunID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func containerLabels(container Container) map[string]string {
	return map[string]string{
		"cb.managed":            "true",
		"cb.kind":               "run",
		containerNamespaceLabel: container.namespace,
		containerRunIDLabel:     container.runID,
		containerToolLabel:      container.tool,
	}
}

func requireOwnedContainer(expected Container, observed ContainerSnapshot) error {
	if observed.ID() != expected.id {
		return errors.New("Docker container ID does not match the created identity")
	}
	labels := observed.Labels()
	for key, value := range containerLabels(expected) {
		if labels[key] != value {
			return fmt.Errorf("Docker container label %s=%q, expected %q", key, labels[key], value)
		}
	}
	return nil
}

func validateContainerCreateSpec(spec ContainerCreateSpec) error {
	if !validRuntimeName(spec.Tool) {
		return fmt.Errorf("invalid tool name %q", spec.Tool)
	}
	if !validWSLNamespace(spec.Namespace) {
		return fmt.Errorf("invalid native WSL namespace %q", spec.Namespace)
	}
	if err := validateImageReference(spec.Image); err != nil {
		return err
	}
	if err := validateContainerPath(spec.WorkingDirectory); err != nil {
		return fmt.Errorf("invalid working directory: %w", err)
	}
	for index, argument := range spec.Command {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, '\x00') {
			return fmt.Errorf("command argument %d is not valid UTF-8 or contains NUL", index)
		}
	}
	seenEnvironment := make(map[string]bool, len(spec.Environment))
	for _, assignment := range spec.Environment {
		name, _, ok := strings.Cut(assignment, "=")
		if !ok || !utf8.ValidString(assignment) || !validEnvironmentName(name) || strings.ContainsRune(assignment, '\x00') {
			return fmt.Errorf("invalid environment assignment %q", assignment)
		}
		if seenEnvironment[name] {
			return fmt.Errorf("duplicate environment assignment for %s", name)
		}
		seenEnvironment[name] = true
	}
	seenTargets := make(map[string]bool, len(spec.Mounts))
	bindMounts := 0
	for index, mount := range spec.Mounts {
		if err := validateContainerMount(mount, spec.Namespace); err != nil {
			return fmt.Errorf("invalid mount %d: %w", index, err)
		}
		if mount.Type == "bind" {
			bindMounts++
			if bindMounts > 1 {
				return errors.New("native WSL container creation permits at most one project bind mount")
			}
		}
		if seenTargets[mount.Target] {
			return fmt.Errorf("duplicate mount target %s", mount.Target)
		}
		seenTargets[mount.Target] = true
	}
	return nil
}

func validateContainerMount(mount ContainerMount, namespace string) error {
	if err := validateContainerPath(mount.Target); err != nil {
		return fmt.Errorf("target: %w", err)
	}
	switch mount.Type {
	case "bind":
		if err := validateContainerPath(mount.Source); err != nil {
			return fmt.Errorf("bind source: %w", err)
		}
		if bindSourceContainsDockerSocket(mount.Source) || mount.Target == DockerSocketPath || mount.Target == "/run/docker.sock" {
			return errors.New("Docker socket bind mounts are forbidden")
		}
	case "volume":
		if !validDockerObjectName(mount.Source) {
			return fmt.Errorf("invalid named-volume source %q", mount.Source)
		}
		if !strings.HasPrefix(mount.Source, "cb-"+namespace+"-") {
			return fmt.Errorf("named-volume source %q is outside the native WSL namespace", mount.Source)
		}
		if err := validateVolumeLabels(mount.VolumeLabels, namespace); err != nil {
			return fmt.Errorf("named-volume ownership labels: %w", err)
		}
	default:
		return fmt.Errorf("unsupported type %q", mount.Type)
	}
	return nil
}

func bindSourceContainsDockerSocket(source string) bool {
	for _, socketPath := range []string{DockerSocketPath, "/run/docker.sock"} {
		if source == socketPath || strings.HasPrefix(socketPath, source+"/") {
			return true
		}
	}
	return false
}

func validateVolumeLabels(labels map[string]string, namespace string) error {
	if labels == nil || labels["cb.managed"] != "true" || labels[containerNamespaceLabel] != namespace {
		return errors.New("exact managed namespace identity is required")
	}
	if labels["cb.kind"] != "shared" && labels["cb.kind"] != "project" {
		return errors.New("managed volume kind must be shared or project")
	}
	group, logical, ok := strings.Cut(labels["cb.owner"], "/")
	if !ok || strings.ContainsRune(logical, '/') || !validRuntimeName(group) || !validRuntimeName(logical) {
		return errors.New("managed volume owner is invalid")
	}
	for key, value := range labels {
		if key == "" || !utf8.ValidString(key) || !utf8.ValidString(value) || strings.ContainsRune(key, '\x00') || strings.ContainsRune(value, '\x00') {
			return errors.New("managed volume labels contain an invalid key or value")
		}
	}
	return nil
}

func proveContainerMounts(ctx context.Context, mounts []ContainerMount, operations operationDependencies, resolveBindSource func(string) (string, error)) error {
	for _, mount := range mounts {
		if mount.Type == "bind" {
			resolved, err := resolveBindSource(mount.Source)
			if err != nil {
				return fmt.Errorf("resolve bind source %s: %w", mount.Source, err)
			}
			if resolved != mount.Source {
				return fmt.Errorf("bind source %s resolves to %s; symlinked bind roots are forbidden", mount.Source, resolved)
			}
			if bindSourceContainsDockerSocket(resolved) {
				return fmt.Errorf("bind source %s contains the Docker socket", mount.Source)
			}
			continue
		}
		if mount.Type != "volume" {
			continue
		}
		response, err := execute(ctx, Request{
			Method:          http.MethodGet,
			Path:            "/volumes/" + mount.Source,
			SuccessStatuses: []int{http.StatusOK},
		}, operations)
		if err != nil {
			return fmt.Errorf("inspect exact volume %s: %w", mount.Source, err)
		}
		var observed struct {
			Name   string            `json:"Name"`
			Driver string            `json:"Driver"`
			Scope  string            `json:"Scope"`
			Labels map[string]string `json:"Labels"`
		}
		if err := json.Unmarshal(response.Body, &observed); err != nil {
			return fmt.Errorf("decode exact volume %s: %w", mount.Source, err)
		}
		if observed.Name != mount.Source || observed.Driver != "local" || observed.Scope != "local" || !reflect.DeepEqual(observed.Labels, mount.VolumeLabels) {
			return fmt.Errorf("volume %s does not match its exact local identity and ownership labels", mount.Source)
		}
	}
	return nil
}

func validateContainerPath(value string) error {
	if value == "" || !utf8.ValidString(value) || !path.IsAbs(value) || path.Clean(value) != value || value == "/" || strings.ContainsRune(value, '\\') {
		return errors.New("path must be a canonical absolute non-root Linux path")
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return errors.New("path contains a control character")
		}
	}
	return nil
}

func validateImageReference(value string) error {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return errors.New("image reference is empty, oversized, or not canonical")
	}
	for _, char := range value {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return errors.New("image reference contains whitespace or a control character")
		}
	}
	return nil
}

func validEnvironmentName(value string) bool {
	if value == "" {
		return false
	}
	for index, char := range value {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || char == '_' || (index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}

func validRuntimeName(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func validWSLNamespace(value string) bool {
	if len(value) != len("wsl2-")+32 || !strings.HasPrefix(value, "wsl2-") {
		return false
	}
	return validLowerHex(value[len("wsl2-"):])
}

func validRunID(value string) bool { return len(value) == 32 && validLowerHex(value) }

func validLowerHex(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validDockerObjectName(value string) bool {
	if value == "" || len(value) > 255 {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || (index > 0 && (char == '_' || char == '.' || char == '-')) {
			continue
		}
		return false
	}
	return true
}
