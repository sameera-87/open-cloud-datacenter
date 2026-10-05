// Unit tests for helpers.go.
//
// appendSet is the single shared helper every data source Read uses to funnel its
// d.Set calls through one error-collecting path. Its whole contract is: on a
// successful Set, return the diagnostics unchanged; on a failing Set, append a
// diagnostic so the Read can keep going and surface every bad field at once rather
// than bailing on the first. These tests pin both halves of that contract.
package datasources

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// newTestAppendSetData builds a *schema.ResourceData off DataSourceProject's schema
// purely as a convenient carrier of a few typed fields (a TypeString "name" and a
// TypeInt "cpu_cores") for exercising appendSet — the data source itself is irrelevant
// here, we just need a real ResourceData with known field types.
func newTestAppendSetData(t *testing.T) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, DataSourceProject().Schema, map[string]interface{}{})
}

// TestAppendSet_SuccessLeavesDiagsUnchanged verifies the happy path: setting a value
// whose Go type matches the schema field must not add any diagnostics, and the value
// must actually land in state.
func TestAppendSet_SuccessLeavesDiagsUnchanged(t *testing.T) {
	d := newTestAppendSetData(t)

	var diags diag.Diagnostics
	diags = appendSet(diags, d, "name", "my-project")

	if diags.HasError() {
		t.Fatalf("appendSet added error diagnostics on a valid Set: %v", diags)
	}
	if len(diags) != 0 {
		t.Errorf("len(diags) = %d, want 0 for a successful Set", len(diags))
	}
	if got, want := d.Get("name").(string), "my-project"; got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
}

// TestAppendSet_FailureAppendsDiag verifies the error path: setting a value whose Go
// type cannot be coerced into the schema field's type (a string into a TypeInt) must
// cause appendSet to append an error diagnostic rather than silently swallow it.
func TestAppendSet_FailureAppendsDiag(t *testing.T) {
	d := newTestAppendSetData(t)

	var diags diag.Diagnostics
	// cpu_cores is TypeInt; handing it a non-numeric string makes d.Set return an error.
	diags = appendSet(diags, d, "cpu_cores", "not-an-int")

	if !diags.HasError() {
		t.Fatal("appendSet did not append an error diagnostic for a type-mismatched Set")
	}
}

// TestAppendSet_PreservesExistingDiags verifies appendSet appends to, rather than
// replaces, whatever diagnostics were already accumulated — the Read loop relies on
// this to collect errors across many successive Set calls.
func TestAppendSet_PreservesExistingDiags(t *testing.T) {
	d := newTestAppendSetData(t)

	diags := diag.Diagnostics{diag.Diagnostic{Severity: diag.Error, Summary: "pre-existing"}}
	diags = appendSet(diags, d, "name", "ok") // a successful Set must keep the prior error

	if len(diags) != 1 {
		t.Errorf("len(diags) = %d, want 1 (existing error preserved, no new one added)", len(diags))
	}
}
