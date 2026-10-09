package acctest

// Plan: docs/testsuite/resources/dns-record.md

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"terraform-provider-dcapi/internal/client"
)

// TestAccDNSRecord_basic exercises the happy-path lifecycle of dcapi_dns_record on its own VNet
// and private DNS zone: it creates an A record (ttl omitted) and a CNAME, reads the A record
// back through the data source, imports both records, and deletes the A record out-of-band.
//
// PASSES when: the A record's ttl defaults to 300 and round-trips its single value, record_id is
// computed, the CNAME is created with type CNAME and a record_id, DC-API confirms the A record's
// values/ttl, the data source returns the same record_id/values/ttl as the resource, both
// records import cleanly by their 5-part state ID, and the out-of-band-deleted A record yields a
// non-empty plan.
// FAILS when: the ttl default is wrong, a value or computed field is missing, the API disagrees,
// the data source diverges from the resource, import drops a field, or Read ignores the 404.
func TestAccDNSRecord_basic(t *testing.T) {
	name := RandomName("dr")
	addr := "dcapi_dns_record.a"
	cfg := testAccDNSRecordZone(name) + testAccDNSRecordA("a", "app", []string{"10.206.0.5"}, 0) + `
resource "dcapi_dns_record" "cname" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  zone_id    = dcapi_private_dns_zone.z.zone_id
  name       = "app-alias"
  type       = "CNAME"
  values     = ["app.${dcapi_private_dns_zone.z.name}"]
}
`

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// CheckDestroy fails the test unless, after teardown, the record, zone and VNet all return
		// 404 (GetDnsRecord nil, or the zone gone).
		CheckDestroy: testAccDNSRecordCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create: apply the A + CNAME records and assert the Create/Read round-trip.
				// Passes only if the A record's ttl defaults to 300 (omitted in config), its single
				// value and record_id are set, the CNAME has type CNAME with a record_id, and
				// DC-API reports the same values/ttl.
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "ttl", "300"), // omitted → schema default
					resource.TestCheckResourceAttr(addr, "values.#", "1"),
					resource.TestCheckResourceAttr(addr, "values.0", "10.206.0.5"),
					resource.TestCheckResourceAttrSet(addr, "record_id"),
					resource.TestCheckResourceAttr("dcapi_dns_record.cname", "type", "CNAME"),
					resource.TestCheckResourceAttrSet("dcapi_dns_record.cname", "record_id"),
					CheckAPI(addr, checkDNSRecordAPI([]string{"10.206.0.5"}, 300)),
				),
			},
			{
				// Step 2 — Data source parity: add a data "dcapi_dns_record" that looks the A record
				// up by zone + name + type and assert record_id, values.# and ttl match the managed
				// resource. Fails if the data source's Read diverges from the resource's Read.
				Config: cfg + `
data "dcapi_dns_record" "a" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  zone_id    = dcapi_private_dns_zone.z.zone_id
  name       = dcapi_dns_record.a.name
  type       = "A"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPair("data.dcapi_dns_record.a", "record_id", addr, "record_id"),
					resource.TestCheckResourceAttrPair("data.dcapi_dns_record.a", "values.#", addr, "values.#"),
					resource.TestCheckResourceAttrPair("data.dcapi_dns_record.a", "ttl", addr, "ttl"),
				),
			},
			{
				// Step 3 — Import the A record by its 5-part state ID
				// (tenant/project/vnet/zone/record_id). ImportStateVerify fails if any attribute
				// differs after a fresh import.
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Step 4 — Import the CNAME record the same way, confirming the import ID works for
				// a second record type.
				ResourceName:      "dcapi_dns_record.cname",
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				// Step 5 — Disappears: delete the A record out-of-band, then re-plan. Read must drop
				// it from state and produce a non-empty recreate plan. Passes only if
				// ExpectNonEmptyPlan holds; fails if Read ignores the 404.
				Config:             cfg,
				Check:              Disappears(addr, DeleteDNSRecord),
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccDNSRecord_update verifies that values and ttl are updatable in place (not ForceNew): it
// grows the value list and lowers the ttl, then shrinks the list again, asserting an Update each
// time and a stable record_id across all three steps (sameID / ValuesSame).
//
// PASSES when: each change plans as an Update, the new values/ttl are stored and confirmed by
// DC-API, and record_id never changes between steps.
// FAILS when: a change is planned as a replacement, the record_id changes, or the stored/API
// values or ttl don't match the config.
func TestAccDNSRecord_update(t *testing.T) {
	name := RandomName("dr")
	addr := "dcapi_dns_record.a"
	sameID := statecheck.CompareValue(compare.ValuesSame())
	update := resource.ConfigPlanChecks{
		PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
	}
	zone := testAccDNSRecordZone(name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             testAccDNSRecordCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the baseline A record (one value, ttl 300) and capture its
				// record_id as the reference for the ValuesSame comparison.
				Config:            zone + testAccDNSRecordA("a", "app", []string{"10.206.0.5"}, 300),
				ConfigStateChecks: []statecheck.StateCheck{sameID.AddStateValue(addr, tfjsonpath.New("record_id"))},
			},
			{
				// Step 2 — Add a second value and lower ttl to 60. Must be an in-place Update with
				// the same record_id; DC-API must report both values and ttl 60.
				Config:           zone + testAccDNSRecordA("a", "app", []string{"10.206.0.5", "10.206.0.6"}, 60),
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "values.#", "2"),
					resource.TestCheckResourceAttr(addr, "ttl", "60"),
					CheckAPI(addr, checkDNSRecordAPI([]string{"10.206.0.5", "10.206.0.6"}, 60)),
				),
				ConfigStateChecks: []statecheck.StateCheck{sameID.AddStateValue(addr, tfjsonpath.New("record_id"))},
			},
			{
				// Step 3 — Shrink back to one value (ttl still 60). Again an in-place Update with
				// the same record_id; the API must report the single value.
				Config:           zone + testAccDNSRecordA("a", "app", []string{"10.206.0.5"}, 60),
				ConfigPlanChecks: update,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "values.#", "1"),
					CheckAPI(addr, checkDNSRecordAPI([]string{"10.206.0.5"}, 60)),
				),
				ConfigStateChecks: []statecheck.StateCheck{sameID.AddStateValue(addr, tfjsonpath.New("record_id"))},
			},
		},
	})
}

// TestAccDNSRecord_valuesOrdering (G4) guards against the DNS back end silently sorting a record
// set: it creates an A record whose values are in neither numeric nor lexical order and asserts
// each value stays at the exact index given in the config. (The implicit post-apply empty-plan
// check also fails if the order drifts.)
//
// PASSES when: values.0/.1/.2 match the config order exactly and the apply leaves an empty plan.
// FAILS when: the back end reorders the set, so a value lands at a different index or a perpetual
// diff remains — the symptom this test is designed to catch.
func TestAccDNSRecord_valuesOrdering(t *testing.T) {
	name := RandomName("dr")
	values := []string{"10.206.0.20", "10.206.0.3", "10.206.0.100"}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		CheckDestroy:             testAccDNSRecordCheckDestroy(),
		Steps: []resource.TestStep{
			{
				// Create with deliberately unordered values; assert each position matches the config.
				Config: testAccDNSRecordZone(name) + testAccDNSRecordA("a", "app", values, 300),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("dcapi_dns_record.a", "values.0", values[0]),
					resource.TestCheckResourceAttr("dcapi_dns_record.a", "values.1", values[1]),
					resource.TestCheckResourceAttr("dcapi_dns_record.a", "values.2", values[2]),
				),
			},
		},
	})
}

// TestAccDNSRecord_forceNew verifies that name is ForceNew: changing the record's name must
// destroy-then-recreate it rather than update in place. The IDSet records each record_id so
// CheckAllGone can confirm the replaced record was really deleted.
//
// PASSES when: renaming "app" to "app2" plans a DestroyBeforeCreate, and at teardown every
// recorded record_id returns a 404.
// FAILS when: the rename is treated as an in-place update, or a replaced record is left alive on
// the API.
func TestAccDNSRecord_forceNew(t *testing.T) {
	name := RandomName("dr")
	addr := "dcapi_dns_record.a"
	ids := &IDSet{}
	zone := testAccDNSRecordZone(name)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { PreCheck(t) },
		ProtoV5ProviderFactories: ProviderFactories,
		// Every recorded record_id must be gone (404) by the end.
		CheckDestroy: ids.CheckAllGone(DNSRecordExists),
		Steps: []resource.TestStep{
			{
				// Step 1 — Create the baseline record named "app" and record its first record_id.
				Config: zone + testAccDNSRecordA("a", "app", []string{"10.206.0.5"}, 300),
				Check:  ids.Record(addr),
			},
			{
				// Step 2 — Rename to "app2". ForceNew means this must replace the record: the plan
				// check requires DestroyBeforeCreate and a new record_id is recorded.
				Config: zone + testAccDNSRecordA("a", "app2", []string{"10.206.0.5"}, 300),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionDestroyBeforeCreate)},
				},
				Check: ids.Record(addr),
			},
		},
	})
}

// testAccDNSRecordZone declares the test's own VNet and zone "z".
func testAccDNSRecordZone(name string) string {
	return ConfigBase() + ConfigNetwork(name, CIDRDNSRecordVNet, "") + fmt.Sprintf(`
resource "dcapi_private_dns_zone" "z" {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  name       = "%s.acc.internal"
}
`, name)
}

// testAccDNSRecordA declares an A record in zone "z". ttl 0 leaves ttl out of the config.
func testAccDNSRecordA(key, recordName string, values []string, ttl int) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	ttlLine := ""
	if ttl != 0 {
		ttlLine = fmt.Sprintf("ttl        = %d", ttl)
	}
	return fmt.Sprintf(`
resource "dcapi_dns_record" %q {
  tenant_id  = local.tenant_id
  project_id = local.project_id
  vnet_id    = dcapi_vnet.parent.vnet_uuid
  zone_id    = dcapi_private_dns_zone.z.zone_id
  name       = %q
  type       = "A"
  values     = [%s]
  %s
}
`, key, recordName, strings.Join(quoted, ", "), ttlLine)
}

func testAccDNSRecordCheckDestroy() resource.TestCheckFunc {
	return resource.ComposeTestCheckFunc(
		CheckDestroy("dcapi_dns_record", DNSRecordExists),
		CheckDestroy("dcapi_private_dns_zone", PrivateDNSZoneExists),
		CheckDestroy("dcapi_vnet", VNetExists),
	)
}

func checkDNSRecordAPI(wantValues []string, wantTTL int) APICheckFunc {
	return func(ctx context.Context, c *client.DCAPIClient, p []string) error {
		r, err := c.GetDnsRecord(ctx, p[0], p[1], p[2], p[3], p[4])
		if err != nil {
			return err
		}
		if r == nil {
			return fmt.Errorf("record not found")
		}
		if fmt.Sprint(r.Values) != fmt.Sprint(wantValues) || r.TTL != wantTTL {
			return fmt.Errorf("values/ttl = %v/%d, want %v/%d", r.Values, r.TTL, wantValues, wantTTL)
		}
		return nil
	}
}
