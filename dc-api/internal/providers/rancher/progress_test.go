package rancher

import "testing"

// condition mirrors the struct shape used by conditionsToStatus + pickProgressMessage
// so the tests can construct fixtures without depending on Rancher's public types.
type condition = struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
}

func TestPickProgressMessage_PrefersReconcilingTrue(t *testing.T) {
	got := pickProgressMessage([]condition{
		{Type: "Provisioned", Status: "False", Message: "waiting for viable init node"},
		{Type: "Reconciling", Status: "True", Message: "configuring control plane"},
		{Type: "Updated", Status: "False", Message: "Updated is not true"},
	})
	if got != "configuring control plane" {
		t.Errorf("expected Reconciling=True message, got %q", got)
	}
}

func TestPickProgressMessage_FallsBackToFirstNonTrueWithMessage(t *testing.T) {
	got := pickProgressMessage([]condition{
		{Type: "Reconciling", Status: "True", Message: ""},                          // no message → skip
		{Type: "Provisioned", Status: "False", Message: "waiting for viable init node"},
		{Type: "Updated", Status: "False", Message: "later message"},
	})
	if got != "waiting for viable init node" {
		t.Errorf("expected first non-True+non-empty message, got %q", got)
	}
}

func TestPickProgressMessage_IgnoresStalled(t *testing.T) {
	got := pickProgressMessage([]condition{
		{Type: "Stalled", Status: "True", Message: "should not be picked (Stalled handled elsewhere)"},
		{Type: "Provisioned", Status: "False", Message: "real progress"},
	})
	if got != "real progress" {
		t.Errorf("expected Stalled to be skipped, got %q", got)
	}
}

func TestPickProgressMessage_EmptyWhenNothingInformative(t *testing.T) {
	got := pickProgressMessage([]condition{
		{Type: "Provisioned", Status: "True", Message: "ok"}, // True → skip
		{Type: "Updated", Status: "True", Message: "ok"},
	})
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestPickProgressMessage_HandlesEmptyConditions(t *testing.T) {
	if got := pickProgressMessage(nil); got != "" {
		t.Errorf("expected empty string for nil conditions, got %q", got)
	}
	if got := pickProgressMessage([]condition{}); got != "" {
		t.Errorf("expected empty string for empty conditions, got %q", got)
	}
}
