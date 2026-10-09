// Unit tests for provider.go.
//
// These tests live in "package provider" (not "package provider_test") because
// configureProvider is unexported — a test can only reach it when compiled as part
// of the same package.
//
// Two distinct things are under test here:
//
//  1. New() — the provider definition. The single most valuable test for any
//     terraform-plugin-sdk provider is schema.Provider.InternalValidate(): it walks
//     every resource and data source schema and fails on the structural mistakes the
//     compiler can't catch (a field that is both Required and Computed, a missing
//     Type, a resource with no Read func, etc.). Wiring a new resource into
//     ResourcesMap without this test could ship a provider that panics at plan time;
//     InternalValidate turns that into a fast, offline unit-test failure.
//
//  2. configureProvider — credential validation. It must reject a missing endpoint
//     or token with an error diagnostic (never a nil-but-silent success), and build a
//     *client.DCAPIClient when both are present. Because endpoint/token fall back to
//     DCAPI_ENDPOINT / DCAPI_TOKEN env vars via EnvDefaultFunc, every test clears
//     those env vars with t.Setenv so a developer's real environment can't make the
//     "missing credential" cases spuriously pass.
package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

// TestProvider_InternalValidate is the structural sanity check for the whole
// provider: it validates the provider schema plus every registered resource and
// data source schema. If any schema in internal/resources or internal/datasources
// is malformed, this fails offline instead of at `terraform plan` time.
func TestProvider_InternalValidate(t *testing.T) {
	if err := New().InternalValidate(); err != nil {
		t.Fatalf("provider InternalValidate() failed: %v", err)
	}
}

// TestProvider_Schema checks the two provider-level config fields exist with the
// properties the rest of the provider relies on: both optional (so env-var fallback
// is allowed) and token marked Sensitive (so Terraform redacts it from logs/output).
func TestProvider_Schema(t *testing.T) {
	s := New().Schema

	for _, key := range []string{"endpoint", "token"} {
		f := s[key]
		if f == nil {
			t.Fatalf("provider schema is missing field %q", key)
		}
		if !f.Optional {
			t.Errorf("%s: Optional = false, want true (must allow env-var fallback)", key)
		}
	}

	if !s["token"].Sensitive {
		t.Error("token: Sensitive = false, want true (bearer token must be redacted)")
	}
}

// TestProvider_RegistersExpectedResources guards the ResourcesMap and DataSourcesMap
// wiring: a resource silently dropped from (or renamed in) the map would break user
// configs with no compile error. Each expected .tf type name is asserted present.
func TestProvider_RegistersExpectedResources(t *testing.T) {
	p := New()

	wantResources := []string{
		"dcapi_tenant", "dcapi_project", "dcapi_vnet", "dcapi_subnet",
		"dcapi_virtual_machine", "dcapi_service_account", "dcapi_bastion",
		"dcapi_cluster", "dcapi_node_pool", "dcapi_route_table",
		"dcapi_route_table_association", "dcapi_network_security_group",
		"dcapi_nsg_attachment", "dcapi_key_vault", "dcapi_vnet_peering",
		"dcapi_private_dns_zone", "dcapi_dns_record", "dcapi_private_endpoint",
		"dcapi_tenant_member", "dcapi_key_vault_secret",
	}
	for _, name := range wantResources {
		if p.ResourcesMap[name] == nil {
			t.Errorf("ResourcesMap is missing resource %q", name)
		}
	}
	if got, want := len(p.ResourcesMap), len(wantResources); got != want {
		t.Errorf("ResourcesMap has %d resources, want %d (map and expected list drifted)", got, want)
	}

	wantDataSources := []string{
		"dcapi_tenant", "dcapi_project", "dcapi_vnet", "dcapi_subnet",
		"dcapi_route_table", "dcapi_network_security_group", "dcapi_key_vault",
		"dcapi_private_dns_zone", "dcapi_dns_record", "dcapi_vnet_peering",
		"dcapi_region", "dcapi_image",
	}
	for _, name := range wantDataSources {
		if p.DataSourcesMap[name] == nil {
			t.Errorf("DataSourcesMap is missing data source %q", name)
		}
	}
	if got, want := len(p.DataSourcesMap), len(wantDataSources); got != want {
		t.Errorf("DataSourcesMap has %d data sources, want %d (map and expected list drifted)", got, want)
	}
}

// configData builds a *schema.ResourceData from the provider's own schema, the same
// way Terraform core hands configured provider config to ConfigureContextFunc.
func configData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, New().Schema, raw)
}

// clearProviderEnv unsets the two env vars endpoint/token fall back to, so the
// "missing credential" tests can't be made to pass by a developer's real shell env.
func clearProviderEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DCAPI_ENDPOINT", "")
	t.Setenv("DCAPI_TOKEN", "")
}

// TestConfigureProvider_Success verifies that with both endpoint and token present,
// configureProvider returns a non-nil *client.DCAPIClient and no error diagnostics.
func TestConfigureProvider_Success(t *testing.T) {
	clearProviderEnv(t)

	d := configData(t, map[string]interface{}{
		"endpoint": "https://dcapi.example.com",
		"token":    "dcapi_sa_abc_secret",
	})

	meta, diags := configureProvider(context.Background(), d)
	if diags.HasError() {
		t.Fatalf("configureProvider returned unexpected error diagnostics: %v", diags)
	}
	if _, ok := meta.(*client.DCAPIClient); !ok {
		t.Fatalf("meta = %T, want *client.DCAPIClient", meta)
	}
}

// TestConfigureProvider_MissingCredentials verifies that an absent endpoint and/or
// token each produces an error diagnostic and a nil client — Terraform must never
// proceed to call resources with a half-configured client.
func TestConfigureProvider_MissingCredentials(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]interface{}
	}{
		{"both missing", map[string]interface{}{}},
		{"missing endpoint", map[string]interface{}{"token": "dcapi_sa_abc_secret"}},
		{"missing token", map[string]interface{}{"endpoint": "https://dcapi.example.com"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearProviderEnv(t)
			d := configData(t, tc.raw)

			meta, diags := configureProvider(context.Background(), d)
			if !diags.HasError() {
				t.Fatal("diags.HasError() = false, want true for missing credentials")
			}
			if meta != nil {
				t.Errorf("meta = %v, want nil when configuration is incomplete", meta)
			}
		})
	}
}
