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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newAvailableReconciler returns a reconciler and a DBInstance wired up for
// phaseAvailable unit tests. The instance starts in Available phase with a
// known VMI UID, simulating a DB that was running before the test begins.
func newAvailableReconciler(stub *stubHarvester) (*DBInstanceReconciler, *dbaasv1.DBInstance) {
	inst := &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "tenant-a", ResourceVersion: "1"},
		Status: dbaasv1.DBInstanceStatus{
			Phase:             dbaasv1.StatusAvailable,
			ProvisioningPhase: dbaasv1.PhaseAvailable,
			LastKnownVMIUID:   "vmi-uid-abc",
			Resources:         dbaasv1.ResourceRefs{VMName: "pg-orders"},
		},
	}
	scheme := runtime.NewScheme()
	_ = dbaasv1.AddToScheme(scheme)
	fakeClient := ctrlfake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(inst).
		WithObjects(inst).
		Build()
	r := &DBInstanceReconciler{
		Client:    fakeClient,
		Harvester: stub,
		Recorder:  record.NewFakeRecorder(10),
	}
	return r, inst
}

// TestPhaseAvailableLivenessRestartsAfterThreshold verifies the sudo-poweroff
// scenario: VMI gone (AgentConnected=false, Running=false) fires StopVM+StartVM
// after restartAt = livenessRestartThreshold/livenessAgentAccelFactor = 2 cycles.
func TestPhaseAvailableLivenessRestartsAfterThreshold(t *testing.T) {
	stub := &stubHarvester{
		// VMI is gone: no UID, not running, guest agent unreachable
		readiness: harvester.VMIReadiness{Running: false, Ready: false, AgentConnected: false, VMIUID: ""},
	}
	r, inst := newAvailableReconciler(stub)
	ctx := context.Background()

	// Cycle 1: count=1, below restartAt=2 — no restart yet
	if _, err := r.phaseAvailable(ctx, inst); err != nil {
		t.Fatalf("cycle 1 error: %v", err)
	}
	if inst.Status.ConsecutiveUnhealthyCount != 1 {
		t.Fatalf("cycle 1: ConsecutiveUnhealthyCount = %d, want 1", inst.Status.ConsecutiveUnhealthyCount)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("cycle 1: StopVM/StartVM should not have been called yet (stop=%d start=%d)", stub.StopVMCalls, stub.StartVMCalls)
	}

	// Cycle 2: count=2, hits restartAt=2 — StopVM+StartVM must fire
	if _, err := r.phaseAvailable(ctx, inst); err != nil {
		t.Fatalf("cycle 2 error: %v", err)
	}
	if stub.StopVMCalls != 1 {
		t.Fatalf("cycle 2: StopVM called %d times, want 1", stub.StopVMCalls)
	}
	if stub.StartVMCalls != 1 {
		t.Fatalf("cycle 2: StartVM called %d times, want 1", stub.StartVMCalls)
	}
	if inst.Status.RestartCount != 1 {
		t.Fatalf("cycle 2: RestartCount = %d, want 1", inst.Status.RestartCount)
	}
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseVMCreated {
		t.Fatalf("cycle 2: ProvisioningPhase = %q, want %q", inst.Status.ProvisioningPhase, dbaasv1.PhaseVMCreated)
	}
	if inst.Status.ConsecutiveUnhealthyCount != 0 {
		t.Fatalf("cycle 2: ConsecutiveUnhealthyCount = %d, want 0 (reset after restart)", inst.Status.ConsecutiveUnhealthyCount)
	}
}

// TestLivenessRestartWaitsForVMITeardown verifies the restart sequence is
// StopVM → wait for the VMI to actually terminate → StartVM. KubeVirt's start
// subresource rejects while a VMI object exists, so calling StartVM
// back-to-back with StopVM fails with "VM is already running" and the retry
// loop spams events (observed on-cluster during the KI-006 ladder test).
func TestLivenessRestartWaitsForVMITeardown(t *testing.T) {
	stub := &stubHarvester{
		// VMI exists and is running, but the guest agent is down →
		// accelerated restartAt=2.
		readiness: harvester.VMIReadiness{Running: true, Ready: false, AgentConnected: false, VMIUID: "vmi-uid-abc"},
	}
	r, inst := newAvailableReconciler(stub)
	ctx := context.Background()

	// Cycle 1: below threshold.
	if _, err := r.phaseAvailable(ctx, inst); err != nil {
		t.Fatalf("cycle 1 error: %v", err)
	}

	// Cycles 2-3: threshold hit — StopVM fires, but the VMI still reports
	// Running (teardown in flight), so StartVM must NOT be called yet.
	for i := 2; i <= 3; i++ {
		res, err := r.phaseAvailable(ctx, inst)
		if err != nil {
			t.Fatalf("cycle %d error: %v", i, err)
		}
		if stub.StartVMCalls != 0 {
			t.Fatalf("cycle %d: StartVM called %d times during VMI teardown, want 0", i, stub.StartVMCalls)
		}
		if res.RequeueAfter != 5*time.Second {
			t.Fatalf("cycle %d: RequeueAfter = %v during teardown wait, want 5s", i, res.RequeueAfter)
		}
	}
	if stub.StopVMCalls == 0 {
		t.Fatalf("StopVM never called")
	}
	if inst.Status.RestartCount != 0 || inst.Status.ConsecutiveRestartAttempts != 0 {
		t.Fatalf("restart committed during teardown wait (restarts=%d attempts=%d)", inst.Status.RestartCount, inst.Status.ConsecutiveRestartAttempts)
	}

	// VMI finished terminating — next cycle completes the restart.
	stub.readiness = harvester.VMIReadiness{}
	if _, err := r.phaseAvailable(ctx, inst); err != nil {
		t.Fatalf("post-teardown cycle error: %v", err)
	}
	if stub.StartVMCalls != 1 {
		t.Fatalf("StartVM called %d times after teardown, want 1", stub.StartVMCalls)
	}
	if inst.Status.RestartCount != 1 || inst.Status.ConsecutiveRestartAttempts != 1 {
		t.Fatalf("restart not committed (restarts=%d attempts=%d)", inst.Status.RestartCount, inst.Status.ConsecutiveRestartAttempts)
	}
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseVMCreated {
		t.Fatalf("ProvisioningPhase = %q, want %q", inst.Status.ProvisioningPhase, dbaasv1.PhaseVMCreated)
	}
}

// TestPhaseAvailableLivenessDoesNotRestartBelowThreshold verifies that a single
// unhealthy cycle increments the counter but does not trigger StopVM/StartVM.
func TestPhaseAvailableLivenessDoesNotRestartBelowThreshold(t *testing.T) {
	stub := &stubHarvester{
		readiness: harvester.VMIReadiness{Running: false, Ready: false, AgentConnected: false, VMIUID: ""},
	}
	r, inst := newAvailableReconciler(stub)
	ctx := context.Background()

	if _, err := r.phaseAvailable(ctx, inst); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("StopVM/StartVM should not fire on first miss (stop=%d start=%d)", stub.StopVMCalls, stub.StartVMCalls)
	}
	if inst.Status.RestartCount != 0 {
		t.Fatalf("RestartCount = %d, want 0", inst.Status.RestartCount)
	}
}

// TestPhaseAvailableLivenessRecoveryResetsCounter verifies that a healthy cycle
// after misses resets ConsecutiveUnhealthyCount without triggering a restart.
func TestPhaseAvailableLivenessRecoveryResetsCounter(t *testing.T) {
	stub := &stubHarvester{
		readiness: harvester.VMIReadiness{Running: false, Ready: false, AgentConnected: false, VMIUID: ""},
	}
	r, inst := newAvailableReconciler(stub)
	ctx := context.Background()

	// One unhealthy cycle
	if _, err := r.phaseAvailable(ctx, inst); err != nil {
		t.Fatalf("unhealthy cycle error: %v", err)
	}
	if inst.Status.ConsecutiveUnhealthyCount != 1 {
		t.Fatalf("ConsecutiveUnhealthyCount = %d, want 1", inst.Status.ConsecutiveUnhealthyCount)
	}

	// VM recovers: guest agent reconnects with the same UID
	stub.readiness = harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-abc"}
	if _, err := r.phaseAvailable(ctx, inst); err != nil {
		t.Fatalf("healthy cycle error: %v", err)
	}
	if inst.Status.ConsecutiveUnhealthyCount != 0 {
		t.Fatalf("ConsecutiveUnhealthyCount = %d after recovery, want 0", inst.Status.ConsecutiveUnhealthyCount)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("StopVM/StartVM should never fire (stop=%d start=%d)", stub.StopVMCalls, stub.StartVMCalls)
	}
}
