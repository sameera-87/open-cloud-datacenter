/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newLivenessReconciler wires up a reconciler with a fake event recorder and
// a fake k8s client pre-populated with inst. The inst is returned for assertion.
func newLivenessReconciler(stub *stubHarvester, inst *dbaasv1.DBInstance) (*DBInstanceReconciler, *record.FakeRecorder) {
	scheme := runtime.NewScheme()
	_ = dbaasv1.AddToScheme(scheme)
	fakeClient := ctrlfake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(inst).
		WithObjects(inst).
		Build()
	rec := record.NewFakeRecorder(16)
	r := &DBInstanceReconciler{Client: fakeClient, Harvester: stub, Recorder: rec}
	return r, rec
}

func newAvailableInst() *dbaasv1.DBInstance {
	return &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "tenant-a", ResourceVersion: "1"},
		Status: dbaasv1.DBInstanceStatus{
			Phase:             dbaasv1.StatusAvailable,
			ProvisioningPhase: dbaasv1.PhaseAvailable,
			LastKnownVMIUID:   "uid-1",
			Resources:         dbaasv1.ResourceRefs{VMName: "pg-orders"},
			Endpoint:          &dbaasv1.Endpoint{Address: "192.168.40.50", Port: 5432},
		},
	}
}

// healthyReadiness returns a fully healthy VMIReadiness snapshot.
func healthyReadiness() harvester.VMIReadiness {
	return harvester.VMIReadiness{
		Running:        true,
		IP:             "192.168.40.50",
		Ready:          true,
		AgentConnected: true,
		VMIUID:         "uid-1",
	}
}

// --- UID / unplanned restart ---

func TestPhaseAvailableSnapshotsUIDOnFirstEntry(t *testing.T) {
	inst := newAvailableInst()
	inst.Status.LastKnownVMIUID = "" // simulate first entry
	stub := &stubHarvester{readiness: healthyReadiness()}
	r, _ := newLivenessReconciler(stub, inst)

	_, err := r.phaseAvailable(context.Background(), inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst.Status.LastKnownVMIUID != "uid-1" {
		t.Fatalf("LastKnownVMIUID = %q, want uid-1", inst.Status.LastKnownVMIUID)
	}
}

func TestPhaseAvailableDetectsUnplannedRestart(t *testing.T) {
	inst := newAvailableInst()
	inst.Status.LastKnownVMIUID = "uid-OLD"
	stub := &stubHarvester{readiness: healthyReadiness()} // VMIUID is "uid-1" ≠ "uid-OLD"
	r, rec := newLivenessReconciler(stub, inst)

	result, err := r.phaseAvailable(context.Background(), inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 10*time.Second {
		t.Fatalf("RequeueAfter = %v, want 10s after unplanned restart", result.RequeueAfter)
	}
	if inst.Status.RestartCount != 1 {
		t.Fatalf("RestartCount = %d, want 1", inst.Status.RestartCount)
	}
	if inst.Status.LastKnownVMIUID != "uid-1" {
		t.Fatalf("LastKnownVMIUID = %q, want uid-1", inst.Status.LastKnownVMIUID)
	}
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseVMCreated {
		t.Fatalf("ProvisioningPhase = %q, want PhaseVMCreated", inst.Status.ProvisioningPhase)
	}
	assertEvent(t, rec, dbaasv1.ReasonVMRestarting)
}

// --- Healthy path ---

func TestPhaseAvailableHealthyResetsUnhealthyCount(t *testing.T) {
	inst := newAvailableInst()
	inst.Status.ConsecutiveUnhealthyCount = 2
	setCondition(inst, dbaasv1.ConditionDegraded, metav1.ConditionTrue, dbaasv1.ReasonPostgresUnreachable, "degraded")
	stub := &stubHarvester{readiness: healthyReadiness()}
	r, _ := newLivenessReconciler(stub, inst)

	_, err := r.phaseAvailable(context.Background(), inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst.Status.ConsecutiveUnhealthyCount != 0 {
		t.Fatalf("ConsecutiveUnhealthyCount = %d, want 0 after healthy reconcile", inst.Status.ConsecutiveUnhealthyCount)
	}
	for _, c := range inst.Status.Conditions {
		if c.Type == dbaasv1.ConditionDegraded {
			t.Fatalf("Degraded condition should have been removed on healthy reconcile")
		}
	}
}

// --- Warning events ---

func TestPhaseAvailableEmitsWarningAtWarnThreshold(t *testing.T) {
	inst := newAvailableInst()
	inst.Status.ConsecutiveUnhealthyCount = livenessWarnThreshold - 1
	stub := &stubHarvester{readiness: harvester.VMIReadiness{
		Running: true, IP: "192.168.40.50", Ready: false, AgentConnected: true, VMIUID: "uid-1",
	}}
	r, rec := newLivenessReconciler(stub, inst)

	_, _ = r.phaseAvailable(context.Background(), inst)

	if inst.Status.ConsecutiveUnhealthyCount != livenessWarnThreshold {
		t.Fatalf("ConsecutiveUnhealthyCount = %d, want %d", inst.Status.ConsecutiveUnhealthyCount, livenessWarnThreshold)
	}
	assertEvent(t, rec, dbaasv1.ReasonPostgresUnreachable)
}

// --- Degraded condition ---

func TestPhaseAvailableSetsDegradedAtThreshold(t *testing.T) {
	inst := newAvailableInst()
	inst.Status.ConsecutiveUnhealthyCount = livenessDegradedThreshold - 1
	stub := &stubHarvester{readiness: harvester.VMIReadiness{
		Running: true, IP: "192.168.40.50", Ready: false, AgentConnected: true, VMIUID: "uid-1",
	}}
	r, _ := newLivenessReconciler(stub, inst)

	_, _ = r.phaseAvailable(context.Background(), inst)

	found := false
	for _, c := range inst.Status.Conditions {
		if c.Type == dbaasv1.ConditionDegraded && c.Status == metav1.ConditionTrue {
			found = true
		}
	}
	if !found {
		t.Fatalf("Degraded condition not set at threshold %d", livenessDegradedThreshold)
	}
}

// --- Restart initiation ---

func TestPhaseAvailableInitiatesRestartAtRestartThreshold(t *testing.T) {
	inst := newAvailableInst()
	inst.Status.ConsecutiveUnhealthyCount = livenessRestartThreshold - 1
	stub := &stubHarvester{readiness: harvester.VMIReadiness{
		Running: true, IP: "192.168.40.50", Ready: false, AgentConnected: true, VMIUID: "uid-1",
	}}
	r, rec := newLivenessReconciler(stub, inst)

	result, err := r.phaseAvailable(context.Background(), inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 10*time.Second {
		t.Fatalf("RequeueAfter = %v, want 10s after restart initiation", result.RequeueAfter)
	}
	if inst.Status.RestartCount != 1 {
		t.Fatalf("RestartCount = %d, want 1", inst.Status.RestartCount)
	}
	if inst.Status.ConsecutiveUnhealthyCount != 0 {
		t.Fatalf("ConsecutiveUnhealthyCount = %d, want 0 after restart", inst.Status.ConsecutiveUnhealthyCount)
	}
	if inst.Status.LastKnownVMIUID != "" {
		t.Fatalf("LastKnownVMIUID = %q, want empty after restart so phaseAvailable re-snapshots", inst.Status.LastKnownVMIUID)
	}
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseVMCreated {
		t.Fatalf("ProvisioningPhase = %q, want PhaseVMCreated", inst.Status.ProvisioningPhase)
	}
	assertEvent(t, rec, dbaasv1.ReasonVMRestarting)
}

func TestPhaseAvailableAcceleratesRestartWhenAgentDisconnected(t *testing.T) {
	acceleratedThreshold := livenessRestartThreshold / livenessAgentAccelFactor
	inst := newAvailableInst()
	inst.Status.ConsecutiveUnhealthyCount = acceleratedThreshold - 1
	stub := &stubHarvester{readiness: harvester.VMIReadiness{
		Running: false, IP: "", Ready: false, AgentConnected: false, VMIUID: "uid-1",
	}}
	r, rec := newLivenessReconciler(stub, inst)

	result, _ := r.phaseAvailable(context.Background(), inst)

	if result.RequeueAfter != 10*time.Second {
		t.Fatalf("RequeueAfter = %v, want 10s: agent disconnected should trigger accelerated restart at count %d",
			result.RequeueAfter, acceleratedThreshold)
	}
	assertEvent(t, rec, dbaasv1.ReasonVMRestarting)
}

// --- Terminal failure ---

func TestPhaseAvailableSetsFailedWhenMaxRestartsExceeded(t *testing.T) {
	inst := newAvailableInst()
	inst.Status.RestartCount = maxRestartCount
	inst.Status.ConsecutiveUnhealthyCount = livenessRestartThreshold - 1
	stub := &stubHarvester{readiness: harvester.VMIReadiness{
		Running: false, IP: "", Ready: false, AgentConnected: false, VMIUID: "uid-1",
	}}
	r, _ := newLivenessReconciler(stub, inst)

	result, err := r.phaseAvailable(context.Background(), inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != (ctrl.Result{}) {
		t.Fatalf("result = %v, want empty (stop requeuing)", result)
	}
	if inst.Status.Phase != dbaasv1.StatusFailed {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusFailed)
	}
	found := false
	for _, c := range inst.Status.Conditions {
		if c.Type == dbaasv1.ConditionFailed && c.Status == metav1.ConditionTrue &&
			c.Reason == dbaasv1.ReasonMaxRestartsExceeded {
			found = true
		}
	}
	if !found {
		t.Fatalf("Failed condition with reason MaxRestartsExceeded not set")
	}
}

func TestPhaseAvailableStopVMFailureDoesNotResetUnhealthyCount(t *testing.T) {
	inst := newAvailableInst()
	inst.Status.ConsecutiveUnhealthyCount = livenessRestartThreshold - 1
	stub := &stubHarvester{
		readiness: harvester.VMIReadiness{
			Running: true, IP: "192.168.40.50", Ready: false, AgentConnected: true, VMIUID: "uid-1",
		},
		stopVMErr: errors.New("harvester API unavailable"),
	}
	r, rec := newLivenessReconciler(stub, inst)

	result, err := r.phaseAvailable(context.Background(), inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Must requeue (short interval to retry), not the normal 60 s cycle.
	if result.RequeueAfter == 0 || result.RequeueAfter == 60*time.Second {
		t.Fatalf("RequeueAfter = %v, want a short retry interval", result.RequeueAfter)
	}
	// Counter must NOT be reset — next reconcile must retry the restart.
	if inst.Status.ConsecutiveUnhealthyCount != livenessRestartThreshold {
		t.Fatalf("ConsecutiveUnhealthyCount = %d, want %d (not reset after StopVM failure)",
			inst.Status.ConsecutiveUnhealthyCount, livenessRestartThreshold)
	}
	// RestartCount must NOT be incremented — the restart did not happen.
	if inst.Status.RestartCount != 0 {
		t.Fatalf("RestartCount = %d, want 0 after StopVM failure", inst.Status.RestartCount)
	}
	// ProvisioningPhase must stay in phaseAvailable — do NOT enter phaseWaitReady.
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseAvailable {
		t.Fatalf("ProvisioningPhase = %q, want %q after StopVM failure",
			inst.Status.ProvisioningPhase, dbaasv1.PhaseAvailable)
	}
	assertEvent(t, rec, dbaasv1.ReasonVMRestarting)
}

func TestPhaseAvailableStartVMFailureDoesNotIncrementRestartCount(t *testing.T) {
	inst := newAvailableInst()
	inst.Status.ConsecutiveUnhealthyCount = livenessRestartThreshold - 1
	stub := &stubHarvester{
		readiness: harvester.VMIReadiness{
			Running: true, IP: "192.168.40.50", Ready: false, AgentConnected: true, VMIUID: "uid-1",
		},
		startVMErr: errors.New("KubeVirt admission webhook timeout"),
	}
	r, rec := newLivenessReconciler(stub, inst)

	result, err := r.phaseAvailable(context.Background(), inst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Must requeue with a short interval to retry StartVM.
	if result.RequeueAfter == 0 || result.RequeueAfter == 60*time.Second {
		t.Fatalf("RequeueAfter = %v, want a short retry interval", result.RequeueAfter)
	}
	// RestartCount must NOT be incremented — stop succeeded but start did not.
	if inst.Status.RestartCount != 0 {
		t.Fatalf("RestartCount = %d, want 0 after StartVM failure", inst.Status.RestartCount)
	}
	// Counter must NOT be reset — next reconcile must retry (StopVM no-op + StartVM again).
	if inst.Status.ConsecutiveUnhealthyCount != livenessRestartThreshold {
		t.Fatalf("ConsecutiveUnhealthyCount = %d, want %d (not reset after StartVM failure)",
			inst.Status.ConsecutiveUnhealthyCount, livenessRestartThreshold)
	}
	// ProvisioningPhase must stay in phaseAvailable — phaseWaitReady cannot call StartVM.
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseAvailable {
		t.Fatalf("ProvisioningPhase = %q, want %q after StartVM failure",
			inst.Status.ProvisioningPhase, dbaasv1.PhaseAvailable)
	}
	assertEvent(t, rec, dbaasv1.ReasonVMRestarting)
}

// assertEvent drains one event from rec and checks it contains the expected reason.
func assertEvent(t *testing.T, rec *record.FakeRecorder, reason string) {
	t.Helper()
	select {
	case ev := <-rec.Events:
		if ev == "" {
			t.Fatalf("received empty event, want reason %q", reason)
		}
		// FakeRecorder format: "<EventType> <reason> <message>"
		for _, part := range []string{reason} {
			found := false
			for _, word := range splitWords(ev) {
				if word == part {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("event %q does not contain %q", ev, part)
			}
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("no event emitted, want reason %q", reason)
	}
}

func splitWords(s string) []string {
	var words []string
	start := -1
	for i, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			if start >= 0 {
				words = append(words, s[start:i])
				start = -1
			}
		} else if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		words = append(words, s[start:])
	}
	return words
}
