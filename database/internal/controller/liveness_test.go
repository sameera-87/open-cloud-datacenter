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

	dbaasv1 "github.com/wso2/open-cloud-datacenter/crds/dbaas/api/v1alpha1"
	"github.com/wso2/open-cloud-datacenter/crds/dbaas/internal/harvester"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrlfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// degradedReason returns the reason of the Degraded condition, or "" if absent.
func degradedReason(inst *dbaasv1.DBInstance) string {
	for _, c := range inst.Status.Conditions {
		if c.Type == dbaasv1.ConditionDegraded && c.Status == metav1.ConditionTrue {
			return c.Reason
		}
	}
	return ""
}

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

// TestPhaseAvailableReadinessFailureSetsDegraded verifies the report-only
// behaviour: when the probe-debounced Ready condition is False (agent up), the
// instance is marked Degraded with PostgresUnreachable and the VM is NOT
// restarted. Phase stays available.
func TestPhaseAvailableReadinessFailureSetsDegraded(t *testing.T) {
	stub := &stubHarvester{
		// Agent up, probe failing → we KNOW PostgreSQL is down.
		readiness: harvester.VMIReadiness{Running: true, Ready: false, AgentConnected: true, IP: "10.0.0.5", VMIUID: "vmi-uid-abc"},
	}
	r, inst := newAvailableReconciler(stub)
	ctx := context.Background()

	if _, err := r.phaseAvailable(ctx, inst); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := degradedReason(inst); got != dbaasv1.ReasonPostgresUnreachable {
		t.Fatalf("Degraded reason = %q, want %q", got, dbaasv1.ReasonPostgresUnreachable)
	}
	if inst.Status.Phase != dbaasv1.StatusAvailable {
		t.Fatalf("Phase = %q, want %q (report-only — phase unchanged)", inst.Status.Phase, dbaasv1.StatusAvailable)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("VM must not be restarted on readiness failure (stop=%d start=%d)", stub.StopVMCalls, stub.StartVMCalls)
	}
}

// TestPhaseAvailableAgentDisconnectAttributed verifies that an agent disconnect
// is reported as GuestAgentDisconnected (health unknown), not as a PostgreSQL
// fault, and still does not restart the VM.
func TestPhaseAvailableAgentDisconnectAttributed(t *testing.T) {
	stub := &stubHarvester{
		// Agent down → probe can't run; Ready also False. Attribute to the agent.
		readiness: harvester.VMIReadiness{Running: true, Ready: false, AgentConnected: false, VMIUID: "vmi-uid-abc"},
	}
	r, inst := newAvailableReconciler(stub)
	ctx := context.Background()

	if _, err := r.phaseAvailable(ctx, inst); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := degradedReason(inst); got != dbaasv1.ReasonGuestAgentDisconnected {
		t.Fatalf("Degraded reason = %q, want %q", got, dbaasv1.ReasonGuestAgentDisconnected)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("VM must not be restarted on agent disconnect (stop=%d start=%d)", stub.StopVMCalls, stub.StartVMCalls)
	}
}

// TestPhaseAvailableHealthyClearsDegraded verifies that once the probe reports
// Ready again the Degraded condition is cleared, with no VM calls.
func TestPhaseAvailableHealthyClearsDegraded(t *testing.T) {
	stub := &stubHarvester{
		readiness: harvester.VMIReadiness{Running: true, Ready: false, AgentConnected: true, VMIUID: "vmi-uid-abc"},
	}
	r, inst := newAvailableReconciler(stub)
	ctx := context.Background()

	// One degraded cycle sets the condition.
	if _, err := r.phaseAvailable(ctx, inst); err != nil {
		t.Fatalf("degraded cycle error: %v", err)
	}
	if degradedReason(inst) == "" {
		t.Fatalf("Degraded condition not set after readiness failure")
	}

	// Probe recovers → Degraded must clear.
	stub.readiness = harvester.VMIReadiness{Running: true, Ready: true, AgentConnected: true, VMIUID: "vmi-uid-abc"}
	if _, err := r.phaseAvailable(ctx, inst); err != nil {
		t.Fatalf("healthy cycle error: %v", err)
	}
	if got := degradedReason(inst); got != "" {
		t.Fatalf("Degraded still set after recovery (reason=%q)", got)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("VM calls during report-only liveness (stop=%d start=%d)", stub.StopVMCalls, stub.StartVMCalls)
	}
}

// TestPhaseAvailableReadinessFetchErrorLeavesConditionUntouched is the RF-3
// regression: a failed VMI fetch is not a health signal, so the controller must
// not flip Degraded based on the zero-value readiness, and must not restart.
func TestPhaseAvailableReadinessFetchErrorLeavesConditionUntouched(t *testing.T) {
	stub := &stubHarvester{readinessErr: errors.New("apiserver timeout")}
	r, inst := newAvailableReconciler(stub)
	ctx := context.Background()

	if _, err := r.phaseAvailable(ctx, inst); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := degradedReason(inst); got != "" {
		t.Fatalf("Degraded set from a fetch error (reason=%q), want none", got)
	}
	if stub.StopVMCalls != 0 || stub.StartVMCalls != 0 {
		t.Fatalf("VM calls on fetch error (stop=%d start=%d), want none", stub.StopVMCalls, stub.StartVMCalls)
	}
}
