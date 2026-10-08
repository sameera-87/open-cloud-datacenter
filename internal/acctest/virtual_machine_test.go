package acctest

// Plan: docs/testsuite/resources/virtual-machine.md
//
// No import step: the VM GET doesn't return image_name, disk_gb or the network fields, so an
// imported VM would plan a replacement (G2).

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-dcapi/internal/client"
)

// TestAccVirtualMachine_vpc exercises the happy-path lifecycle of a VPC-mode dcapi_virtual_machine
// (created in its own VNet+subnet): create and capture the one-time secrets, confirm a refresh keeps
// those secrets in state, and out-of-band deletion. There is no import step (see the file header:
// the VM GET omits image_name/disk_gb/network fields, so an import would plan a replacement, G2).
//
// PASSES when: the VM reaches ACTIVE with an IP inside the subnet's 10.208.1.0/24 range, private_key
// and console_password are populated (and the key looks like a PEM), provider_type is set, DC-API
// agrees the VM is ACTIVE with a matching IP (CheckAPI), a refresh leaves both secrets unchanged,
// and a VM deleted out-of-band forces a non-empty plan.
// FAILS when: the VM isn't ACTIVE, the IP is outside the expected range, a secret/provider_type is
// empty, the API disagrees, a refresh drops or rewrites the one-time secrets, or Read misses the 404.
func TestAccVirtualMachine_vpc(t *testing.T) {
	name := RandomName("vm")
	addr := "dcapi_virtual_machine.test"
	image := RequireEnv(t, "DCAPI_ACC_VM_IMAGE")
	cfg := testAccVMConfig(name, image, "small")
	privateKey, password := &AttrSnapshot{}, &AttrSnapshot{}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// CheckDestroy fails unless the VM and its parent subnet and VNet all return a 404.
		CheckDestroy: testAccVMCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create: apply the config and assert the Create/Read round-trip. Passes
				// only if the VM is ACTIVE with an in-range IP, the one-time secrets and
				// provider_type are set, and the API confirms the VM (CheckAPI). The secrets are
				// snapshotted for the next step.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "status", "ACTIVE"),
					resource.TestMatchResourceAttr(addr, "ip_address", regexp.MustCompile(`^10\.208\.1\.\d+$`)),
					resource.TestMatchResourceAttr(addr, "private_key", regexp.MustCompile(`BEGIN .*PRIVATE KEY`)),
					resource.TestCheckResourceAttrSet(addr, "console_password"),
					resource.TestCheckResourceAttrSet(addr, "provider_type"),
					privateKey.Save(addr, "private_key"),
					password.Save(addr, "console_password"),
					CheckAPI(addr, checkVMAPI()),
				),
			},
			{
				// Step 2 — Refresh: the secrets are shown by the API only once, so Read must preserve
				// them. Passes only if both the private_key and console_password are unchanged from
				// the values captured in Step 1; fails if Read blanks or rewrites them.
				RefreshState: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					privateKey.Unchanged(addr, "private_key"),
					password.Unchanged(addr, "console_password"),
				),
			},
			{
				// Step 3 — Disappears: delete the VM out-of-band, then re-plan. Read must drop it
				// from state and produce a non-empty plan; fails if the 404 is ignored.
				Config:             cfg,
				Check:              Disappears(addr, DeleteVM),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccVirtualMachine_invalidNetwork checks the mutually-exclusive networking-mode validation.
// The validation runs in Create before any API call (G6), so these applies fail without creating
// anything. The configs use dummy IDs and no VNet. Once G6 moves the check to CustomizeDiff, these
// steps can become PlanOnly.
//
// PASSES when: each config is rejected with its specific error — mixing network_name with vnet/subnet
// gives "not both", supplying no networking gives "one networking mode is required", and giving only
// vnet_id (VPC mode, incomplete) gives "VPC mode requires BOTH vnet_id and subnet_id".
// FAILS when: any config is accepted, or it errors with a different message than expected.
func TestAccVirtualMachine_invalidNetwork(t *testing.T) {
	cfg := func(network string) string {
		return ConfigBase() + fmt.Sprintf(`
resource "dcapi_virtual_machine" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  name       = "acc-invalid-network"
  size       = "small"
  image_name = "acc/not-used"
  %s
}
`, network)
	}
	const dummy = "00000000-0000-0000-0000-000000000000"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Step 1 — Both modes at once (network_name plus vnet_id/subnet_id): rejected with
				// "not both".
				Config:      cfg(fmt.Sprintf("network_name = \"iaas/net\"\n  vnet_id = %q\n  subnet_id = %q", dummy, dummy)),
				ExpectError: ExpectErr("not both"),
			},
			{
				// Step 2 — No networking at all: rejected with "one networking mode is required".
				Config:      cfg(""),
				ExpectError: ExpectErr("one networking mode is required"),
			},
			{
				// Step 3 — Incomplete VPC mode (vnet_id without subnet_id): rejected with
				// "VPC mode requires BOTH vnet_id and subnet_id".
				Config:      cfg(fmt.Sprintf("vnet_id = %q", dummy)),
				ExpectError: ExpectErr("VPC mode requires BOTH vnet_id and subnet_id"),
			},
		},
	})
}

// TestAccVirtualMachine_validation checks the size schema validator rejects an unsupported value at
// plan time, before any API call.
//
// PASSES when: planning a config with size "tiny" fails with a "to be one of" validation error.
// FAILS when: the invalid size is accepted, or the plan errors with a different message.
func TestAccVirtualMachine_validation(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Single step — PlanOnly: an invalid "tiny" size must be rejected by the validator
				// during plan; passes only on the "to be one of" error.
				Config: ConfigBase() + `
resource "dcapi_virtual_machine" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  name       = "acc-validation"
  size       = "tiny"
  image_name = "acc/not-used"
  vnet_id    = "00000000-0000-0000-0000-000000000000"
  subnet_id  = "00000000-0000-0000-0000-000000000000"
}
`,
				PlanOnly:    true,
				ExpectError: ExpectErr("to be one of"),
			},
		},
	})
}

// TestAccVirtualMachine_forceNewSize verifies size is ForceNew: a resize is a replacement, not an
// in-place update. The old VM must be destroyed-then-recreated, and the new one must come with a
// fresh one-time private_key. The IDSet records every VM id so CheckAllGone can confirm the
// replaced VM was really deleted.
//
// PASSES when: the size change plans a DestroyBeforeCreate, the new size lands in state, the
// private_key differs from the previous step, and at teardown every recorded VM (plus the parent
// subnet and VNet) returns a 404.
// FAILS when: the resize is treated as an in-place update, the private_key is reused, or a replaced
// VM is left alive on the API.
func TestAccVirtualMachine_forceNewSize(t *testing.T) {
	name := RandomName("vm")
	addr := "dcapi_virtual_machine.test"
	image := RequireEnv(t, "DCAPI_ACC_VM_IMAGE")
	ids := &IDSet{}
	// ValuesDiffer asserts the private_key at this step differs from the one captured last step.
	newKey := statecheck.CompareValue(compare.ValuesDiffer())

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// Every recorded VM id must be gone (404), and so must the parent subnet and VNet.
		CheckDestroy: resource.ComposeTestCheckFunc(ids.CheckAllGone(VMExists), testAccVMCheckDestroy()),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the small VM and record its first id and private_key.
				Config:            testAccVMConfig(name, image, "small"),
				Check:             ids.Record(addr),
				ConfigStateChecks: []statecheck.StateCheck{newKey.AddStateValue(addr, tfjsonpath.New("private_key"))},
			},
			{
				// Step 2 — Resize to medium. ForceNew means this must replace the VM: the plan check
				// requires DestroyBeforeCreate, the new size must be in state, and the private_key
				// must differ from Step 1's (a freshly minted secret).
				Config: testAccVMConfig(name, image, "medium"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					ids.Record(addr),
					resource.TestCheckResourceAttr(addr, "size", "medium"),
				),
				ConfigStateChecks: []statecheck.StateCheck{newKey.AddStateValue(addr, tfjsonpath.New("private_key"))},
			},
		},
	})
}

// TestAccVirtualMachine_legacyNetwork exercises legacy bridge networking (network_name mode instead
// of VPC vnet_id/subnet_id). It runs only where legacy networks are still offered, so it skips
// unless DCAPI_ACC_LEGACY_NETWORK names one.
//
// PASSES when: the env var is set and the VM created with network_name reaches ACTIVE.
// FAILS when: the VM in legacy mode does not become ACTIVE (when the env var is unset the test is
// skipped, not failed).
func TestAccVirtualMachine_legacyNetwork(t *testing.T) {
	network := os.Getenv("DCAPI_ACC_LEGACY_NETWORK")
	if network == "" {
		t.Skip("DCAPI_ACC_LEGACY_NETWORK not set; skipping the legacy network_name test")
	}
	name := RandomName("vm")
	image := RequireEnv(t, "DCAPI_ACC_VM_IMAGE")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             CheckDestroy("dcapi_virtual_machine", VMExists),
		Steps: []resource.TestStep{
			{
				// Single step — Create a VM in legacy network_name mode and assert it reaches ACTIVE.
				Config: ConfigBase() + fmt.Sprintf(`
resource "dcapi_virtual_machine" "test" {
  tenant_id    = local.tenant_id
  project_id   = local.project_id
  name         = %q
  size         = "small"
  disk_gb      = 20
  image_name   = %q
  network_name = %q
}
`, name, image, network),
				Check: resource.TestCheckResourceAttr("dcapi_virtual_machine.test", "status", "ACTIVE"),
			},
		},
	})
}

func testAccVMConfig(name, image, size string) string {
	return ConfigBase() + ConfigNetwork(name, CIDRVirtualMachineVNet, CIDRVirtualMachineSubnet) + fmt.Sprintf(`
resource "dcapi_virtual_machine" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  name       = %q
  size       = %q
  disk_gb    = 20
  image_name = %q
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  subnet_id  = dcapi_subnet.parent.subnet_uuid
}
`, name, size, image)
}

func testAccVMCheckDestroy() resource.TestCheckFunc {
	return resource.ComposeTestCheckFunc(
		CheckDestroy("dcapi_virtual_machine", VMExists),
		CheckDestroy("dcapi_subnet", SubnetExists),
		CheckDestroy("dcapi_vnet", VNetExists),
	)
}

func checkVMAPI() APICheckFunc {
	return func(ctx context.Context, c *client.DCAPIClient, p []string) error {
		vm, err := c.GetVM(ctx, p[0], p[1], p[2])
		if err != nil {
			return err
		}
		if vm == nil {
			return fmt.Errorf("VM not found")
		}
		if vm.Status != "ACTIVE" || !regexp.MustCompile(`^10\.208\.1\.\d+$`).MatchString(vm.IPAddress) {
			return fmt.Errorf("status/ip = %s/%s, want ACTIVE and an address in %s", vm.Status, vm.IPAddress, CIDRVirtualMachineSubnet)
		}
		return nil
	}
}
