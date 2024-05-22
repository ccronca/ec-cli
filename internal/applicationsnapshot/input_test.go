// Copyright The Enterprise Contract Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

//go:build unit

package applicationsnapshot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	gcrfake "github.com/google/go-containerregistry/pkg/v1/fake"
	"github.com/google/go-containerregistry/pkg/v1/types"
	app "github.com/redhat-appstudio/application-api/api/v1alpha1"
	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/enterprise-contract/ec-cli/internal/kubernetes"
	"github.com/enterprise-contract/ec-cli/internal/policy"
	"github.com/enterprise-contract/ec-cli/internal/utils"
	"github.com/enterprise-contract/ec-cli/internal/utils/oci"
	"github.com/enterprise-contract/ec-cli/internal/utils/oci/fake"
)

func Test_DetermineInputSpec(t *testing.T) {
	imageRef := "registry.io/repository/image:tag"
	snapshot := &app.SnapshotSpec{
		Components: []app.SnapshotComponent{
			{
				Name:           "Unnamed",
				ContainerImage: imageRef,
			},
		},
	}
	testJson, _ := json.Marshal(snapshot)
	tests := []struct {
		name  string
		input Input
		want  *app.SnapshotSpec
	}{
		{
			name:  "file",
			input: Input{File: "/home/list-of-images.json"},
			want:  snapshot,
		},
		{
			name:  "inline-json",
			input: Input{JSON: string(testJson)},
			want:  snapshot,
		},
		{
			name:  "image",
			input: Input{Image: imageRef},
			want:  snapshot,
		},
		{
			name:  "snapshot ref",
			input: Input{Snapshot: "namespace/name"},
			want:  snapshot,
		},
		{
			name:  "snapshot ref no namespace",
			input: Input{Snapshot: "just name"},
			want:  snapshot,
		},
		{
			name: "nothing",
			want: nil,
		},
		{
			name:  "snapShotSource as a string",
			input: Input{Images: string(testJson)},
			want:  snapshot,
		},
		{
			name:  "snapShotSource as a file",
			input: Input{Images: "/home/list-of-images.json"},
			want:  snapshot,
		},
		{
			name: "combined (all same)",
			input: Input{
				File:     "/home/list-of-images.json",
				JSON:     string(testJson),
				Image:    imageRef,
				Snapshot: "namespace/name",
			},
			want: snapshot,
		},
		{
			name: "combined (all different)",
			input: Input{
				File:     "/home/list-of-images.json",
				JSON:     `{"components":[{"name": "Named", "containerImage":"registry.io/repository/image:different"}]}`,
				Image:    "registry.io/repository/image:another",
				Snapshot: "namespace/name",
			},
			want: &app.SnapshotSpec{
				Components: []app.SnapshotComponent{
					snapshot.Components[0],
					{
						Name:           "Named",
						ContainerImage: "registry.io/repository/image:different",
					},
					{
						Name:           "Unnamed",
						ContainerImage: "registry.io/repository/image:another",
					},
				},
			},
		},
		{
			name: "combined (some different)",
			input: Input{
				File:  "/home/list-of-images.json",
				JSON:  `{"components":[{"name": "Named", "containerImage":"` + imageRef + `"},{"name": "Set name", "containerImage":"registry.io/repository/image:another"}]}`,
				Image: "registry.io/repository/image:another",
			},
			want: &app.SnapshotSpec{
				Components: []app.SnapshotComponent{
					{
						Name:           "Named",
						ContainerImage: imageRef,
					},
					{
						Name:           "Set name",
						ContainerImage: "registry.io/repository/image:another",
					},
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			ctx := utils.WithFS(context.Background(), fs)
			ctx = kubernetes.WithClient(ctx, &policy.FakeKubernetesClient{
				Snapshot: *snapshot,
			})

			client := fake.FakeClient{}
			// TODO: Replace mock.Anything calls with specific values
			client.On("Head", mock.Anything).Return(&v1.Descriptor{MediaType: types.OCIManifestSchema1}, nil)
			ctx = oci.WithClient(ctx, &client)

			if tc.input.File != "" {
				if err := afero.WriteFile(fs, tc.input.File, []byte(testJson), 0400); err != nil {
					panic(err)
				}
			}

			if tc.input.Images == "/home/list-of-images.json" {
				if err := afero.WriteFile(fs, tc.input.Images, []byte(testJson), 0400); err != nil {
					panic(err)
				}
			}
			got, err := DetermineInputSpec(ctx, tc.input)
			// expect an error so check for nil
			if tc.want != nil {
				assert.NoError(t, err)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestReadSnapshotFile(t *testing.T) {
	t.Run("Successful file read and unmarshal", func(t *testing.T) {
		snapshotSpec := app.SnapshotSpec{
			Components: []app.SnapshotComponent{
				{
					Name:           "Named",
					ContainerImage: "",
				},
				{
					Name:           "Set name",
					ContainerImage: "registry.io/repository/image:another",
				},
			},
		}
		fs := afero.NewMemMapFs()
		spec := `{"components":[{"name": "Named", "containerImage":""},{"name": "Set name", "containerImage":"registry.io/repository/image:another"}]}`

		err := afero.WriteFile(fs, "/correct.json", []byte(spec), 0644)
		if err != nil {
			t.Fatalf("Setup failure: could not write file: %v", err)
		}

		content, err := afero.ReadFile(fs, "/correct.json")
		assert.NoError(t, err)
		got, err := readSnapshotSource(content)
		assert.Equal(t, snapshotSpec, got)
		assert.NoError(t, err)
	})

	t.Run("Invalid Spec", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		spec := `bad spec`
		specFile := "/badSpec.json"

		err := afero.WriteFile(fs, specFile, []byte(spec), 0644)
		if err != nil {
			t.Fatalf("Setup failure: could not write file: %v", err)
		}

		content, err := afero.ReadFile(fs, specFile)
		assert.NoError(t, err)
		_, err = readSnapshotSource(content)
		wrapped := errors.New("error unmarshaling JSON: while decoding JSON: json: cannot unmarshal string into Go value of type v1alpha1.SnapshotSpec")
		expected := fmt.Errorf("unable to parse Snapshot specification from %s: %w", spec, wrapped)
		assert.Error(t, err, expected)
	})
}

func TestExpandImageIndex(t *testing.T) {
	client := fake.FakeClient{}
	expectedRef := name.MustParseReference("registry.io/repository/image:tag")
	client.On("Head", expectedRef).Return(&v1.Descriptor{MediaType: types.OCIImageIndex}, nil)

	index := gcrfake.FakeImageIndex{}
	index.IndexManifestReturns(&v1.IndexManifest{
		Manifests: []v1.Descriptor{
			{
				MediaType: types.OCIManifestSchema1,
				Platform:  &v1.Platform{Architecture: "amd64"},
				Digest:    v1.Hash{Algorithm: "sha256", Hex: "digest1"},
			},
			{
				MediaType: types.OCIManifestSchema1,
				Platform:  &v1.Platform{Architecture: "arm64"},
				Digest:    v1.Hash{Algorithm: "sha256", Hex: "digest2"},
			},
		},
	}, nil)

	client.On("Index", expectedRef).Return(&index, nil)

	ctx := oci.WithClient(context.Background(), &client)

	snap := &app.SnapshotSpec{
		Components: []app.SnapshotComponent{
			{
				Name:           "some-image-name",
				ContainerImage: "registry.io/repository/image:tag",
			},
		},
	}

	expandImageIndex(ctx, snap)
	assert.True(t, len(snap.Components) == 2, "Image Index itself should be removed and be replaced by individual image manifests")

	amd64Image, arm64Image := false, false
	for _, archImage := range snap.Components {
		switch {
		case strings.Contains(archImage.Name, "some-image-name-sha256:digest1-amd64"):
			amd64Image = true
		case strings.Contains(archImage.Name, "some-image-name-sha256:digest2-arm64"):
			arm64Image = true
		}
	}

	assert.True(t, amd64Image, "An amd64 image should be present in the component")
	assert.True(t, arm64Image, "An arm64 image should be present in the component")
}

func TestExpandImageImage_Errors(t *testing.T) {
	imagePullspec := "registry.io/repository/image:tag"
	expectedRef, _ := name.ParseReference(imagePullspec)
	tests := []struct {
		name     string
		client   func(*fake.FakeClient)
		imageRef string
		want     string
	}{
		{
			name:     "ParseReference error",
			client:   func(c *fake.FakeClient) {},
			imageRef: "",
			want:     "unable to parse container image",
		},
		{
			name: "remote.Get error",
			client: func(c *fake.FakeClient) {
				c.On("Head", expectedRef).Return(nil, fmt.Errorf("fetch failed"))
			},
			imageRef: imagePullspec,
			want:     "unable to fetch descriptior for container image",
		},
		{
			name: "error fetching the index",
			client: func(c *fake.FakeClient) {
				c.On("Head", expectedRef).Return(&v1.Descriptor{MediaType: types.OCIImageIndex}, nil)
				c.On("Index", expectedRef).Return(nil, fmt.Errorf("fetch index failed"))
			},
			imageRef: imagePullspec,
			want:     "unable to fetch index for container image",
		},
		{
			name: "error fetching the index manifests",
			client: func(c *fake.FakeClient) {
				c.On("Head", expectedRef).Return(&v1.Descriptor{MediaType: types.OCIImageIndex}, nil)
				index := gcrfake.FakeImageIndex{}
				index.IndexManifestReturns(nil, fmt.Errorf("failed to get IndexManifest"))
				c.On("Index", expectedRef).Return(&index, nil)
			},
			imageRef: imagePullspec,
			want:     "unable to fetch index manifest for container image",
		},
	}

	fs := afero.NewMemMapFs()
	ctx := utils.WithFS(context.Background(), fs)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logger, hook := test.NewNullLogger()
			logger.SetLevel(log.WarnLevel)
			log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
			log.AddHook(hook)

			client := fake.FakeClient{}
			tc.client(&client)
			ctx := oci.WithClient(ctx, &client)
			snapshot := &app.SnapshotSpec{
				Components: []app.SnapshotComponent{
					{
						Name:           "Unnamed",
						ContainerImage: tc.imageRef,
					},
				},
			}
			expandImageIndex(ctx, snapshot)

			found := false
			for _, entry := range hook.AllEntries() {
				if strings.Contains(entry.Message, tc.want) {
					found = true
					break
				}
			}
			assert.True(t, found, "Error message should have the pre-defined string", tc.want)
		})
		// Clear the hooks set by the last test
		log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	}
}
