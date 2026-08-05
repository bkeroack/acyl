package slacknotifier

import (
  "github.com/bkeroack/acyl/pkg/models"
  "github.com/bkeroack/acyl/pkg/ghclient"
)

type RepoRevisionData = models.RepoRevisionData
type QADestroyReason = models.QADestroyReason

type RepoClient = ghclient.RepoClient
type BranchInfo = ghclient.BranchInfo
type CommitStatus = ghclient.CommitStatus

const (
  DestroyApiRequest = models.DestroyApiRequest
)
