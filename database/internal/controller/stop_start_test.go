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

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func boolPtr(b bool) *bool { return &b }

// newStopStartReconciler builds a reconciler around a fake client holding an
// Available instance with the finalizer already present, so Reconcile goes
// straight to the dispatcher branches.
func newStopStartReconciler(stub *stubHarvester, running bool) (*DBInstanceReconciler, ctrl.Request) {
	inst := &dbaasv1.DBInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "orders",
			Namespace:  "tenant-a",
			Finalizers: []string{dbaasv1.FinalizerName},
		},
		Spec: dbaasv1.DBInstanceSpec{Running: boolPtr(running)},
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
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "orders", Namespace: "tenant-a"}}
	return r, req
}

func getInst(t *testing.T, c client.Client) *dbaasv1.DBInstance {
	t.Helper()
	var inst dbaasv1.DBInstance
	if err := c.Get(context.Background(), types.NamespacedName{Name: "orders", Namespace: "tenant-a"}, &inst); err != nil {
		t.Fatalf("get instance: %v", err)
	}
	return &inst
}

// TestStopMovesToProvisioningPhaseStopped verifies reconcileStop writes
// phase=stopped AND provisioningPhase=Stopped in one step — the KI-005 fix.
func TestStopMovesToProvisioningPhaseStopped(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-abc"}}
	r, req := newStopStartReconciler(stub, false)
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if stub.StopVMCalls != 1 {
		t.Fatalf("StopVM called %d times, want 1", stub.StopVMCalls)
	}
	inst := getInst(t, r.Client)
	if inst.Status.Phase != dbaasv1.StatusStopped {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusStopped)
	}
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseStopped {
		t.Fatalf("ProvisioningPhase = %q, want %q", inst.Status.ProvisioningPhase, dbaasv1.PhaseStopped)
	}
}

// TestStoppedInstanceIsNotResurrected verifies the RF-1 loop is dead: further
// reconciles of a stopped instance must not re-enter phaseAvailable, stamp
// phase=available, or start the VM.
func TestStoppedInstanceIsNotResurrected(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-abc"}}
	r, req := newStopStartReconciler(stub, false)
	ctx := context.Background()

	// First reconcile performs the stop.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("stop reconcile error: %v", err)
	}
	// VMI is now gone — this used to make phaseAvailable escalate to a restart.
	stub.readiness = harvester.VMIReadiness{}

	for i := 0; i < 5; i++ {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatalf("idle reconcile %d error: %v", i, err)
		}
	}
	if stub.StopVMCalls != 1 {
		t.Fatalf("StopVM called %d times, want 1 (no resurrection loop)", stub.StopVMCalls)
	}
	if stub.StartVMCalls != 0 {
		t.Fatalf("StartVM called %d times, want 0 (no liveness restart while stopped)", stub.StartVMCalls)
	}
	inst := getInst(t, r.Client)
	if inst.Status.Phase != dbaasv1.StatusStopped {
		t.Fatalf("Phase = %q after idle reconciles, want %q", inst.Status.Phase, dbaasv1.StatusStopped)
	}
}

// TestStartReentersProvisioningChain verifies that flipping running back to
// true starts the VM and hands off to phaseWaitReady (provisioningPhase=
// VMCreated) instead of declaring available immediately.
func TestStartReentersProvisioningChain(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-abc"}}
	r, req := newStopStartReconciler(stub, false)
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("stop reconcile error: %v", err)
	}
	// VMI finished terminating after the stop.
	stub.readiness = harvester.VMIReadiness{}

	// User flips running back to true.
	inst := getInst(t, r.Client)
	inst.Spec.Running = boolPtr(true)
	if err := r.Update(ctx, inst); err != nil {
		t.Fatalf("spec update: %v", err)
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("start reconcile error: %v", err)
	}
	if stub.StartVMCalls != 1 {
		t.Fatalf("StartVM called %d times, want 1", stub.StartVMCalls)
	}
	inst = getInst(t, r.Client)
	if inst.Status.Phase != dbaasv1.StatusStarting {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusStarting)
	}
	if inst.Status.ProvisioningPhase != dbaasv1.PhaseVMCreated {
		t.Fatalf("ProvisioningPhase = %q, want %q (re-enter phaseWaitReady)", inst.Status.ProvisioningPhase, dbaasv1.PhaseVMCreated)
	}
	if inst.Status.LastKnownVMIUID != "" {
		t.Fatalf("LastKnownVMIUID = %q, want cleared (planned start must not count as unplanned restart)", inst.Status.LastKnownVMIUID)
	}
}

// TestStartWaitsForVMITeardown verifies reconcileStart does not call StartVM
// while the previous VMI is still terminating (KubeVirt rejects start while a
// VMI object exists).
func TestStartWaitsForVMITeardown(t *testing.T) {
	stub := &stubHarvester{readiness: harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-abc"}}
	r, req := newStopStartReconciler(stub, false)
	ctx := context.Background()

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("stop reconcile error: %v", err)
	}
	// VMI still reports Running (teardown in flight).
	inst := getInst(t, r.Client)
	inst.Spec.Running = boolPtr(true)
	if err := r.Update(ctx, inst); err != nil {
		t.Fatalf("spec update: %v", err)
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("start reconcile error: %v", err)
	}
	if stub.StartVMCalls != 0 {
		t.Fatalf("StartVM called %d times while VMI still running, want 0", stub.StartVMCalls)
	}
	inst = getInst(t, r.Client)
	if inst.Status.Phase != dbaasv1.StatusStopped {
		t.Fatalf("Phase = %q during teardown wait, want %q", inst.Status.Phase, dbaasv1.StatusStopped)
	}

	// VMI finishes terminating — next reconcile starts the VM.
	stub.readiness = harvester.VMIReadiness{}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second start reconcile error: %v", err)
	}
	if stub.StartVMCalls != 1 {
		t.Fatalf("StartVM called %d times after teardown, want 1", stub.StartVMCalls)
	}
}

// TestPhaseStoppedRecoversStuckStoppingPhase verifies the structural recovery
// property for the KI-005 trap: even if a legacy instance persisted
// phase=stopping (which the fixed reconcileStop can no longer produce), the
// dispatcher reaches phaseStopped via provisioningPhase=Stopped and routes a
// running=true spec to reconcileStart instead of dead-ending.
func TestPhaseStoppedRecoversStuckStoppingPhase(t *testing.T) {
	stub := &stubHarvester{}
	r, req := newStopStartReconciler(stub, true)
	ctx := context.Background()

	// Force the historical stuck shape: phase=stopping, provisioningPhase=Stopped.
	inst := getInst(t, r.Client)
	inst.Status.Phase = dbaasv1.StatusStopping
	inst.Status.ProvisioningPhase = dbaasv1.PhaseStopped
	if err := r.Status().Update(ctx, inst); err != nil {
		t.Fatalf("status seed: %v", err)
	}

	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if stub.StartVMCalls != 1 {
		t.Fatalf("StartVM called %d times, want 1 (phaseStopped must route stuck instance to start)", stub.StartVMCalls)
	}
	inst = getInst(t, r.Client)
	if inst.Status.Phase != dbaasv1.StatusStarting {
		t.Fatalf("Phase = %q, want %q", inst.Status.Phase, dbaasv1.StatusStarting)
	}
}
