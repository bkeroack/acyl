package ghclient

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/src-d/go-billy.v4/osfs"
	git "gopkg.in/src-d/go-git.v4"

	"github.com/bkeroack/acyl/pkg/memfs"
	billy "gopkg.in/src-d/go-billy.v4"
	gitplumb "gopkg.in/src-d/go-git.v4/plumbing"
	gitcache "gopkg.in/src-d/go-git.v4/plumbing/cache"
	gitobj "gopkg.in/src-d/go-git.v4/plumbing/object"
	gitfs "gopkg.in/src-d/go-git.v4/storage/filesystem"
)

// localTestRepo creates a new repo in memory with the provided branches and returns testing commit hashes
func localTestRepo(t *testing.T, branches []string) (billy.Filesystem, []string) {
	fs := memfs.New()
	if err := fs.MkdirAll("repo", os.ModeDir|os.ModePerm); err != nil {
		t.Fatalf("error in mkdir repo: %v", err)
	}
	fs2, err := fs.Chroot("repo")
	if err != nil {
		t.Fatalf("error in chroot: %v", err)
	}
	if err := fs2.MkdirAll(".git", os.ModeDir|os.ModePerm); err != nil {
		t.Fatalf("error in mkdir .git: %v", err)
	}
	dot, _ := fs2.Chroot(".git")
	repo, err := git.Init(gitfs.NewStorage(dot, gitcache.NewObjectLRUDefault()), fs2)
	if err != nil {
		t.Fatalf("error initializing repo: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("error getting working tree: %v", err)
	}
	fs2.MkdirAll("something", os.ModeDir|os.ModePerm)
	f, err := fs2.Create("something/foo.txt")
	if err != nil {
		t.Fatalf("error creating file 1: %v", err)
	}
	f.Write([]byte(`omg12345`))
	f.Close()
	if _, err := wt.Add("something/foo.txt"); err != nil {
		t.Fatalf("error adding changed file: %v", err)
	}
	h1, err := wt.Commit("first commit", &git.CommitOptions{Author: &gitobj.Signature{Name: "someguy", Email: "asdf@asdf.com", When: time.Now().UTC()}})
	if err != nil {
		t.Fatalf("error commiting 1: %v", err)
	}
	out := []string{h1.String()}
	for i, b := range branches {
		co := &git.CheckoutOptions{Branch: gitplumb.NewBranchReferenceName(b), Create: true}
		if err := wt.Checkout(co); err != nil {
			t.Fatalf("error checking out branch: %v: %v", b, err)
		}
		if i == 0 {
			fs2.MkdirAll("somethingelse", os.ModeDir|os.ModePerm)
			f, err := fs2.Create("somethingelse/bar.txt")
			if err != nil {
				t.Fatalf("error creating file 2: %v", err)
			}
			f.Write([]byte(`qwerty9999`))
			f.Close()
			f, err = fs2.Create("somethingelse/asdf.txt")
			if err != nil {
				t.Fatalf("error creating file 3: %v", err)
			}
			f.Write([]byte(`00000000`))
			f.Close()
			if _, err := wt.Add("somethingelse/"); err != nil {
				t.Fatalf("error adding changed files 2: %v", err)
			}
			h2, err := wt.Commit("another commit", &git.CommitOptions{Author: &gitobj.Signature{Name: "someguy", Email: "asdf@asdf.com", When: time.Now().UTC()}})
			if err != nil {
				t.Fatalf("error commiting 2: %v", err)
			}
			out = append(out, h2.String())
		}
	}
	// add a file but don't commit it
	f, err = fs2.Create("something/bar.txt")
	if err != nil {
		t.Fatalf("error creating extra file: %v", err)
	}
	f.Write([]byte(`asdf`))
	f.Close()
	return fs, out
}

func TestLocalWrapperGetBranches(t *testing.T) {
	fs, _ := localTestRepo(t, []string{"foo", "bar"})
	var backendExecuted bool
	lw := &LocalWrapper{
		FSFunc: func(path string) billy.Filesystem {
			rfs, _ := fs.Chroot(path)
			return rfs
		},
		RepoPathMap: map[string]string{"some/repo": "repo"},
		Backend: &FakeRepoClient{
			GetBranchesFunc: func(context.Context, string) ([]BranchInfo, error) {
				backendExecuted = true
				return []BranchInfo{}, nil
			},
		},
	}
	bl, err := lw.GetBranches(context.Background(), "some/repo")
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if len(bl) != 3 {
		t.Fatalf("bad count: %v", len(bl))
	}
	_, err = lw.GetBranches(context.Background(), "some/other-repo")
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if !backendExecuted {
		t.Fatalf("backend should have been executed")
	}
}

func TestLocalWrapperGetBranch(t *testing.T) {
	fs, _ := localTestRepo(t, []string{"foo", "bar"})
	var backendExecuted bool
	lw := &LocalWrapper{
		FSFunc: func(path string) billy.Filesystem {
			rfs, _ := fs.Chroot(path)
			return rfs
		},
		RepoPathMap: map[string]string{"some/repo": "repo"},
		Backend: &FakeRepoClient{
			GetBranchFunc: func(context.Context, string, string) (BranchInfo, error) {
				backendExecuted = true
				return BranchInfo{}, nil
			},
		},
	}
	bi, err := lw.GetBranch(context.Background(), "some/repo", "foo")
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if bi.Name != "foo" {
		t.Errorf("bad branch name: %v", bi.Name)
	}
	_, err = lw.GetBranch(context.Background(), "some/other-repo", "foo")
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if !backendExecuted {
		t.Fatalf("backend should have been executed")
	}
}

func TestLocalWrapperGetCommitMessage(t *testing.T) {
	fs, commits := localTestRepo(t, []string{"foo", "bar"})
	var backendExecuted bool
	lw := &LocalWrapper{
		FSFunc: func(path string) billy.Filesystem {
			rfs, _ := fs.Chroot(path)
			return rfs
		},
		RepoPathMap: map[string]string{"some/repo": "repo"},
		Backend: &FakeRepoClient{
			GetCommitMessageFunc: func(context.Context, string, string) (string, error) {
				backendExecuted = true
				return "", nil
			},
		},
	}
	msg, err := lw.GetCommitMessage(context.Background(), "some/repo", commits[0])
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if msg != "first commit" {
		t.Errorf("bad commit msg: %v", msg)
	}
	_, err = lw.GetCommitMessage(context.Background(), "some/other-repo", "asdf")
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if !backendExecuted {
		t.Fatalf("backend should have been executed")
	}
}

func TestLocalWrapperGetFileContents(t *testing.T) {
	fs, commits := localTestRepo(t, []string{"foo", "bar"})
	var backendExecuted bool
	lw := &LocalWrapper{
		FSFunc: func(path string) billy.Filesystem {
			rfs, _ := fs.Chroot(path)
			return rfs
		},
		RepoPathMap: map[string]string{"some/repo": "repo"},
		Backend: &FakeRepoClient{
			GetFileContentsFunc: func(context.Context, string, string, string) ([]byte, error) {
				backendExecuted = true
				return nil, nil
			},
		},
	}
	contents, err := lw.GetFileContents(context.Background(), "some/repo", "something/foo.txt", commits[0])
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if string(contents) != "omg12345" {
		t.Errorf("bad contents: %v", string(contents))
	}
	_, err = lw.GetFileContents(context.Background(), "some/other-repo", "something/foo.txt", commits[0])
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if !backendExecuted {
		t.Fatalf("backend should have been executed")
	}
}

func TestLocalWrapperGetFileContentsTriggeringRepo(t *testing.T) {
	fs, commits := localTestRepo(t, []string{"foo", "bar"})
	var backendExecuted bool
	lw := &LocalWrapper{
		WorkingTreeRepos: []string{"some/repo"},
		FSFunc: func(path string) billy.Filesystem {
			rfs, _ := fs.Chroot(path)
			return rfs
		},
		RepoPathMap: map[string]string{"some/repo": "repo"},
		Backend: &FakeRepoClient{
			GetFileContentsFunc: func(context.Context, string, string, string) ([]byte, error) {
				backendExecuted = true
				return nil, nil
			},
		},
	}
	// we should be able to read the uncommitted file
	contents, err := lw.GetFileContents(context.Background(), "some/repo", "something/bar.txt", commits[0])
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if string(contents) != "asdf" {
		t.Errorf("bad contents: %v", string(contents))
	}
	if backendExecuted {
		t.Fatalf("backend should not have been executed")
	}
}

func TestLocalWrapperGetDirectoryContents(t *testing.T) {
	fs, _ := localTestRepo(t, []string{"foo", "bar"})
	var backendExecuted bool
	lw := &LocalWrapper{
		FSFunc: func(path string) billy.Filesystem {
			rfs, _ := fs.Chroot(path)
			return rfs
		},
		RepoPathMap: map[string]string{"some/repo": "repo"},
		Backend: &FakeRepoClient{
			GetDirectoryContentsFunc: func(context.Context, string, string, string) (map[string]FileContents, error) {
				backendExecuted = true
				return nil, nil
			},
		},
	}
	dc, err := lw.GetDirectoryContents(context.Background(), "some/repo", "somethingelse", "foo")
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if len(dc) != 2 {
		t.Fatalf("bad length: %v", len(dc))
	}
	bar, ok := dc["bar.txt"]
	if !ok {
		t.Fatalf("bar.txt not found")
	}
	if string(bar.Contents) != "qwerty9999" {
		t.Fatalf("bad contents for bar: %v", string(bar.Contents))
	}
	asdf, ok := dc["asdf.txt"]
	if !ok {
		t.Fatalf("asdf.txt not found")
	}
	if string(asdf.Contents) != "00000000" {
		t.Fatalf("bad contents for asdf: %v", string(asdf.Contents))
	}
	_, err = lw.GetDirectoryContents(context.Background(), "some/other-repo", "somethingelse", "foo")
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if !backendExecuted {
		t.Fatalf("backend should have been executed")
	}
}

func TestLocalWrapperGetDirectoryContentsTriggeringRepo(t *testing.T) {
	fs, commits := localTestRepo(t, []string{"foo", "bar"})
	var backendExecuted bool
	lw := &LocalWrapper{
		WorkingTreeRepos: []string{"some/repo"},
		FSFunc: func(path string) billy.Filesystem {
			rfs, _ := fs.Chroot(path)
			return rfs
		},
		RepoPathMap: map[string]string{"some/repo": "repo"},
		Backend: &FakeRepoClient{
			GetFileContentsFunc: func(context.Context, string, string, string) ([]byte, error) {
				backendExecuted = true
				return nil, nil
			},
		},
	}
	// we should be able to read the uncommitted files
	dc, err := lw.GetDirectoryContents(context.Background(), "some/repo", "something", commits[0])
	if err != nil {
		t.Fatalf("should have succeeded: %v", err)
	}
	if len(dc) != 2 {
		t.Fatalf("bad length: %v", len(dc))
	}
	foo, ok := dc["foo.txt"]
	if !ok {
		t.Fatalf("foo.txt not found")
	}
	if string(foo.Contents) != "omg12345" {
		t.Fatalf("bad contents for foo: %v", string(foo.Contents))
	}
	bar, ok := dc["bar.txt"]
	if !ok {
		t.Fatalf("bar.txt not found")
	}
	if string(bar.Contents) != "asdf" {
		t.Fatalf("bad contents for bar: %v", string(bar.Contents))
	}
	if backendExecuted {
		t.Fatalf("backend should not have been executed")
	}
}

func TestLocalWrapperThisAcylRepo(t *testing.T) {
	if os.Getenv("TEST_ACYL_REPO") == "" {
		t.SkipNow()
	}
	p, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("error making path absolute: %v", err)
	}
	lw := &LocalWrapper{
		FSFunc:      func(path string) billy.Filesystem { return osfs.New(path) },
		RepoPathMap: map[string]string{"dollarshaveclub/acyl": p},
	}
	bl, err := lw.GetBranches(context.Background(), "dollarshaveclub/acyl")
	if err != nil {
		t.Fatalf("branches should have succeeded: %v", err)
	}
	t.Logf("# branches: %v\n", len(bl))
	// arbitrary commit SHA
	msg, err := lw.GetCommitMessage(context.Background(), "dollarshaveclub/acyl", "516d472b0ae6292fcd6b07350734ca0268747659")
	if err != nil {
		t.Fatalf("commit msg should have succeeded: %v", err)
	}
	if msg != "only load db secrets, fix error msg\n" {
		t.Fatalf("bad commit msg: %v", msg)
	}
	lw.WorkingTreeRepos = []string{"dollarshaveclub/acyl"}
	contents, err := lw.GetDirectoryContents(context.Background(), "dollarshaveclub/acyl", "", "516d472b0ae6292fcd6b07350734ca0268747659")
	if err != nil {
		t.Fatalf("get dir contents should have succeeded: %v", err)
	}
	t.Logf("contents file count: %v", len(contents))
}
