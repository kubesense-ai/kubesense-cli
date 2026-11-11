package plan

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// WorkloadStatus is one Deployment, StatefulSet or DaemonSet of the release.
type WorkloadStatus struct {
	Kind, Name              string
	Desired, Updated, Ready int32
	// Problem is why pods are not ready, read from them: OOMKilled,
	// CrashLoopBackOff, ImagePullBackOff, Pending (unschedulable), ...
	Problem string
}

func (w WorkloadStatus) Done() bool { return w.Updated >= w.Desired && w.Ready >= w.Desired }

func (w WorkloadStatus) String() string {
	s := fmt.Sprintf("%s/%s %d/%d ready, %d/%d updated", w.Kind, w.Name, w.Ready, w.Desired, w.Updated, w.Desired)
	if w.Problem != "" {
		s += ": " + w.Problem
	}
	return s
}

// RolloutResult is how the release's workloads ended up.
type RolloutResult struct {
	Workloads []WorkloadStatus
	TimedOut  bool
}

// NotReady lists the workloads that did not finish rolling out.
func (r RolloutResult) NotReady() []WorkloadStatus {
	var out []WorkloadStatus
	for _, w := range r.Workloads {
		if !w.Done() {
			out = append(out, w)
		}
	}
	return out
}

// WatchRollout follows the release's workloads after apply until every one is
// updated and ready, or timeout. It calls progress whenever a workload's state
// changes, so the person applying sees what is rolling and why something is
// stuck instead of a silent wait. It only reads.
func WatchRollout(ctx context.Context, t Target, namespace, release string, timeout time.Duration, progress func(WorkloadStatus)) (RolloutResult, error) {
	c, err := t.connect(namespace, nil, nil)
	if err != nil {
		return RolloutResult{}, err
	}
	deadline := time.Now().Add(timeout)
	stepf("watching the rollout of release %s every 3s, up to %s", release, timeout)
	last := map[string]string{}
	for {
		ws, err := releaseWorkloads(ctx, c.clientset, namespace, release)
		if err != nil {
			return RolloutResult{}, err
		}
		done := true
		for _, w := range ws {
			key := w.Kind + "/" + w.Name
			if s := w.String(); last[key] != s {
				last[key] = s
				if progress != nil {
					progress(w)
				}
			}
			done = done && w.Done()
		}
		if done {
			stepf("rollout: all %d workloads updated and ready", len(ws))
			return RolloutResult{Workloads: ws}, nil
		}
		if time.Now().After(deadline) {
			return RolloutResult{Workloads: ws, TimedOut: true}, nil
		}
		select {
		case <-ctx.Done():
			return RolloutResult{Workloads: ws, TimedOut: true}, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// releaseWorkloads returns the workloads Helm manages for the release (Helm
// annotates every resource it applies with meta.helm.sh/release-name).
func releaseWorkloads(ctx context.Context, cs kubernetes.Interface, ns, release string) ([]WorkloadStatus, error) {
	ours := func(m metav1.ObjectMeta) bool { return m.Annotations["meta.helm.sh/release-name"] == release }
	var out []WorkloadStatus
	deps, err := cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, d := range deps.Items {
		if !ours(d.ObjectMeta) {
			continue
		}
		want := int32(1)
		if d.Spec.Replicas != nil {
			want = *d.Spec.Replicas
		}
		w := WorkloadStatus{Kind: "Deployment", Name: d.Name, Desired: want, Updated: d.Status.UpdatedReplicas, Ready: d.Status.ReadyReplicas}
		if d.Status.ObservedGeneration < d.Generation {
			w.Updated = 0
		}
		out = append(out, withProblem(ctx, cs, ns, w, d.Spec.Selector))
	}
	sts, err := cs.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, s := range sts.Items {
		if !ours(s.ObjectMeta) {
			continue
		}
		want := int32(1)
		if s.Spec.Replicas != nil {
			want = *s.Spec.Replicas
		}
		w := WorkloadStatus{Kind: "StatefulSet", Name: s.Name, Desired: want, Updated: s.Status.UpdatedReplicas, Ready: s.Status.ReadyReplicas}
		if s.Status.ObservedGeneration < s.Generation {
			w.Updated = 0
		}
		out = append(out, withProblem(ctx, cs, ns, w, s.Spec.Selector))
	}
	dss, err := cs.AppsV1().DaemonSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, d := range dss.Items {
		if !ours(d.ObjectMeta) {
			continue
		}
		w := WorkloadStatus{Kind: "DaemonSet", Name: d.Name, Desired: d.Status.DesiredNumberScheduled,
			Updated: d.Status.UpdatedNumberScheduled, Ready: d.Status.NumberReady}
		if d.Status.ObservedGeneration < d.Generation {
			w.Updated = 0
		}
		out = append(out, withProblem(ctx, cs, ns, w, d.Spec.Selector))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind+out[i].Name < out[j].Kind+out[j].Name })
	return out, nil
}

// withProblem reads the workload's pods for why they are not ready.
func withProblem(ctx context.Context, cs kubernetes.Interface, ns string, w WorkloadStatus, sel *metav1.LabelSelector) WorkloadStatus {
	if w.Done() || sel == nil {
		return w
	}
	s, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil || s == labels.Nothing() {
		return w
	}
	pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: s.String()})
	if err != nil {
		return w
	}
	seen := map[string]bool{}
	var problems []string
	for _, p := range pods.Items {
		if pr := podProblem(p); pr != "" && !seen[pr] {
			seen[pr] = true
			problems = append(problems, pr)
		}
	}
	w.Problem = strings.Join(problems, "; ")
	return w
}

func podProblem(p corev1.Pod) string {
	if p.Status.Phase == corev1.PodPending {
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse && c.Reason != "" {
				return fmt.Sprintf("%s: %s", c.Reason, firstLine(c.Message))
			}
		}
	}
	for _, cs := range append(p.Status.InitContainerStatuses, p.Status.ContainerStatuses...) {
		if t := cs.LastTerminationState.Terminated; t != nil && t.Reason == "OOMKilled" {
			return fmt.Sprintf("%s OOMKilled (restarted %d times)", cs.Name, cs.RestartCount)
		}
		if wt := cs.State.Waiting; wt != nil && wt.Reason != "" && wt.Reason != "ContainerCreating" && wt.Reason != "PodInitializing" {
			return fmt.Sprintf("%s %s", cs.Name, wt.Reason)
		}
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// watchHooks, under --debug, reports while Helm waits on hook Jobs (the
// database migrations): each Job's pods and what they wait on, whenever that
// changes. Helm itself only says it is watching the Job. The returned
// function stops it.
func watchHooks(ctx context.Context, c *conn, ns string) func() {
	if !Debugging() {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		last := map[string]string{}
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Second):
			}
			jobs, err := c.clientset.BatchV1().Jobs(ns).List(ctx, metav1.ListOptions{})
			if err != nil {
				continue
			}
			for _, j := range jobs.Items {
				if j.Annotations["helm.sh/hook"] == "" || j.Status.Succeeded > 0 {
					continue
				}
				pods, err := c.clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "job-name=" + j.Name})
				if err != nil {
					continue
				}
				var states []string
				for _, p := range pods.Items {
					states = append(states, p.Name+" "+podWaitingOn(p))
				}
				msg := strings.Join(states, "; ")
				if msg == "" {
					msg = "no pods yet"
				}
				if last[j.Name] != msg {
					last[j.Name] = msg
					stepf("hook job %s: %s", j.Name, msg)
				}
			}
		}
	}()
	return cancel
}

// podWaitingOn says what a pod is waiting for: unschedulable, an init
// container still running, a container waiting with a reason.
func podWaitingOn(p corev1.Pod) string {
	if pr := podProblem(p); pr != "" {
		return string(p.Status.Phase) + ": " + pr
	}
	for i, cs := range p.Status.InitContainerStatuses {
		if cs.State.Terminated != nil && cs.State.Terminated.ExitCode == 0 {
			continue
		}
		return fmt.Sprintf("Init:%d/%d, init container %s running", i, len(p.Status.InitContainerStatuses), cs.Name)
	}
	return string(p.Status.Phase)
}
