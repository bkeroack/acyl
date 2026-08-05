package metrics

import (
	"time"

	"github.com/bkeroack/acyl/pkg/models"
)

var _ Collector = &FakeCollector{}

// FakeCollector satisfies the Collector interface but does nothing
type FakeCollector struct{}

func (fc *FakeCollector) Success(name, repo, ref string)                   {}
func (fc *FakeCollector) Failure(name, repo, ref string)                   {}
func (fc *FakeCollector) EventRateLimitDropped(name string)                {}
func (fc *FakeCollector) EventCountExceededDropped(name, repo, ref string) {}
func (fc *FakeCollector) Operation(op, name, repo, ref string, err error)  {}
func (fc *FakeCollector) ProvisioningDuration(name, repo, ref string, duration time.Duration, err error) {
}
func (fc *FakeCollector) ContainerBuildAllDuration(name, repo, ref string, duration time.Duration, err error) {
}
func (fc *FakeCollector) ContainerBuildDuration(name, repo, ref, depRepo, depRef string, duration time.Duration, err error) {
}
func (fc *FakeCollector) EnvironmentCount(repo string, status models.EnvironmentStatus, num uint) {}
func (fc *FakeCollector) Pruned(count int)                                                        {}
func (fc *FakeCollector) Reaped(name, repo string, reason models.QADestroyReason, err error)      {}
func (fc *FakeCollector) TimeContainerBuildAll(name, repo, ref string, err *error) func() {
	return func() {}
}
func (fc *FakeCollector) TimeProvisioning(name, repo, ref string, err *error) func() { return func() {} }
func (fc *FakeCollector) TimeContainerBuild(name, repo, ref, depRepo, depRef string, err *error) func() {
	return func() {}
}
func (fc *FakeCollector) AminoDeployTimedOut(name, repo, ref string) {}
func (fc *FakeCollector) ImageBuildFailed(name, repo, ref string)    {}
