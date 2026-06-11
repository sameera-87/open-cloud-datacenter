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
	"testing"
	"time"

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func hasCondition(inst *dbaasv1.DBInstance, condType string) bool {
	for _, c := range inst.Status.Conditions {
		if c.Type == condType {
			return true
		}
	}
	return false
}

// TestLivenessGuardIgnoresLifetimeRestartCount is the KI-006 Problem B
// regression: an instance with a large cumulative RestartCount (accumulated
// from KubeVirt auto-recoveries) must still get its full restart budget for a
// new unhealthy episode — not jump straight to failed.
func TestLivenessGuardIgnoresLifetimeRestartCount(t *testing.T) {
	stub := &stubHarvester{
		readiness: harvester.VMIReadiness{Running: false, Ready: false, AgentConnected: false, VMIUID: ""},
	}
	r, inst := newAvailableReconciler(stub)
	inst.Status.RestartCount = 6 // poisoned lifetime counter, double maxRestartCount
	ctx := context.Background()

	// Two unhealthy cycles reach the accelerated restartAt=2 threshold.
	for i := 0; i < 2; i++ {
		if _, err := r.phaseAvailable(ctx, inst); err != nil {
			t.Fatalf("cycle %d error: %v", i+1, err)
		}
	}
	if inst.Status.Phase == dbaasv1.StatusFailed {
		t.Fatalf("instance declared failed from lifetime RestartCount with zero attempts this episode")
	}
	if stub.StartVMCalls != 1 {
		t.Fatalf("StartVM called %d times, want 1 (restart budget must be episode-scoped)", stub.StartVMCalls)
	}
	if inst.Status.ConsecutiveRestartAttempts != 1 {
		t.Fatalf("ConsecutiveRestartAttempts = %d, want 1", inst.Status.ConsecutiveRestartAttempts)
	}
	if inst.Status.RestartCount != 7 {
		t.Fatalf("RestartCount = %d, want 7 (cumulative counter keeps counting)", inst.Status.RestartCount)
	}
}

// TestLivenessGuardFailsAfterConsecutiveAttempts verifies the guard fires on
// the episode counter and writes BOTH phase=failed and provisioningPhase=
// Failed (the RF-7 TODO), without attempting another restart.
func TestLivenessGuardFailsAfterConsecutiveAttempts(t *testing.T) {
	stub := &stubHarvester{
		readiness: harvester.VMIReadiness{Running: false, Ready: false, AgentConnected: false, VMIUID: ""},
	}
	r, inst := newAvailableReconciler(stub)
	inst.Status.ConsecutiveRestartAttempts = maxRestartCount // budget exhausted this episode
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		if _, err := r.phaseAvailable(ctx, inst); err != nil {
			t.Fatalf("cycle %d error: %v", i+1, err)
		}
	}
	if inst.Status.Phase != dbaasv1.StatusFailed {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusFailed)
	}
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseFailed {
		t.Fatalf("ProvisioningPhase = %q, want %q (RF-7 TODO must be completed)", inst.Status.ProvisioningPhase, dbaasv1.PhaseFailed)
	}
	if stub.StartVMCalls != 0 {
		t.Fatalf("StartVM called %d times, want 0 (budget exhausted)", stub.StartVMCalls)
	}
	if !hasCondition(inst, dbaasv1.ConditionFailed) {
		t.Fatalf("ConditionFailed not set")
	}
}

// TestFailedStateDoesNotHotLoop is the KI-007 regression: a failed instance
// must idle on 30s requeues — no counter increments, no status writes, no VM
// calls — instead of re-entering phaseAvailable on every status-write event.
func TestFailedStateDoesNotHotLoop(t *testing.T) {
	stub := &stubHarvester{} // VMI unhealthy (zero readiness)
	r, req := newStopStartReconciler(stub, true)
	ctx := context.Background()

	inst := getInst(t, r.Client)
	inst.Status.Phase = dbaasv1.StatusFailed
	inst.Status.ProvisioningPhase = dbaasv1.PhaseFailed
	inst.Status.ConsecutiveUnhealthyCount = 5
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatalf("status seed: %v", err)
	}

	for i := 0; i < 5; i++ {
		res, err := r.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("reconcile %d error: %v", i, err)
		}
		if res.RequeueAfter != 30*time.Second {
			t.Fatalf("reconcile %d: RequeueAfter = %v, want 30s", i, res.RequeueAfter)
		}
	}
	inst = getInst(t, r.Client)
	if inst.Status.ConsecutiveUnhealthyCount != 5 {
		t.Fatalf("ConsecutiveUnhealthyCount = %d, want 5 unchanged (hot loop must be dead)", inst.Status.ConsecutiveUnhealthyCount)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("VM calls while parked in failed (stop=%d start=%d), want none", stub.StopVMCalls, stub.StartVMCalls)
	}
}

// TestPhaseFailedRecoversWhenHealthy verifies the intentional recovery path:
// once the VMI reports fully healthy, the instance clears Failed, resets its
// episode counters, and re-enters the provisioning chain.
func TestPhaseFailedRecoversWhenHealthy(t *testing.T) {
	stub := &stubHarvester{
		readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, IP: "10.0.0.5", VMIUID: "vmi-uid-new"},
	}
	r, req := newStopStartReconciler(stub, true)
	ctx := context.Background()

	inst := getInst(t, r.Client)
	inst.Status.Phase = dbaasv1.StatusFailed
	inst.Status.ProvisioningPhase = dbaasv1.PhaseFailed
	inst.Status.ConsecutiveRestartAttempts = maxRestartCount
	inst.Status.ConsecutiveUnhealthyCount = 5
	inst.Status.Conditions = []metav1.Condition{{
		Type:               dbaasv1.ConditionFailed,
		Status:             metav1.ConditionTrue,
		Reason:             dbaasv1.ReasonMaxRestartsExceeded,
		Message:            "seeded",
		LastTransitionTime: metav1.Now(),
	}}
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatalf("status seed: %v", err)
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	inst = getInst(t, r.Client)
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseVMCreated {
		t.Fatalf("ProvisioningPhase = %q, want %q (re-enter provisioning chain)", inst.Status.ProvisioningPhase, dbaasv1.PhaseVMCreated)
	}
	if inst.Status.Phase != dbaasv1.StatusStarting {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusStarting)
	}
	if inst.Status.ConsecutiveRestartAttempts != 0 || inst.Status.ConsecutiveUnhealthyCount != 0 {
		t.Fatalf("episode counters not reset (attempts=%d unhealthy=%d)", inst.Status.ConsecutiveRestartAttempts, inst.Status.ConsecutiveUnhealthyCount)
	}
	if hasCondition(inst, dbaasv1.ConditionFailed) {
		t.Fatalf("ConditionFailed still present after recovery")
	}
}

// TestHealthyReconcileResetsRestartAttempts verifies the episode boundary: a
// fully healthy phaseAvailable pass zeroes ConsecutiveRestartAttempts so the
// next episode starts with a full budget.
func TestHealthyReconcileResetsRestartAttempts(t *testing.T) {
	stub := &stubHarvester{
		readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-abc"},
	}
	r, inst := newAvailableReconciler(stub)
	inst.Status.ConsecutiveRestartAttempts = 2
	ctx := context.Background()

	if _, err := r.phaseAvailable(ctx, inst); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inst.Status.ConsecutiveRestartAttempts != 0 {
		t.Fatalf("ConsecutiveRestartAttempts = %d after healthy reconcile, want 0", inst.Status.ConsecutiveRestartAttempts)
	}
}
