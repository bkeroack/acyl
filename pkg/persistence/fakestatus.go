package persistence

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	lorem "github.com/dollarshaveclub/acyl/pkg/persistence/golorem"
	guuid "github.com/gofrs/uuid"

	"github.com/dollarshaveclub/acyl/pkg/models"
	"github.com/dollarshaveclub/acyl/pkg/namegen"
	"github.com/google/uuid"
)

func randomDuration(minMillis, maxMillis uint) time.Duration {
	if maxMillis < minMillis {
		return time.Duration(0)
	}

	rand.Seed(time.Now().UnixNano())
	return time.Duration(rand.Intn(int(maxMillis-minMillis+1))+int(minMillis)) * time.Millisecond
}

func randomLogLines(n uint) []string {
	out := make([]string, n)
	for i := 0; i < int(n); i++ {
		out[i] = lorem.Sentence(5, 20)
	}
	return out
}

func (fdl *FakeDataLayer) updateEvent(id uuid.UUID, envname string, success bool) {
	// update config after a bit
	cfgd := randomDuration(50, 200)
	time.Sleep(cfgd)

	if envname == "" {
		ng, _ := namegen.NewWordnetNameGenerator("data/words.json.gz", log.New(os.Stderr, "", log.LstdFlags))
		envname = "foonly-receptacle"
		if ng != nil {
			rn, err := ng.New()
			if err == nil {
				envname = rn
			}
		}
	}

	started := time.Now().UTC()

	offsetTime := func(created time.Time) time.Time {
		d := time.Now().UTC().Sub(started)
		return created.Add(d)
	}

	fdl.data.Lock()
	fdl.data.elogs[id].Log = append(fdl.data.elogs[id].Log, randomLogLines(10)...)
	fdl.data.elogs[id].Status.Config.RenderedStatus = models.RenderedEventStatus{
		Description:   fmt.Sprintf("The Acyl environment %v is being created.", envname),
		LinkTargetURL: "https://media.giphy.com/media/oiymhxu13VYEo/giphy.gif",
	}
	fdl.data.elogs[id].EnvName = envname
	fdl.data.elogs[id].Status.Config.EnvName = envname
	fdl.data.elogs[id].Status.Config.K8sNamespace = "nitro-84930-" + envname
	fdl.data.elogs[id].Status.Config.ProcessingTime = models.ConfigProcessingDuration{Duration: cfgd}
	fdl.data.elogs[id].Status.Config.RefMap = map[string]string{
		"foo/bar":           "asdf1234",
		"foo/somethingelse": "master",
		"foo/dependency":    "feature-foo",
	}
	fdl.data.elogs[id].Status.Tree = map[string]models.EventStatusTreeNode{
		"foo-bar": models.EventStatusTreeNode{
			Parent: "",
			Image: models.EventStatusTreeNodeImage{
				Name:    "quay.io/foo/bar",
				Started: offsetTime(fdl.data.elogs[id].Created),
				ID:      guuid.Must(guuid.NewV4()),
			},
			Chart: models.EventStatusTreeNodeChart{
				Status: models.WaitingChartStatus,
			},
		},
		"memcache": models.EventStatusTreeNode{
			Parent: "foo-bar",
			Chart: models.EventStatusTreeNodeChart{
				Status: models.WaitingChartStatus,
			},
		},
		"foo-somethingelse": models.EventStatusTreeNode{
			Parent: "foo-bar",
			Image: models.EventStatusTreeNodeImage{
				Name: "quay.io/foo/somethingelse",
				ID:   guuid.Must(guuid.NewV4()),
			},
			Chart: models.EventStatusTreeNodeChart{
				Status: models.WaitingChartStatus,
			},
		},
		"foo-dependency": models.EventStatusTreeNode{
			Parent: "foo-bar",
			Image: models.EventStatusTreeNodeImage{
				Name:    "quay.io/foo/dependency",
				Started: offsetTime(fdl.data.elogs[id].Created),
				ID:      guuid.Must(guuid.NewV4()),
			},
			Chart: models.EventStatusTreeNodeChart{
				Status: models.WaitingChartStatus,
			},
		},
		"foo-dependency-redis": models.EventStatusTreeNode{
			Parent: "foo-dependency",
			Chart: models.EventStatusTreeNodeChart{
				Status: models.WaitingChartStatus,
			},
		},
		"foo-dependency-postgres": models.EventStatusTreeNode{
			Parent: "foo-dependency",
			Chart: models.EventStatusTreeNodeChart{
				Status: models.WaitingChartStatus,
			},
		},
		"foo-dependency-some-long-name-asdf12345": models.EventStatusTreeNode{
			Parent: "foo-dependency",
			Chart: models.EventStatusTreeNodeChart{
				Status: models.WaitingChartStatus,
			},
		},
		"foo-dependency-some-long-name-asdf12345-asdf": models.EventStatusTreeNode{
			Parent: "foo-dependency",
			Chart: models.EventStatusTreeNodeChart{
				Status: models.WaitingChartStatus,
			},
		},
		"foo-dependency-some-long-name-asdf12345-2": models.EventStatusTreeNode{
			Parent: "foo-dependency",
			Chart: models.EventStatusTreeNodeChart{
				Status: models.WaitingChartStatus,
			},
		},
		"foo-level-4": models.EventStatusTreeNode{
			Parent: "foo-dependency-some-long-name-asdf12345-2",
			Chart: models.EventStatusTreeNodeChart{
				Status: models.WaitingChartStatus,
			},
		},
		"foo-level-5": models.EventStatusTreeNode{
			Parent: "foo-level-4",
			Chart: models.EventStatusTreeNodeChart{
				Status: models.WaitingChartStatus,
			},
		},
	}
	fdl.data.Unlock()

	// start level 5
	time.Sleep(randomDuration(500, 5000))
	fdl.data.Lock()
	{
		fdl.data.elogs[id].Log = append(fdl.data.elogs[id].Log, randomLogLines(10)...)
		n := "foo-level-5"
		c := fdl.data.elogs[id].Status.Tree[n]
		c.Chart.Status = models.InstallingChartStatus
		c.Chart.Started = offsetTime(fdl.data.elogs[id].Created)
		fdl.data.elogs[id].Status.Tree[n] = c
	}
	fdl.data.Unlock()

	// complete level 5
	time.Sleep(randomDuration(500, 5000))
	fdl.data.Lock()
	{
		n := "foo-level-5"
		c := fdl.data.elogs[id].Status.Tree[n]
		c.Chart.Status = models.DoneChartStatus
		c.Chart.Completed = offsetTime(fdl.data.elogs[id].Created)
		fdl.data.elogs[id].Status.Tree[n] = c
	}
	fdl.data.Unlock()

	// start level 4
	time.Sleep(randomDuration(500, 5000))
	fdl.data.Lock()
	{
		fdl.data.elogs[id].Log = append(fdl.data.elogs[id].Log, randomLogLines(10)...)
		n := "foo-level-4"
		c := fdl.data.elogs[id].Status.Tree[n]
		c.Chart.Status = models.InstallingChartStatus
		c.Chart.Started = offsetTime(fdl.data.elogs[id].Created)
		fdl.data.elogs[id].Status.Tree[n] = c
	}
	fdl.data.Unlock()

	// complete level 4
	time.Sleep(randomDuration(500, 5000))
	fdl.data.Lock()
	{
		n := "foo-level-4"
		c := fdl.data.elogs[id].Status.Tree[n]
		c.Chart.Status = models.DoneChartStatus
		c.Chart.Completed = offsetTime(fdl.data.elogs[id].Created)
		fdl.data.elogs[id].Status.Tree[n] = c
	}
	fdl.data.Unlock()

	// start foo-somethingelse image build
	fdl.data.Lock()
	{
		c := fdl.data.elogs[id].Status.Tree["foo-somethingelse"]
		c.Image.Started = offsetTime(fdl.data.elogs[id].Created)
		fdl.data.elogs[id].Status.Tree["foo-somethingelse"] = c
	}
	fdl.data.Unlock()

	// update leaf dependencies to installing
	time.Sleep(randomDuration(500, 5000))
	fdl.data.Lock()
	fdl.data.elogs[id].Log = append(fdl.data.elogs[id].Log, randomLogLines(10)...)
	for _, n := range []string{"foo-dependency-postgres", "foo-dependency-redis", "foo-dependency-some-long-name-asdf12345", "foo-dependency-some-long-name-asdf12345-asdf", "foo-dependency-some-long-name-asdf12345-2"} {
		c := fdl.data.elogs[id].Status.Tree[n]
		c.Chart.Status = models.InstallingChartStatus
		c.Chart.Started = offsetTime(fdl.data.elogs[id].Created)
		fdl.data.elogs[id].Status.Tree[n] = c
	}
	fdl.data.Unlock()

	// complete leaf dependencies
	time.Sleep(randomDuration(0, 3000))
	fdl.data.Lock()
	fdl.data.elogs[id].Log = append(fdl.data.elogs[id].Log, randomLogLines(10)...)
	for _, n := range []string{"foo-dependency-postgres", "foo-dependency-redis", "foo-dependency-some-long-name-asdf12345", "foo-dependency-some-long-name-asdf12345-asdf", "foo-dependency-some-long-name-asdf12345-2"} {
		c := fdl.data.elogs[id].Status.Tree[n]
		c.Chart.Status = models.DoneChartStatus
		c.Chart.Completed = offsetTime(fdl.data.elogs[id].Created)
		fdl.data.elogs[id].Status.Tree[n] = c
	}
	fdl.data.Unlock()

	// finish foo-dependency image build & start installing
	time.Sleep(randomDuration(0, 3000))
	fdl.data.Lock()
	fdl.data.elogs[id].Log = append(fdl.data.elogs[id].Log, randomLogLines(10)...)
	i := fdl.data.elogs[id].Status.Tree["foo-dependency"]
	i.Image.Completed = offsetTime(fdl.data.elogs[id].Created)
	i.Image.Error = false
	i.Chart.Status = models.InstallingChartStatus
	i.Chart.Started = offsetTime(fdl.data.elogs[id].Created)
	fdl.data.elogs[id].Status.Tree["foo-dependency"] = i
	fdl.data.Unlock()

	// finish foo-somethingelse image build & start installing
	time.Sleep(randomDuration(0, 4000))
	fdl.data.Lock()
	fdl.data.elogs[id].Log = append(fdl.data.elogs[id].Log, randomLogLines(10)...)
	i = fdl.data.elogs[id].Status.Tree["foo-somethingelse"]
	i.Image.Completed = offsetTime(fdl.data.elogs[id].Created)
	i.Image.Error = true
	i.Chart.Status = models.InstallingChartStatus
	i.Chart.Started = offsetTime(fdl.data.elogs[id].Created)
	fdl.data.elogs[id].Status.Tree["foo-somethingelse"] = i
	fdl.data.Unlock()

	// complete foo-bar image build, set chart to waiting
	time.Sleep(randomDuration(0, 5000))
	fdl.data.Lock()
	fdl.data.elogs[id].Log = append(fdl.data.elogs[id].Log, randomLogLines(10)...)
	i = fdl.data.elogs[id].Status.Tree["foo-bar"]
	i.Image.Completed = offsetTime(fdl.data.elogs[id].Created)
	i.Image.Error = false
	i.Chart.Status = models.WaitingChartStatus
	fdl.data.elogs[id].Status.Tree["foo-bar"] = i
	fdl.data.Unlock()

	// start memcache install
	time.Sleep(randomDuration(0, 5000))
	fdl.data.Lock()
	fdl.data.elogs[id].Log = append(fdl.data.elogs[id].Log, randomLogLines(10)...)
	i = fdl.data.elogs[id].Status.Tree["memcache"]
	i.Image.Completed = offsetTime(fdl.data.elogs[id].Created)
	i.Image.Error = false
	i.Chart.Status = models.InstallingChartStatus
	i.Chart.Started = offsetTime(fdl.data.elogs[id].Created)
	fdl.data.elogs[id].Status.Tree["memcache"] = i
	fdl.data.Unlock()

	// complete foo-dependency install
	time.Sleep(randomDuration(0, 4000))
	fdl.data.Lock()
	fdl.data.elogs[id].Log = append(fdl.data.elogs[id].Log, randomLogLines(10)...)
	i = fdl.data.elogs[id].Status.Tree["foo-dependency"]
	i.Chart.Status = models.DoneChartStatus
	i.Chart.Completed = offsetTime(fdl.data.elogs[id].Created)
	fdl.data.elogs[id].Status.Tree["foo-dependency"] = i
	fdl.data.Unlock()

	// complete foo-somethingelse install
	time.Sleep(randomDuration(0, 3000))
	fdl.data.Lock()
	fdl.data.elogs[id].Log = append(fdl.data.elogs[id].Log, randomLogLines(10)...)
	i = fdl.data.elogs[id].Status.Tree["foo-somethingelse"]
	i.Chart.Status = models.DoneChartStatus
	i.Chart.Completed = offsetTime(fdl.data.elogs[id].Created)
	fdl.data.elogs[id].Status.Tree["foo-somethingelse"] = i
	fdl.data.Unlock()

	// complete memcache install
	time.Sleep(randomDuration(0, 4000))
	fdl.data.Lock()
	fdl.data.elogs[id].Log = append(fdl.data.elogs[id].Log, randomLogLines(10)...)
	i = fdl.data.elogs[id].Status.Tree["memcache"]
	i.Chart.Status = models.DoneChartStatus
	i.Chart.Completed = offsetTime(fdl.data.elogs[id].Created)
	fdl.data.elogs[id].Status.Tree["memcache"] = i
	fdl.data.Unlock()

	// start foo-bar install
	time.Sleep(randomDuration(0, 3000))
	fdl.data.Lock()
	fdl.data.elogs[id].Log = append(fdl.data.elogs[id].Log, randomLogLines(10)...)
	i = fdl.data.elogs[id].Status.Tree["foo-bar"]
	i.Chart.Status = models.InstallingChartStatus
	i.Chart.Started = offsetTime(fdl.data.elogs[id].Created)
	fdl.data.elogs[id].Status.Tree["foo-bar"] = i
	fdl.data.Unlock()

	// outcome
	finalcstatus := models.FailedChartStatus
	finalstatus := models.FailedStatus
	if success {
		finalcstatus = models.DoneChartStatus
		finalstatus = models.DoneStatus
	}

	// complete foo-bar install, mark event as completed
	time.Sleep(randomDuration(1000, 2000))
	fdl.data.Lock()
	fdl.data.elogs[id].Status.Config.RenderedStatus = models.RenderedEventStatus{
		Description:   fmt.Sprintf("The Acyl environment %v was created successfully.", envname),
		LinkTargetURL: "https://media.giphy.com/media/SRO0ZwmImic0/giphy.gif",
	}
	fdl.data.elogs[id].Log = append(fdl.data.elogs[id].Log, randomLogLines(10)...)
	i = fdl.data.elogs[id].Status.Tree["foo-bar"]
	i.Chart.Status = finalcstatus
	i.Chart.Completed = offsetTime(fdl.data.elogs[id].Created)
	fdl.data.elogs[id].Status.Tree["foo-bar"] = i
	fdl.data.elogs[id].Status.Config.Status = finalstatus
	fdl.data.elogs[id].Status.Config.Completed = offsetTime(fdl.data.elogs[id].Created)
	fdl.data.Unlock()
}

func (fdl *FakeDataLayer) newStatus(id uuid.UUID, repo, user, envname string, etype models.EventStatusType, success bool) *models.EventStatusSummary {
	if repo == "" && user == "" && envname == "" {
		// if no explicit data is set, associate the event with a random existing env
		rand.Seed(time.Now().UnixNano())
		fdl.data.RLock()
		if len(fdl.data.d) == 0 {
			repo = "acme/foo"
			user = "alice.walker"
			envname = "some-name"
		} else {
			n := rand.Intn(len(fdl.data.d))
			var i int
			for k := range fdl.data.d {
				if i == n {
					repo = fdl.data.d[k].Repo
					user = fdl.data.d[k].User
					envname = fdl.data.d[k].Name
					break
				}
				i++
			}
		}
		fdl.data.RUnlock()
	}
	out := models.EventStatusSummary{
		Config: models.EventStatusSummaryConfig{
			Type:           etype,
			Status:         models.PendingStatus,
			TriggeringRepo: repo,
			PullRequest:    23,
			GitHubUser:     user,
			Branch:         "feature-foo",
			Revision:       "asdf1234",
			Started:        time.Now().UTC(),
		},
		Tree: map[string]models.EventStatusTreeNode{},
	}

	fdl.data.Lock()
	if fdl.data.elogs[id] == nil {
		fdl.data.elogs[id] = &models.EventLog{
			ID:          id,
			Created:     time.Now().UTC(),
			Repo:        repo,
			PullRequest: 23,
		}
	}
	fdl.data.elogs[id].Status = out
	fdl.data.elogs[id].Log = []string{}
	fdl.data.Unlock()

	go fdl.updateEvent(id, envname, success)
	return &out
}

func (fdl *FakeDataLayer) setEventLogStarted(id uuid.UUID, started time.Time) {
	fdl.data.Lock()
	fdl.data.elogs[id].Created = started
	fdl.data.elogs[id].Status.Config.Started = started
	fdl.data.Unlock()
}

func (fdl *FakeDataLayer) NewFakeCreateEvent(started time.Time, repo, user, envname string) uuid.UUID {
	id := uuid.Must(uuid.NewRandom())
	fdl.newStatus(id, repo, user, envname, models.CreateEvent, true)
	fdl.setEventLogStarted(id, started)
	return id
}

func (fdl *FakeDataLayer) NewFakeEvent(started time.Time, repo, user, envname string, etype models.EventStatusType, success bool) uuid.UUID {
	id := uuid.Must(uuid.NewRandom())
	fdl.newStatus(id, repo, user, envname, etype, success)
	fdl.setEventLogStarted(id, started)
	return id
}
