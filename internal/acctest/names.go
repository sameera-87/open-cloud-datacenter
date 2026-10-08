package acctest

import (
	"fmt"

	sdkacctest "github.com/hashicorp/terraform-plugin-testing/helper/acctest"
)

// RandomName returns "acc-<run>-<short>-<rand5>", e.g. "acc-k3f9q-nsg-x7a2m".
//
//	acc-   : cleanup prefix (docs/testsuite/06). NEVER reuse this prefix for anything long-lived.
//	<run>  : DCAPI_ACC_RUN_ID — lets the sweep step find exactly this run's leftovers.
//	<short>: resource abbreviation, for people reading DC-API listings.
//	<rand5>: uniqueness across tests and retries within a run.
func RandomName(short string) string {
	return fmt.Sprintf("%s%s-%s", RunPrefix(), short, sdkacctest.RandStringFromCharSet(5, sdkacctest.CharSetAlphaNum))
}

// RunPrefix is the prefix every object created by this run starts with: "acc-<run>-".
func RunPrefix() string {
	return "acc-" + RunID() + "-"
}
