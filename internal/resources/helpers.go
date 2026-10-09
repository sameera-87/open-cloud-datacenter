// Shared helpers used across multiple resource files in this package.
package resources

import (
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// appendSet calls d.Set and appends any error into the diagnostics slice.
func appendSet(diags diag.Diagnostics, d *schema.ResourceData, key string, val interface{}) diag.Diagnostics {
	if err := d.Set(key, val); err != nil {
		diags = append(diags, diag.FromErr(err)...)
	}
	return diags
}

// isNotFound reports whether a client error is DC-API's HTTP 404: the object is already gone.
// Delete functions treat that as success, so destroying something that was deleted outside
// Terraform (or by a destroy run with -refresh=false) doesn't fail.
func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 404")
}