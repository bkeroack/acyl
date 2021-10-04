package images

import (
	"bytes"
	"context"
	"io"
	"io/ioutil"
	"os"
	"testing"

	"github.com/pkg/errors"

	"github.com/dollarshaveclub/acyl/pkg/ghclient"
	"github.com/dollarshaveclub/acyl/pkg/persistence"

	"github.com/docker/docker/api/types"
)

type fakeDockerClient struct {
	ImageBuildFunc func(ctx context.Context, buildContext io.Reader, options types.ImageBuildOptions) (types.ImageBuildResponse, error)
	ImagePushFunc  func(ctx context.Context, image string, options types.ImagePushOptions) (io.ReadCloser, error)
}

func (fdc *fakeDockerClient) ImageBuild(ctx context.Context, buildContext io.Reader, options types.ImageBuildOptions) (types.ImageBuildResponse, error) {
	if fdc.ImageBuildFunc != nil {
		return fdc.ImageBuildFunc(ctx, buildContext, options)
	}
	return types.ImageBuildResponse{Body: ioutil.NopCloser(&bytes.Buffer{})}, nil
}
func (fdc *fakeDockerClient) ImagePush(ctx context.Context, image string, options types.ImagePushOptions) (io.ReadCloser, error) {
	if fdc.ImagePushFunc != nil {
		return fdc.ImagePushFunc(ctx, image, options)
	}
	return ioutil.NopCloser(&bytes.Buffer{}), nil
}

func TestDockerBackendBuild(t *testing.T) {
	var tname string
	createtf := func() {
		tf, err := ioutil.TempFile("", "*.tar.gz")
		if err != nil {
			t.Fatalf("error creating temp file: %v", err)
		}
		defer tf.Close()
		f, err := os.Open("testdata/contents.tar.gz")
		if err != nil {
			t.Fatalf("error opening contents tar: %v", err)
		}
		defer f.Close()
		if _, err := io.Copy(tf, f); err != nil {
			t.Fatalf("error copying contents tar: %v", err)
		}
		tname = tf.Name()
	}
	rc := &ghclient.FakeRepoClient{
		GetRepoArchiveFunc: func(ctx context.Context, repo, ref string) (string, error) {
			return tname, nil
		},
	}
	var builderr, pusherr bool
	var built, pushed bool
	dbb := DockerBuilderBackend{
		DC: &fakeDockerClient{
			ImageBuildFunc: func(ctx context.Context, buildContext io.Reader, options types.ImageBuildOptions) (types.ImageBuildResponse, error) {
				built = true
				if builderr {
					return types.ImageBuildResponse{}, errors.New("build failure")
				}
				return types.ImageBuildResponse{Body: ioutil.NopCloser(&bytes.Buffer{})}, nil
			},
			ImagePushFunc: func(ctx context.Context, image string, options types.ImagePushOptions) (io.ReadCloser, error) {
				pushed = true
				if pusherr {
					return nil, errors.New("push failure")
				}
				return ioutil.NopCloser(&bytes.Buffer{}), nil
			},
		},
		DL: persistence.NewFakeDataLayer(),
		RC: rc,
		Auths: map[string]types.AuthConfig{
			"https://quay.io": types.AuthConfig{},
		},
		Push: false,
	}
	createtf()
	defer os.Remove(tname)
	err := dbb.BuildImage(context.Background(), "some-name", "acme-widgets", "acme/widgets", "quay.io/acme/widgets", "asdf", BuildOptions{})
	if err != nil {
		t.Fatalf("build should have succeeded: %v", err)
	}
	if !built {
		t.Fatalf("ImageBuild should have been called")
	}
	if pushed {
		t.Fatalf("ImagePush shouldn't have been called")
	}
	createtf()
	defer os.Remove(tname)
	builderr = true
	err = dbb.BuildImage(context.Background(), "some-name", "acme-widgets", "acme/widgets", "quay.io/acme/widgets", "asdf", BuildOptions{})
	if err == nil {
		t.Fatalf("build should have failed")
	}
	createtf()
	defer os.Remove(tname)
	built = false
	builderr = false
	dbb.Push = true
	err = dbb.BuildImage(context.Background(), "some-name", "acme-widgets", "acme/widgets", "quay.io/acme/widgets", "asdf", BuildOptions{})
	if err != nil {
		t.Fatalf("build should have succeeded: %v", err)
	}
	if !built {
		t.Fatalf("ImageBuild should have been called")
	}
	if !pushed {
		t.Fatalf("ImagePush should have have been called")
	}
	createtf()
	defer os.Remove(tname)
	pusherr = true
	err = dbb.BuildImage(context.Background(), "some-name", "acme-widgets", "acme/widgets", "quay.io/acme/widgets", "asdf", BuildOptions{})
	if err == nil {
		t.Fatalf("build should have failed")
	}
	createtf()
	defer os.Remove(tname)
	err = dbb.BuildImage(context.Background(), "some-name", "acme-widgets", "acme/widgets", "privateregistry.io/acme/widgets", "asdf", BuildOptions{})
	if err == nil {
		t.Fatalf("build should have failed with missing auth")
	}
}
