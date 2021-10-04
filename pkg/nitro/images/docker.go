package images

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dollarshaveclub/acyl/pkg/eventlogger"
	"github.com/dollarshaveclub/acyl/pkg/ghclient"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/pkg/jsonmessage"
	"github.com/dollarshaveclub/acyl/pkg/persistence"
	"github.com/mholt/archiver"
	"github.com/pkg/errors"
)

type DockerClient interface {
	ImageBuild(ctx context.Context, buildContext io.Reader, options types.ImageBuildOptions) (types.ImageBuildResponse, error)
	ImagePush(ctx context.Context, image string, options types.ImagePushOptions) (io.ReadCloser, error)
}

// DockerBuilderBackend builds images using a Docker Engine
type DockerBuilderBackend struct {
	DC    DockerClient
	RC    ghclient.RepoClient
	DL    persistence.DataLayer
	Auths map[string]types.AuthConfig
	Push  bool
}

var _ BuilderBackend = &DockerBuilderBackend{}

func (dbb *DockerBuilderBackend) log(ctx context.Context, msg string, args ...interface{}) {
	eventlogger.GetLogger(ctx).Printf("docker builder: "+msg, args...)
}

// BuildImage synchronously builds and optionally pushes the image using the Docker Engine, returning when the build completes.
func (dbb *DockerBuilderBackend) BuildImage(ctx context.Context, envName, depName, githubRepo, imageRepo, ref string, ops BuildOptions) error {
	if dbb.DC == nil {
		return errors.New("docker client is nil")
	}
	if dbb.DL == nil {
		return errors.New("datalayer is nil")
	}
	if dbb.RC == nil {
		return errors.New("repo client is nil")
	}
	if ops.DockerfilePath == "" {
		ops.DockerfilePath = "Dockerfile"
	}
	tdir, err := ioutil.TempDir("", "acyl-docker-builder")
	if err != nil {
		return fmt.Errorf("error getting temp dir: %w", err)
	}
	defer os.RemoveAll(tdir)
	dbb.log(ctx, "getting repo contents for %v", githubRepo)
	tgz, err := dbb.RC.GetRepoArchive(ctx, githubRepo, ref)
	if err != nil {
		return fmt.Errorf("error getting repo archive: %w", err)
	}
	defer os.Remove(tgz)
	dbb.log(ctx, "unarchiving repo contents: %v", githubRepo)
	if err := archiver.Unarchive(tgz, tdir); err != nil {
		return fmt.Errorf("error unarchiving repo contents: %w", err)
	}
	// verify that there's exactly one subdirectory in the unarchived contents
	f, err := os.Open(tdir)
	if err != nil {
		return fmt.Errorf("error opening temp dir: %w", err)
	}
	fi, err := f.Readdir(-1)
	f.Close()
	if err != nil {
		return fmt.Errorf("error reading temp dir: %w", err)
	}
	if len(fi) != 1 {
		return fmt.Errorf("expected one path in repo archive but got %v", len(fi))
	}
	if !fi[0].IsDir() {
		return fmt.Errorf("top-level directory in repo not found in unarchived repo archive: %v", fi[0].Name())
	}
	// get all files within the top-level directory
	f, err = os.Open(filepath.Join(tdir, fi[0].Name()))
	if err != nil {
		return fmt.Errorf("error opening top-level repo archive dir: %w", err)
	}
	fi, err = f.Readdir(-1)
	f.Close()
	if err != nil {
		return fmt.Errorf("error reading top-level repo archive dir: %w", err)
	}
	files := make([]string, len(fi))
	for i := range fi {
		files[i] = filepath.Join(f.Name(), fi[i].Name())
	}
	dbb.log(ctx, "building context tar for %v", githubRepo)
	bcontents, err := ioutil.TempFile("", "acyl-docker-builder-context-*.tar")
	if err != nil {
		return fmt.Errorf("error creating tar temp file: %w", err)
	}
	bcontents.Close()
	tar := archiver.NewTar()
	tar.ContinueOnError = true // ignore things like broken symlinks
	tar.OverwriteExisting = true
	if err := tar.Archive(files, bcontents.Name()); err != nil {
		return fmt.Errorf("error writing tar file: %w", err)
	}
	defer os.Remove(bcontents.Name())
	f, err = os.Open(bcontents.Name())
	if err != nil {
		return fmt.Errorf("error opening tar: %w", err)
	}
	defer f.Close()
	bargs := make(map[string]*string, len(ops.BuildArgs))
	for k, v := range ops.BuildArgs {
		v := v
		bargs[k] = &v
	}
	opts := types.ImageBuildOptions{
		Tags:        []string{imageRepo + ":" + ref},
		Remove:      true,
		ForceRemove: true,
		PullParent:  true,
		Dockerfile:  ops.DockerfilePath,
		BuildArgs:   bargs,
		AuthConfigs: dbb.Auths,
	}
	dbb.DL.AddEvent(ctx, envName, fmt.Sprintf("building container: %v:%v", githubRepo, ref))
	dbb.log(ctx, "building image: %v", opts.Tags[0])
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		for _ = range ticker.C {
			dbb.log(ctx, "... still building %v:%v", githubRepo, ref)
		}
	}()
	resp, err := dbb.DC.ImageBuild(ctx, f, opts)
	ticker.Stop()
	if err != nil {
		return fmt.Errorf("error starting image build: %w", err)
	}
	err = handleOutput(resp.Body)
	if err != nil {
		return fmt.Errorf("error performing build: %w", err)
	}
	if dbb.Push {
		rsl := strings.Split(imageRepo, "/")
		var registryURLs []string
		switch len(rsl) {
		case 2: // Docker Hub
			registryURLs = []string{"https://index.docker.io/v1/", "https://index.docker.io/v2/"}
		case 3: // private registry
			registryURLs = []string{"https://" + rsl[0]}
		default:
			return fmt.Errorf("cannot determine base registry URL from %v", imageRepo)
		}
		var auth string
		for _, url := range registryURLs {
			val, ok := dbb.Auths[url]
			if ok {
				j, err := json.Marshal(&val)
				if err != nil {
					return fmt.Errorf("error marshaling auth: %v", err)
				}
				auth = base64.StdEncoding.EncodeToString(j)
			}
		}
		if auth == "" {
			return fmt.Errorf("auth not found for %v", imageRepo)
		}
		opts := types.ImagePushOptions{
			All:          true,
			RegistryAuth: auth,
		}
		dbb.log(ctx, "pushing image: %v", imageRepo+":"+ref)
		ticker = time.NewTicker(5 * time.Second)
		go func() {
			for _ = range ticker.C {
				dbb.log(ctx, "... still pushing %v:%v", githubRepo, ref)
			}
		}()
		resp, err := dbb.DC.ImagePush(ctx, imageRepo+":"+ref, opts)
		ticker.Stop()
		if err != nil {
			return fmt.Errorf("error starting image push: %w", err)
		}
		err = handleOutput(resp)
		if err != nil {
			return fmt.Errorf("error pushing image: %w", err)
		}
		dbb.log(ctx, "image pushed: %v", imageRepo+":"+ref)
	}
	return nil
}

func handleOutput(resp io.ReadCloser) error {
	defer resp.Close()
	return jsonmessage.DisplayJSONMessagesStream(resp, ioutil.Discard, 0, false, nil)
}
