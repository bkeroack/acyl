package metahelm

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"strings"
	"time"

	"github.com/bkeroack/acyl/pkg/models"
	"github.com/bkeroack/acyl/pkg/persistence"
	"github.com/dollarshaveclub/metahelm/pkg/metahelm"
	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
)

// FakeInstaller satisfies the Installer interface but does nothing
type FakeInstaller struct {
	// ChartInstallFunc is called for each repo in chartLocations. Return an error to abort.
	ChartInstallFunc func(repo string, location ChartLocation) error
	ChartUpgradeFunc func(repo string, k8senv *models.KubernetesEnvironment, location ChartLocation) error
	DL               persistence.DataLayer
	KC               kubernetes.Interface
	HelmReleases     []string
}

var _ Installer = &FakeInstaller{}

func getReleases(cl ChartLocations) metahelm.ReleaseMap {
	releases := metahelm.ReleaseMap{} // chart title to release name
	for k := range cl {
		releases[k] = k
	}
	return releases
}

func (fi *FakeInstaller) BuildAndInstallCharts(ctx context.Context, newenv *EnvInfo, chartsLocation ChartLocations) error {
	for k, v := range chartsLocation {
		if err := fi.ChartInstallFunc(k, v); err != nil {
			return fmt.Errorf("install aborted: %w", err)
		}
	}
	if fi.DL != nil {
		ci := ChartInstaller{dl: fi.DL, kc: fi.KC}
		if err := ci.writeK8sEnvironment(ctx, newenv, "nitro-1234-"+newenv.Env.Name); err != nil {
			return err
		}
		releases := getReleases(chartsLocation)
		return ci.writeReleaseNames(ctx, releases, "fake-namespace", newenv)
	}
	return nil
}

func (fi FakeInstaller) BuildAndUpgradeCharts(ctx context.Context, env *EnvInfo, k8senv *models.KubernetesEnvironment, cl ChartLocations) error {
	for k, v := range cl {
		if err := fi.ChartUpgradeFunc(k, k8senv, v); err != nil {
			return fmt.Errorf("upgrade aborted: %w", err)
		}
	}
	if fi.DL != nil {
		ci := ChartInstaller{dl: fi.DL}
		return ci.updateReleaseRevisions(ctx, env)
	}
	return nil
}

func (fi FakeInstaller) BuildAndInstallChartsIntoExisting(ctx context.Context, newenv *EnvInfo, k8senv *models.KubernetesEnvironment, cl ChartLocations) error {
	for k, v := range cl {
		if err := fi.ChartInstallFunc(k, v); err != nil {
			return fmt.Errorf("install aborted: %w", err)
		}
	}
	if fi.DL != nil {
		ci := ChartInstaller{dl: fi.DL}
		releases := getReleases(cl)
		return ci.writeReleaseNames(ctx, releases, "fake-namespace", newenv)
	}
	return nil
}

func (fi FakeInstaller) DeleteNamespace(ctx context.Context, k8senv *models.KubernetesEnvironment) error {
	if fi.DL != nil {
		return fi.DL.DeleteK8sEnv(ctx, k8senv.EnvName)
	}
	return nil
}

// FakeKubernetesReporter satisfies the kubernetes reporter interface but does nothing
type FakeKubernetesReporter struct {
	FakePodLogFilePath string
}

var _ KubernetesReporter = &FakeKubernetesReporter{}

func (fkr FakeKubernetesReporter) GetPodList(ctx context.Context, ns string) (out []K8sPod, err error) {
	pl := stubPodData(ns)
	for _, p := range pl.Items {
		age := time.Since(p.CreationTimestamp.Time)
		var nReady int
		nContainers := len(p.Spec.Containers)
		for _, c := range p.Status.ContainerStatuses {
			if c.Ready {
				nReady += 1
			}
		}
		rc := int32(0)
		if p.Status.Size() != 0 &&
			len(p.Status.ContainerStatuses) > 0 &&
			p.Status.ContainerStatuses[0].Size() != 0 {
			rc = p.Status.ContainerStatuses[0].RestartCount
		}
		out = append(out, K8sPod{
			Name:     p.Name,
			Ready:    fmt.Sprintf("%v/%v", nReady, nContainers),
			Status:   string(p.Status.Phase),
			Restarts: rc,
			Age:      age,
		})
	}
	return out, nil
}

func stubPodData(ns string) *v1.PodList {
	podContainerStatus := v1.ContainerStatus{
		Name:         "foo-app",
		RestartCount: 0,
		Ready:        true,
	}
	podContainerSidecarStatus := v1.ContainerStatus{
		Name:         "foo-app-sidecar",
		RestartCount: 0,
		Ready:        true,
	}
	pod := v1.Pod{
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				podContainerStatus,
				podContainerSidecarStatus,
			},
			Phase: "Running",
			PodIP: "10.0.0.1",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{Name: podContainerStatus.Name},
				{Name: podContainerSidecarStatus.Name},
			},
		},
	}
	pod.Kind = "Pod"
	pod.Name = "foo-app-abc123"
	pod.Namespace = ns
	pod.Labels = map[string]string{"app": "foo-app"}
	pod.Status.Phase = "Running"
	pod.CreationTimestamp.Time = time.Now().UTC().Add(-3 * time.Hour).Add(-37 * time.Minute).Add(-33 * time.Second)

	pod2ContainerStatus := v1.ContainerStatus{
		Name:         "bar-app",
		RestartCount: 1,
		Ready:        true,
	}
	pod2NotReadyContainerStatus := v1.ContainerStatus{
		Name:         "bar-app-not-ready",
		RestartCount: 4,
		Ready: false,
	}
	pod2 := v1.Pod{
		Status: v1.PodStatus{
			ContainerStatuses: []v1.ContainerStatus{
				pod2ContainerStatus,
				pod2NotReadyContainerStatus,
			},
			Phase: "Pending",
			PodIP: "10.0.0.2",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{Name: pod2ContainerStatus.Name},
				{Name: pod2NotReadyContainerStatus.Name},
			},
		},
	}
	pod2.Kind = "Pod"
	pod2.Name = "bar-app-abc123"
	pod2.Namespace = ns
	pod2.Labels = map[string]string{"app": "bar-app"}
	pod2.CreationTimestamp.Time = time.Now().UTC().Add(-3 * time.Hour).Add(-37 * time.Minute).Add(-33 * time.Second)

	return &v1.PodList{
		Items: []v1.Pod{
			pod,
			pod2,
		},
	}
}

func (fkr FakeKubernetesReporter) GetPodContainers(ctx context.Context, ns, podname string) (out K8sPodContainers, err error) {
	pl := stubPodData(ns)
	var pod v1.Pod
	for _, p := range pl.Items {
		if p.Name == podname {
			pod = p
		}
	}
	if pod.Name == "" {
		return K8sPodContainers{}, errors.Errorf("error pod %v not found for namespace %v", podname, ns)
	}
	containers := []string{}
	for _, c := range pod.Spec.Containers {
		containers = append(containers, c.Name)
	}
	return K8sPodContainers{
		Pod:        pod.Name,
		Containers: containers,
	}, nil
}

func (fkr FakeKubernetesReporter) GetPodLogs(ctx context.Context, ns, podname, container string, lines uint) (out io.ReadCloser, err error) {
	rc := ioutil.NopCloser(strings.NewReader(""))
	body, err := getFakePodLogs(fkr.FakePodLogFilePath, lines)
	if err != nil {
		return rc, fmt.Errorf("error getting logs: %w", err)
	}
	lineCount := bytes.Count(body, []byte{'\n'})
	if uint(lineCount) > lines {
		return rc, errors.Errorf("error lines returned exceeded expected %v, actual %v", lines, lineCount)
	}
	plRC := ioutil.NopCloser(bytes.NewReader(body))
	return plRC, nil
}

func getFakePodLogs(path string, lines uint) ([]byte, error) {
	var bucket []string
	var logs []string
	var requestedLogs []byte
	podlogs, err := ioutil.ReadFile(path)
	if err != nil {
		return []byte{}, fmt.Errorf("error reading pod log test data file: %w", err)
	}
	// return number of log lines equal to request to mimic corev1.PodLogOptions{ TailLines }
	br := bytes.NewReader(podlogs)
	scanner := bufio.NewScanner(br)
	scanner.Split(bufio.ScanLines)
	for scanner.Scan() {
		bucket = append(bucket, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return []byte{}, fmt.Errorf("error scanning log lines: %w", err)
	}
	// create random value to vary logs returned for testing
	rand.Seed(time.Now().UnixNano())
	r := len(bucket) - (rand.Intn(50) + 1)
	// reverse bucket of all logs to return most recent test logs
	count := 0
	for i := r; i >= 0; i-- {
		if count == int(lines) {
			break
		}
		logs = append(logs, bucket[i])
		count++
	}
	// convert log string slice to byte slice
	for i := 0; i < len(logs); i++ {
		b := []byte(logs[i] + "\n")
		for j := 0; j < len(b); j++ {
			requestedLogs = append(requestedLogs, b[j])
		}
	}
	return requestedLogs, nil
}
