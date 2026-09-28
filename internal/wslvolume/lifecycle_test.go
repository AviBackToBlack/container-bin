package wslvolume

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/AviBackToBlack/container-bin/internal/wsldocker"
)

type lifecycleStep struct {
	check    func(wsldocker.Request)
	response wsldocker.Response
	err      error
}

func TestInspectDistinguishesMissingExactAndForeignVolumes(t *testing.T) {
	volume := testSharedVolume(t)
	t.Run("missing", func(t *testing.T) {
		execute := scriptedLifecycle(t, lifecycleStep{
			check: checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
			err:   &wsldocker.APIError{Method: http.MethodGet, Path: volumePath(volume.Name()), StatusCode: http.StatusNotFound},
		})
		exists, err := inspect(context.Background(), volume, execute)
		if err != nil || exists {
			t.Fatalf("inspect() = (%t, %v), want (false, nil)", exists, err)
		}
	})

	t.Run("exact", func(t *testing.T) {
		execute := scriptedLifecycle(t, lifecycleStep{
			check:    checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
			response: wsldocker.Response{StatusCode: http.StatusOK, Body: volumeResponse(t, volume)},
		})
		exists, err := inspect(context.Background(), volume, execute)
		if err != nil || !exists {
			t.Fatalf("inspect() = (%t, %v), want (true, nil)", exists, err)
		}
	})

	t.Run("foreign labels", func(t *testing.T) {
		foreign := volumeResponseValue(volume)
		foreign.Labels["cb.owner"] = "go124/other"
		execute := scriptedLifecycle(t, lifecycleStep{
			check:    checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
			response: wsldocker.Response{StatusCode: http.StatusOK, Body: marshalJSON(t, foreign)},
		})
		if _, err := inspect(context.Background(), volume, execute); err == nil || !strings.Contains(err.Error(), "exact constructed") {
			t.Fatalf("foreign-label inspect error = %v", err)
		}
	})

	t.Run("foreign driver", func(t *testing.T) {
		foreign := volumeResponseValue(volume)
		foreign.Driver = "plugin"
		execute := scriptedLifecycle(t, lifecycleStep{
			check:    checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
			response: wsldocker.Response{StatusCode: http.StatusOK, Body: marshalJSON(t, foreign)},
		})
		if _, err := inspect(context.Background(), volume, execute); err == nil || !strings.Contains(err.Error(), "local driver and scope") {
			t.Fatalf("foreign-driver inspect error = %v", err)
		}
	})
}

func TestEnsureCreatesAndRevalidatesExactVolume(t *testing.T) {
	volume := testSharedVolume(t)
	execute := scriptedLifecycle(t,
		lifecycleStep{
			check: checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
			err:   &wsldocker.APIError{Method: http.MethodGet, Path: volumePath(volume.Name()), StatusCode: http.StatusNotFound},
		},
		lifecycleStep{
			check: func(request wsldocker.Request) {
				checkVolumeRequest(t, http.MethodPost, "/volumes/create")(request)
				if !reflect.DeepEqual(request.SuccessStatuses, []int{http.StatusCreated}) {
					t.Fatalf("create success statuses = %v", request.SuccessStatuses)
				}
				var create struct {
					Name   string            `json:"Name"`
					Driver string            `json:"Driver"`
					Labels map[string]string `json:"Labels"`
				}
				if err := json.Unmarshal(request.Body, &create); err != nil {
					t.Fatal(err)
				}
				if create.Name != volume.Name() || create.Driver != "local" || !reflect.DeepEqual(create.Labels, volume.Labels()) {
					t.Fatalf("create request = %#v", create)
				}
			},
			response: wsldocker.Response{StatusCode: http.StatusCreated, Body: volumeResponse(t, volume)},
		},
		lifecycleStep{
			check:    checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
			response: wsldocker.Response{StatusCode: http.StatusOK, Body: volumeResponse(t, volume)},
		},
	)
	if err := ensure(context.Background(), volume, execute); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureDoesNotAdoptCreateCollision(t *testing.T) {
	volume := testSharedVolume(t)
	foreign := volumeResponseValue(volume)
	foreign.Labels[NamespaceLabel] = "wsl2-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	execute := scriptedLifecycle(t,
		lifecycleStep{
			check: checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
			err:   &wsldocker.APIError{Method: http.MethodGet, Path: volumePath(volume.Name()), StatusCode: http.StatusNotFound},
		},
		lifecycleStep{
			check:    checkVolumeRequest(t, http.MethodPost, "/volumes/create"),
			response: wsldocker.Response{StatusCode: http.StatusCreated, Body: marshalJSON(t, foreign)},
		},
	)
	if err := ensure(context.Background(), volume, execute); err == nil || !strings.Contains(err.Error(), "exact constructed") {
		t.Fatalf("create-collision ensure error = %v", err)
	}
}

func TestEnsureRejectsPostCreateIdentityChange(t *testing.T) {
	volume := testSharedVolume(t)
	foreign := volumeResponseValue(volume)
	foreign.Labels["cb.owner"] = "go124/replaced"
	execute := scriptedLifecycle(t,
		lifecycleStep{
			check: checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
			err:   &wsldocker.APIError{Method: http.MethodGet, Path: volumePath(volume.Name()), StatusCode: http.StatusNotFound},
		},
		lifecycleStep{
			check:    checkVolumeRequest(t, http.MethodPost, "/volumes/create"),
			response: wsldocker.Response{StatusCode: http.StatusCreated, Body: volumeResponse(t, volume)},
		},
		lifecycleStep{
			check:    checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
			response: wsldocker.Response{StatusCode: http.StatusOK, Body: marshalJSON(t, foreign)},
		},
	)
	if err := ensure(context.Background(), volume, execute); err == nil || !strings.Contains(err.Error(), "revalidate created") {
		t.Fatalf("post-create identity change error = %v", err)
	}
}

func TestEnsureExistingExactVolumeIsNoOp(t *testing.T) {
	volume := testSharedVolume(t)
	execute := scriptedLifecycle(t, lifecycleStep{
		check:    checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
		response: wsldocker.Response{StatusCode: http.StatusOK, Body: volumeResponse(t, volume)},
	})
	if err := ensure(context.Background(), volume, execute); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveRequiresExactIdentityAndVerifiesAbsence(t *testing.T) {
	volume := testSharedVolume(t)
	execute := scriptedLifecycle(t,
		lifecycleStep{
			check:    checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
			response: wsldocker.Response{StatusCode: http.StatusOK, Body: volumeResponse(t, volume)},
		},
		lifecycleStep{
			check: func(request wsldocker.Request) {
				checkVolumeRequest(t, http.MethodDelete, volumePath(volume.Name()))(request)
				if len(request.Query) != 0 || len(request.Body) != 0 {
					t.Fatalf("remove request used force/query/body: %#v", request)
				}
			},
			response: wsldocker.Response{StatusCode: http.StatusNoContent},
		},
		lifecycleStep{
			check: checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
			err:   &wsldocker.APIError{Method: http.MethodGet, Path: volumePath(volume.Name()), StatusCode: http.StatusNotFound},
		},
	)
	if err := remove(context.Background(), volume, execute); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveRefusesMissingAndForeignVolumesBeforeDelete(t *testing.T) {
	volume := testSharedVolume(t)
	t.Run("missing", func(t *testing.T) {
		execute := scriptedLifecycle(t, lifecycleStep{
			check: checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
			err:   &wsldocker.APIError{Method: http.MethodGet, Path: volumePath(volume.Name()), StatusCode: http.StatusNotFound},
		})
		if err := remove(context.Background(), volume, execute); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("missing remove error = %v", err)
		}
	})

	t.Run("foreign", func(t *testing.T) {
		foreign := volumeResponseValue(volume)
		delete(foreign.Labels, "cb.managed")
		execute := scriptedLifecycle(t, lifecycleStep{
			check:    checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
			response: wsldocker.Response{StatusCode: http.StatusOK, Body: marshalJSON(t, foreign)},
		})
		if err := remove(context.Background(), volume, execute); err == nil || !strings.Contains(err.Error(), "exact constructed") {
			t.Fatalf("foreign remove error = %v", err)
		}
	})
}

func TestRemoveRejectsNameThatStillExistsAfterDelete(t *testing.T) {
	volume := testSharedVolume(t)
	execute := scriptedLifecycle(t,
		lifecycleStep{
			check:    checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
			response: wsldocker.Response{StatusCode: http.StatusOK, Body: volumeResponse(t, volume)},
		},
		lifecycleStep{
			check:    checkVolumeRequest(t, http.MethodDelete, volumePath(volume.Name())),
			response: wsldocker.Response{StatusCode: http.StatusNoContent},
		},
		lifecycleStep{
			check:    checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
			response: wsldocker.Response{StatusCode: http.StatusOK, Body: volumeResponse(t, volume)},
		},
	)
	if err := remove(context.Background(), volume, execute); err == nil || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("persistent-volume remove error = %v", err)
	}
}

func TestDiscoverUsesNamespaceFiltersAndReturnsSortedUntrustedCandidates(t *testing.T) {
	scope := testScope(t)
	first, err := scope.Shared("go124", "gomodcache")
	if err != nil {
		t.Fatal(err)
	}
	second, err := scope.Project("node24", "node-modules", "/home/alice/project")
	if err != nil {
		t.Fatal(err)
	}
	execute := scriptedLifecycle(t, lifecycleStep{
		check: func(request wsldocker.Request) {
			checkVolumeRequest(t, http.MethodGet, "/volumes")(request)
			if len(request.Query) != 1 || len(request.Query["filters"]) != 1 {
				t.Fatalf("discovery query = %#v", request.Query)
			}
			var filters map[string][]string
			if err := json.Unmarshal([]byte(request.Query.Get("filters")), &filters); err != nil {
				t.Fatal(err)
			}
			want := map[string][]string{"label": {scope.FilterLabel()}, "name": {scope.Prefix()}}
			if !reflect.DeepEqual(filters, want) {
				t.Fatalf("discovery filters = %#v, want %#v", filters, want)
			}
		},
		response: wsldocker.Response{StatusCode: http.StatusOK, Body: marshalJSON(t, map[string]any{
			"Volumes":  []engineVolume{volumeResponseValue(second), volumeResponseValue(first)},
			"Warnings": []string{},
		})},
	})
	candidates, err := discover(context.Background(), scope, execute)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].Name() != first.Name() || candidates[1].Name() != second.Name() {
		t.Fatalf("sorted candidates = %#v", candidates)
	}
	labels := candidates[0].Labels()
	labels["cb.owner"] = "mutated"
	if candidates[0].Labels()["cb.owner"] == "mutated" {
		t.Fatal("Candidate.Labels returned mutable internal state")
	}
}

func TestDiscoverFailsClosedOnWarningsDuplicatesAndFilterViolations(t *testing.T) {
	scope := testScope(t)
	volume := testSharedVolume(t)
	for name, body := range map[string]any{
		"warnings": map[string]any{"Volumes": []engineVolume{}, "Warnings": []string{"partial result"}},
		"duplicates": map[string]any{
			"Volumes": []engineVolume{volumeResponseValue(volume), volumeResponseValue(volume)}, "Warnings": []string{},
		},
		"wrong namespace": map[string]any{
			"Volumes":  []engineVolume{{Name: volume.Name(), Driver: "local", Scope: "local", Labels: map[string]string{NamespaceLabel: "wsl2-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},
			"Warnings": []string{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			execute := scriptedLifecycle(t, lifecycleStep{
				check:    checkVolumeRequest(t, http.MethodGet, "/volumes"),
				response: wsldocker.Response{StatusCode: http.StatusOK, Body: marshalJSON(t, body)},
			})
			if _, err := discover(context.Background(), scope, execute); err == nil {
				t.Fatal("discover() accepted ambiguous response")
			}
		})
	}
}

func TestLifecycleRejectsIncompleteInputsAndResponses(t *testing.T) {
	volume := testSharedVolume(t)
	if _, err := inspect(nil, volume, func(context.Context, wsldocker.Request) (wsldocker.Response, error) {
		return wsldocker.Response{}, nil
	}); err == nil {
		t.Fatal("inspect accepted nil context")
	}
	if _, err := inspect(context.Background(), Volume{}, func(context.Context, wsldocker.Request) (wsldocker.Response, error) {
		return wsldocker.Response{}, nil
	}); err == nil {
		t.Fatal("inspect accepted zero volume")
	}
	execute := scriptedLifecycle(t, lifecycleStep{
		check:    checkVolumeRequest(t, http.MethodGet, volumePath(volume.Name())),
		response: wsldocker.Response{StatusCode: http.StatusOK, Body: []byte(`{"Name":"x"}`)},
	})
	if _, err := inspect(context.Background(), volume, execute); err == nil || !strings.Contains(err.Error(), "missing required") {
		t.Fatalf("incomplete response error = %v", err)
	}
}

func scriptedLifecycle(t *testing.T, steps ...lifecycleStep) executeFunc {
	t.Helper()
	index := 0
	t.Cleanup(func() {
		if index != len(steps) {
			t.Errorf("lifecycle executed %d of %d expected requests", index, len(steps))
		}
	})
	return func(_ context.Context, request wsldocker.Request) (wsldocker.Response, error) {
		t.Helper()
		if index >= len(steps) {
			t.Fatalf("unexpected lifecycle request: %#v", request)
		}
		step := steps[index]
		index++
		if step.check != nil {
			step.check(request)
		}
		return step.response, step.err
	}
}

func checkVolumeRequest(t *testing.T, method, requestPath string) func(wsldocker.Request) {
	t.Helper()
	return func(request wsldocker.Request) {
		t.Helper()
		if request.Method != method || request.Path != requestPath {
			t.Fatalf("request = %s %s, want %s %s", request.Method, request.Path, method, requestPath)
		}
		if len(request.SuccessStatuses) == 0 {
			t.Fatal("request did not declare success statuses")
		}
	}
}

func testSharedVolume(t *testing.T) Volume {
	t.Helper()
	volume, err := testScope(t).Shared("go124", "gomodcache")
	if err != nil {
		t.Fatal(err)
	}
	return volume
}

func volumeResponse(t *testing.T, volume Volume) []byte {
	t.Helper()
	return marshalJSON(t, volumeResponseValue(volume))
}

func volumeResponseValue(volume Volume) engineVolume {
	return engineVolume{Name: volume.Name(), Driver: "local", Scope: "local", Labels: volume.Labels()}
}

func marshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
