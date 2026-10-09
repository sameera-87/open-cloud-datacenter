package acctest

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestMain enables the -sweep flag (see sweep.go). Without -sweep it runs the tests normally.
func TestMain(m *testing.M) {
	resource.TestMain(m)
}
