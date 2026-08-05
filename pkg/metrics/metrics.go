package metrics

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/DataDog/datadog-go/statsd"
	"github.com/bkeroack/acyl/pkg/models"
)

const (
	namespace = "acyl."
)

// Collector describes an object capabale of pushing metrics somewhere
type Collector interface {
	Success(name, repo, ref string)
	Failure(name, repo, ref string)
	EventRateLimitDropped(name string)
	EventCountExceededDropped(name, repo, ref string)
	Operation(op, name, repo, ref string, err error)
	ProvisioningDuration(name, repo, ref string, duration time.Duration, err error)
	ContainerBuildAllDuration(name, repo, ref string, duration time.Duration, err error)
	ContainerBuildDuration(name, repo, ref, depRepo, depRef string, duration time.Duration, err error)
	EnvironmentCount(repo string, status models.EnvironmentStatus, num uint)
	Pruned(count int)
	Reaped(name, repo string, reason models.QADestroyReason, err error)
	TimeContainerBuildAll(name, repo, ref string, err *error) func()
	TimeProvisioning(name, repo, ref string, err *error) func()
	TimeContainerBuild(name, repo, ref, depRepo, depRef string, err *error) func()
	AminoDeployTimedOut(name, repo, ref string)
	ImageBuildFailed(name, repo, ref string)
}

// DatadogCollector represents a collector that pushes metrics to Datadog
type DatadogCollector struct {
	c      *statsd.Client
	logger *log.Logger
}

var _ Collector = &DatadogCollector{}

// NewDatadogCollector returns a DatadogCollector using dogstatsd at addr
func NewDatadogCollector(addr string, logger *log.Logger) (*DatadogCollector, error) {
	c, err := statsd.New(addr)
	if err != nil {
		return nil, err
	}
	c.Namespace = namespace
	return &DatadogCollector{
		c:      c,
		logger: logger,
	}, nil
}

func (dc *DatadogCollector) tag(k, v string) string {
	return strings.Join([]string{k, v}, ":")
}

func (dc *DatadogCollector) tagsForOp(name, repo string) []string {
	return []string{
		dc.tag("name", name),
		dc.tag("repo", repo),
	}
}

func (dc *DatadogCollector) tagsForEnv(name, repo, ref string) []string {
	return dc.tagsForProvision(name, repo, ref)
}

func (dc *DatadogCollector) tagsForProvision(name, repo, ref string) []string {
	return append(dc.tagsForOp(name, repo), []string{dc.tag("ref", ref)}...)
}

func (dc *DatadogCollector) timeProvisioning(segment string, duration time.Duration, tags []string, err error) error {
	status := dc.statusOfOp(err)
	provTags := append(tags, []string{dc.tag("status", status)}...)
	metric := fmt.Sprintf("provisioning.time.%s", segment)

	return dc.timing(metric, duration, provTags)
}

func (dc *DatadogCollector) statusOfOp(err error) string {
	status := "success"

	if err != nil {
		status = "error"
	}

	return status
}
func (dc *DatadogCollector) countOpResult(op string, err error, tags []string) error {
	status := dc.statusOfOp(err)
	allTags := append(tags, []string{dc.tag("status", status), dc.tag("operation", op)}...)
	metric := "operations.count"

	return dc.incr(metric, allTags)
}

func (dc *DatadogCollector) logMetricPush(op, metric string, err error, value ...interface{}) {
	if err != nil {
		dc.logger.Printf("error pushing dd-metric: %s: %s: %v : %v", op, metric, value, err)
	} else {
		dc.logger.Printf("pushing dd-metric: %s: %s: %v", op, metric, value)
	}
}

func (dc *DatadogCollector) gauge(metric string, value float64, tags []string) (err error) {
	defer func() { dc.logMetricPush("gauge", metric, err, value, tags) }()

	return dc.c.Gauge(metric, value, tags, 1)
}

func (dc *DatadogCollector) timing(metric string, duration time.Duration, tags []string) (err error) {
	defer func() { dc.logMetricPush("timing", metric, err, duration, tags) }()

	return dc.c.Timing(metric, duration, tags, 1)
}

func (dc *DatadogCollector) incr(metric string, tags []string) (err error) {
	defer func() { dc.logMetricPush("incr", metric, err, tags) }()

	return dc.c.Incr(metric, tags, 1)
}

func (dc *DatadogCollector) count(metric string, value int64, tags []string) (err error) {
	defer func() { dc.logMetricPush("count", metric, err, value, tags) }()

	return dc.c.Count(metric, value, tags, 1)
}

func (dc *DatadogCollector) Success(name, repo, ref string) {
	dc.incr("environment.success", dc.tagsForEnv(name, repo, ref))
}

func (dc *DatadogCollector) Failure(name, repo, ref string) {
	dc.incr("environment.failure", dc.tagsForEnv(name, repo, ref))
}

func (dc *DatadogCollector) AminoDeployTimedOut(name, repo, ref string) {
	dc.incr("environment.amino_deploy_timed_out", dc.tagsForEnv(name, repo, ref))
}

func (dc *DatadogCollector) ImageBuildFailed(name, repo, ref string) {
	dc.incr("environment.image_build_failed", dc.tagsForEnv(name, repo, ref))
}

func (dc *DatadogCollector) EventRateLimitDropped(name string) {
	dc.c.Incr("environment.event_rate_limit_dropped", []string{"name:" + name}, 1)
}

func (dc *DatadogCollector) EventCountExceededDropped(name, repo, ref string) {
	dc.incr("environment.event_count_exceeded_dropped", dc.tagsForEnv(name, repo, ref))
}

func (dc *DatadogCollector) Operation(op, name, repo, ref string, err error) {
	dc.countOpResult(op, err, dc.tagsForEnv(name, repo, ref))
}

func (dc *DatadogCollector) ProvisioningDuration(name, repo, ref string, duration time.Duration, err error) {
	dc.timeProvisioning("total", duration, dc.tagsForProvision(name, repo, ref), err)
}

func (dc *DatadogCollector) ContainerBuildAllDuration(name, repo, ref string, duration time.Duration, err error) {
	dc.timeProvisioning("build.all", duration, dc.tagsForProvision(name, repo, ref), err)
}

func (dc *DatadogCollector) ContainerBuildDuration(name, repo, ref, depRepo, depRef string, duration time.Duration, err error) {
	depTags := []string{dc.tag("deprepo", depRepo), dc.tag("depref", depRef)}
	tags := append(depTags, dc.tagsForProvision(name, repo, ref)...)

	dc.timeProvisioning("build.single", duration, tags, err)
}

func (dc *DatadogCollector) EnvironmentCount(repo string, status models.EnvironmentStatus, num uint) {
	tags := []string{
		dc.tag("repo", repo),
		dc.tag("status", strings.ToLower(status.String())),
	}
	dc.gauge("environments.count", float64(num), tags)
}

func (dc *DatadogCollector) Pruned(count int) {
	dc.count("reaper.pruned", int64(count), []string{})
}

func (dc *DatadogCollector) Reaped(name, repo string, reason models.QADestroyReason, err error) {
	status := dc.statusOfOp(err)
	tags := []string{
		dc.tag("status", status),
		dc.tag("reason", reason.String()),
	}

	dc.incr("reaper.reaps", append(tags, dc.tagsForOp(name, repo)...))
}

func (dc *DatadogCollector) timeAround(f func(time.Duration)) func() {
	start := time.Now()

	return func() { f(time.Since(start)) }
}

func (dc *DatadogCollector) TimeContainerBuildAll(name, repo, ref string, err *error) func() {
	return dc.timeAround(func(duration time.Duration) {
		dc.ContainerBuildAllDuration(name, repo, ref, duration, *err)
	})
}

func (dc *DatadogCollector) TimeProvisioning(name, repo, ref string, err *error) func() {
	return dc.timeAround(func(duration time.Duration) {
		dc.ProvisioningDuration(name, repo, ref, duration, *err)
	})
}

func (dc *DatadogCollector) TimeContainerBuild(name, repo, ref, depRepo, depRef string, err *error) func() {
	return dc.timeAround(func(duration time.Duration) {
		dc.ContainerBuildDuration(name, repo, ref, depRepo, depRef, duration, *err)
	})
}
