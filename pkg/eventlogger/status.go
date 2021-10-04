package eventlogger

import (
	"time"

	"github.com/dollarshaveclub/acyl/pkg/models"
	"github.com/dollarshaveclub/metahelm/pkg/metahelm"
	guuid "github.com/gofrs/uuid"
)

// SetNewStatus creates a new empty event status of etype with only config.type, config.status and config.started set. This is intended to be called first and prior to config processing.
func (l *Logger) SetNewStatus(etype models.EventStatusType, envName string, rrd models.RepoRevisionData) {
	summary := models.EventStatusSummary{
		Config: models.EventStatusSummaryConfig{
			Type:           etype,
			Status:         models.PendingStatus,
			EnvName:        envName,
			TriggeringRepo: rrd.Repo,
			PullRequest:    rrd.PullRequest,
			GitHubUser:     rrd.User,
			Branch:         rrd.SourceBranch,
			Revision:       rrd.SourceSHA,
			Started:        time.Now().UTC(),
		},
	}
	if err := l.DL.SetEventStatus(l.ID, summary); err != nil {
		l.Printf("error setting event status in db: %v", err)
	}
}

// SetInitialStatus sets up the initial progress tree and values according to the processed config, and records the time it took to do the config processing. This should be called no more than once per event.
func (l *Logger) SetInitialStatus(rc *models.RepoConfig, processingTime time.Duration) {
	if rc == nil {
		l.Printf("error setting initial status: rc is nil")
		return
	}
	rm, err := rc.RefMap()
	if err != nil {
		l.Printf("error setting initial status: error getting refmap: %v", err)
		return
	}

	if err := l.DL.SetEventStatusConfig(l.ID, processingTime, rm); err != nil {
		l.Printf("error setting event status config: %v", err)
		return
	}

	tree := make(map[string]models.EventStatusTreeNode, rc.Dependencies.Count()+1)

	tree[models.GetName(rc.Application.Repo)] = models.EventStatusTreeNode{
		Chart: models.EventStatusTreeNodeChart{
			Status: models.WaitingChartStatus,
		},
		Image: models.EventStatusTreeNodeImage{
			Name: rc.Application.Image,
		},
	}
	reqmap := map[string]string{}
	for _, dep := range rc.Dependencies.All() {
		for _, r := range dep.Requires {
			reqmap[r] = dep.Name
		}
	}
	for _, dep := range rc.Dependencies.All() {
		p := dep.Parent
		if r, ok := reqmap[dep.Name]; ok {
			p = r
		}
		if p == "" {
			p = models.GetName(rc.Application.Repo)
		}
		node := models.EventStatusTreeNode{
			Parent: p,
			Chart: models.EventStatusTreeNodeChart{
				Status: models.WaitingChartStatus,
			},
		}
		if dep.Repo != "" {
			node.Image.Name = dep.AppMetadata.Image
		}
		tree[dep.Name] = node
	}
	if err := l.DL.SetEventStatusTree(l.ID, tree); err != nil {
		l.Printf("error setting event status tree: %v", err)
	}
}

func (l *Logger) SetK8sNamespace(ns string) {
	if err := l.DL.SetEventStatusConfigK8sNS(l.ID, ns); err != nil {
		l.Printf("error setting config k8s namespace: %v: %v", ns, err)
	}
}

// SetImageStarted marks the image build for the named dependency to started (name is assumed to exist)
func (l *Logger) SetImageStarted(name string) {
	if err := l.DL.SetEventStatusImageStarted(l.ID, name); err != nil {
		l.Printf("error setting image status to started: %v: %v", name, err)
	}
}

// SetImageBuildID sets the Furan image build ID
func (l *Logger) SetImageBuildID(name string, furanBuildID guuid.UUID) {
	if err := l.DL.SetEventStatusImageBuildID(l.ID, name, furanBuildID); err != nil {
		l.Printf("error setting image build id: %v: %v", name, err)
	}
}

// SetImageStarted marks the image build for the named dependency to completed (name is assumed to exist) and optionally with error if the build failed
func (l *Logger) SetImageCompleted(name string, err bool) {
	if err2 := l.DL.SetEventStatusImageCompleted(l.ID, name, err); err2 != nil {
		l.Printf("error setting image status to completed: %v: %v", name, err2)
	}
}

// SetChartStarted marks the chart install/upgrade for the named dependency to started (name is assumed to exist) with status
func (l *Logger) SetChartStarted(name string, status models.NodeChartStatus) {
	if err := l.DL.SetEventStatusChartStarted(l.ID, name, status); err != nil {
		l.Printf("error setting chart status to started: %v: %v", name, err)
	}
}

// SetChartCompleted marks the chart install/upgrade for the named dependency to completed (name is assumed to exist) with status
func (l *Logger) SetChartCompleted(name string, status models.NodeChartStatus) {
	if err := l.DL.SetEventStatusChartCompleted(l.ID, name, status); err != nil {
		l.Printf("error setting chart status to completed: %v: %v", name, err)
	}
}

// SetCompletedStatus marks the entire event as completed with status. This is intended to be called once at the end of event processing.
func (l *Logger) SetCompletedStatus(status models.EventStatus) {
	if err := l.DL.SetEventStatusCompleted(l.ID, status); err != nil {
		l.Printf("error setting event status to completed: %v", err)
	}
}

// SetFailedStatus marks the entire event as completed with a failed status. This is intended to be called once at the end of event processing.
func (l *Logger) SetFailedStatus(ce metahelm.ChartError) {
	if err := l.DL.SetEventStatusFailed(l.ID, ce); err != nil {
		l.Printf("error setting event status to failed: %v", err)
	}
}
