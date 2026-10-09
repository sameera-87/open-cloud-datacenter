package acctest

// Plan: docs/testsuite/resources/data-sources.md
//
// Resource-backed data sources (vnet, subnet, nsg, …) are tested inside their resource's
// basic test. This file covers the ones that read objects existing before the suite runs.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	sdkacctest "github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestAccDataSource_region reads the dcapi_region platform data source by name. It also doubles
// as a cheap end-to-end auth check, since a successful read proves the SA credentials work.
//
// PASSES when: the data source returns name equal to the configured region, with status set and
// at least one zone (zones.0.name populated).
// FAILS when: auth fails, the region isn't found, or any of those fields is empty.
func TestAccDataSource_region(t *testing.T) {
	addr := "data.dcapi_region.test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Read the region by name and assert name, status and the first zone are returned.
				Config: ConfigBase() + `
data "dcapi_region" "test" {
  name = local.region
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "name", Region()),
					resource.TestCheckResourceAttrSet(addr, "status"),
					resource.TestCheckResourceAttrSet(addr, "zones.0.name"),
				),
			},
		},
	})
}

// TestAccDataSource_image reads the dcapi_image data source by display name (the lookup key),
// resolving the VM image the compute tests depend on. If this fails, fix the image parameters
// first: the VM and cluster tests in the same run will fail too.
//
// PASSES when: the data source resolves the display name to an image with image_id set and
// namespace equal to the namespace part of DCAPI_ACC_VM_IMAGE ("namespace/name").
// FAILS when: the display name doesn't resolve, image_id is empty, or the namespace mapping
// differs from what the resource-style identifier implies.
func TestAccDataSource_image(t *testing.T) {
	displayName := RequireEnv(t, "DCAPI_ACC_VM_IMAGE_DISPLAY_NAME")
	vmImage := RequireEnv(t, "DCAPI_ACC_VM_IMAGE")
	addr := "data.dcapi_image.vm"

	// resources take "namespace/name"; the data source looks up by display name.
	namespace, _, _ := strings.Cut(vmImage, "/")

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Look the image up by display name and assert image_id is set and namespace
				// matches the namespace segment of the resource-style identifier.
				Config: ConfigBase() + fmt.Sprintf(`
data "dcapi_image" "vm" {
  tenant_id    = local.tenant_id
  display_name = %q
}
`, displayName),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(addr, "image_id"),
					resource.TestCheckResourceAttr(addr, "namespace", namespace),
				),
			},
		},
	})
}

// TestAccDataSource_project reads the dcapi_project scope data source. The PreCheck probes the
// API first and, if the SA gets HTTP 403, skips the test with a reason so the permission model
// stays visible in the results instead of failing opaquely.
//
// PASSES when: the SA can read the project and the data source returns project_id equal to the
// configured ID with project_uuid and the cpu_cores quota field populated.
// FAILS when: the read succeeds but any of those fields is wrong or empty. (A 403 skips, not
// fails.)
func TestAccDataSource_project(t *testing.T) {
	addr := "data.dcapi_project.test"

	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			PreCheck(t)
			skipIfForbidden(t, "dcapi_project", func(ctx context.Context) error {
				c, err := NewAPIClient()
				if err != nil {
					return err
				}
				_, err = c.GetProjectByID(ctx, TenantID(), ProjectID())
				return err
			})
		},
		ProtoV5ProviderFactories: ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Read the project by tenant_id/project_id and assert the ID round-trips and the
				// computed project_uuid and cpu_cores quota field are set.
				Config: ConfigBase() + `
data "dcapi_project" "test" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "project_id", ProjectID()),
					resource.TestCheckResourceAttrSet(addr, "project_uuid"),
					resource.TestCheckResourceAttrSet(addr, "cpu_cores"),
				),
			},
		},
	})
}

// TestAccDataSource_tenant reads the dcapi_tenant scope data source. It is expected to be
// skipped: a project-scoped SA probably can't read its tenant, so the PreCheck probe records the
// 403 and skips, keeping the permission model visible.
//
// PASSES when: the SA can read the tenant and the data source returns id equal to the configured
// tenant ID.
// FAILS when: the read succeeds but the id doesn't match. (A 403 skips, not fails.)
func TestAccDataSource_tenant(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			PreCheck(t)
			skipIfForbidden(t, "dcapi_tenant", func(ctx context.Context) error {
				c, err := NewAPIClient()
				if err != nil {
					return err
				}
				_, err = c.GetTenantByID(ctx, TenantID())
				return err
			})
		},
		ProtoV5ProviderFactories: ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Read the tenant by id and assert it round-trips to the configured tenant ID.
				Config: ConfigBase() + `
data "dcapi_tenant" "test" {
  id = local.tenant_id
}
`,
				Check: resource.TestCheckResourceAttr("data.dcapi_tenant.test", "id", TenantID()),
			},
		},
	})
}

// TestAccDataSource_notFound confirms a data source lookup of a missing object fails loudly
// instead of returning an empty object: it reads dcapi_vnet by a randomized name that can't
// exist.
//
// PASSES when: the read errors with a message matching "no VNet named".
// FAILS when: the lookup returns an empty/zero object with no error, or errors with a different
// message.
func TestAccDataSource_notFound(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		Steps: []resource.TestStep{
			{
				// Look up a VNet by a random non-existent name; ExpectError requires the "no VNet
				// named" failure rather than a silent empty result.
				Config: ConfigBase() + fmt.Sprintf(`
data "dcapi_vnet" "missing" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  name       = "acc-does-not-exist-%s"
}
`, sdkacctest.RandStringFromCharSet(8, sdkacctest.CharSetAlphaNum)),
				ExpectError: ExpectErr("no VNet named"),
			},
		},
	})
}

// skipIfForbidden skips the test with a reason when probe gets HTTP 403.
func skipIfForbidden(t *testing.T, what string, probe func(ctx context.Context) error) {
	t.Helper()
	if err := probe(context.Background()); err != nil && strings.Contains(err.Error(), "HTTP 403") {
		t.Skipf("the service account may not read %s (HTTP 403); skipping", what)
	}
}
