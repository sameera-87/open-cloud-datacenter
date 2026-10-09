// Unit tests for the shared helpers in helpers.go.
//
// These two helpers are tiny but load-bearing: appendSet is how every resource
// funnels d.Set errors into a diag.Diagnostics without aborting the rest of the
// Set calls, and isNotFound is the single source of truth for "treat a delete of
// an already-gone object as success". Testing them in isolation keeps the resource
// tests focused on API wiring instead of re-proving this plumbing each time.
package resources

import (
	"errors"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// TestAppendSet_Success verifies the happy path: a d.Set that succeeds must append
// NO diagnostics and must actually store the value in state. We borrow ResourceTenant's
// schema purely as a convenient source of a real *schema.ResourceData — any resource
// schema with a settable string field would do.
func TestAppendSet_Success(t *testing.T) {
	d := schema.TestResourceDataRaw(t, ResourceTenant().Schema, nil)

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "name", "acme-corp")

	if diags.HasError() {
		t.Fatalf("appendSet on a valid field appended error diagnostics: %v", diags)
	}
	if len(diags) != 0 {
		t.Errorf("len(diags) = %d, want 0 on a successful Set", len(diags))
	}
	if got, want := d.Get("name").(string), "acme-corp"; got != want {
		t.Errorf("name = %q, want %q (value was not stored)", got, want)
	}
}

// TestAppendSet_Failure verifies that a failing d.Set (here: a key that doesn't exist
// in the schema) appends an error diagnostic, and that appendSet preserves any
// pre-existing diagnostics it was handed rather than clobbering them.
func TestAppendSet_Failure(t *testing.T) {
	d := schema.TestResourceDataRaw(t, ResourceTenant().Schema, nil)

	// Seed a pre-existing, non-error diagnostic so we can prove it survives.
	diags := diag.Diagnostics{
		{Severity: diag.Warning, Summary: "pre-existing warning"},
	}

	diags = appendSet(diags, d, "no_such_field", "x")

	if !diags.HasError() {
		t.Fatal("appendSet on an unknown key did not append an error diagnostic")
	}
	// The original warning must still be present: appendSet appends, never replaces.
	foundWarning := false
	for _, dg := range diags {
		if dg.Severity == diag.Warning && dg.Summary == "pre-existing warning" {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Errorf("pre-existing warning diagnostic was lost; diags = %v", diags)
	}
}

// TestIsNotFound covers the three cases Delete functions care about: a nil error is
// not a 404, an error carrying "HTTP 404" is, and any other HTTP error (e.g. 500) is
// not — so a real server failure still surfaces instead of being swallowed as "gone".
func TestIsNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"404 message", fmt.Errorf("DC-API returned HTTP 404: not found"), true},
		{"404 wrapped", fmt.Errorf("GetThing: %w", errors.New("DC-API returned HTTP 404: gone")), true},
		{"500 error", errors.New("DC-API returned HTTP 500: boom"), false},
		{"unrelated error", errors.New("connection refused"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isNotFound(tc.err); got != tc.want {
				t.Errorf("isNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
