// Package match implements branch matching
package match

import (
	"fmt"

	nitroerrors "github.com/bkeroack/acyl/pkg/nitro/errors"
)

// BranchInfo includes the information about a specific branch of a git repo
type BranchInfo struct {
	Name string
	SHA  string
}

// RepoInfo models the information about parent (triggered) repository
type RepoInfo struct {
	SourceBranch  string
	BaseBranch    string
	BranchMatch   bool
	DefaultBranch string
}

// GetRefForRepo contains the core branch matching algorithm. The input is data about the triggering repository/PR and the extant branches for the repo in question.
// Output is the branch and SHA to use for the repo
func GetRefForRepo(ri RepoInfo, branches []BranchInfo) (sha string, branch string, err error) {
	var hasSourceBranch, hasBaseBranch, hasDefaultBranch bool

	// map of branch name -> SHA
	binfo := map[string]string{}
	for _, bi := range branches {
		binfo[bi.Name] = bi.SHA
	}

	if !ri.BranchMatch {
		branch = ri.DefaultBranch
		if ri.DefaultBranch == "" {
			branch = "master"
		}
		v, ok := binfo[branch]
		if !ok {
			return "", "", nitroerrors.User(fmt.Errorf("branch matching is disabled but repo is missing a '%v' branch", branch))
		}
		return v, branch, nil
	}

	_, hasSourceBranch = binfo[ri.SourceBranch]
	_, hasBaseBranch = binfo[ri.BaseBranch]
	_, hasDefaultBranch = binfo[ri.DefaultBranch]

	if !hasSourceBranch && !hasBaseBranch && !hasDefaultBranch {
		var fb, fl string
		if ri.DefaultBranch != "" {
			fb = ri.DefaultBranch
			fl = "DefaultBranch"
		} else {
			fb = ri.BaseBranch
			fl = "BaseBranch"
		}
		return "", "", nitroerrors.User(fmt.Errorf(`no suitable branch: neither "%v" (SourceBranch) nor "%v" (%v) found`, ri.SourceBranch, fb, fl))
	}

	// order determines precedence
	if hasBaseBranch {
		branch = ri.BaseBranch
	}
	if hasDefaultBranch {
		branch = ri.DefaultBranch
	}
	if hasSourceBranch {
		branch = ri.SourceBranch
	}
	sha = binfo[branch]

	return sha, branch, nil
}

// RefMap is a map of repository name to the ref (branch/sha) to use for that repo
type RefMap map[string]BranchInfo

// RefMap returns a map that can be cast to models.RefMap
func (rm RefMap) RefMap() map[string]string {
	out := map[string]string{}
	for k, v := range rm {
		out[k] = v.Name
	}
	return out
}

// CommitSHAMap returns a map that can be cast to models.CommitSHAMap
func (rm RefMap) CommitSHAMap() map[string]string {
	out := map[string]string{}
	for k, v := range rm {
		out[k] = v.SHA
	}
	return out
}
