// Unit tests for image.go (data source).
//
// Images are tenant-scoped (not project-scoped): ListImages hits GET .../tenants/{t}/images
// (bare array) and the data source filters by display_name. Match sets a two-part Id
// "tenant_id/image_id", where image_id is the composite "namespace/resource-name". No
// match is a hard error.
package datasources

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"terraform-provider-dcapi/internal/client"
)

func newTestImageDSData(t *testing.T, raw map[string]interface{}) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, DataSourceImage().Schema, raw)
}

// TestDataSourceImage_Schema pins tenant_id/display_name as Required lookups and
// image_id/namespace as Computed.
func TestDataSourceImage_Schema(t *testing.T) {
	s := DataSourceImage().Schema

	for _, key := range []string{"tenant_id", "display_name"} {
		if f := s[key]; f == nil || !f.Required {
			t.Errorf("%s: want a Required lookup field, got %+v", key, f)
		}
	}
	for _, key := range []string{"image_id", "namespace"} {
		f := s[key]
		if f == nil {
			t.Fatalf("schema is missing field %q", key)
		}
		if !f.Computed {
			t.Errorf("%s: Computed = false, want true", key)
		}
		if f.Required || f.Optional {
			t.Errorf("%s: Required=%v Optional=%v, want both false", key, f.Required, f.Optional)
		}
	}
}

// TestDataSourceImageRead_Success verifies the display_name filter, composite Id, and
// that image_id/namespace are copied.
func TestDataSourceImageRead_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/tenants/acme/images" {
			t.Errorf("path = %s, want /v1/tenants/acme/images", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[
			{"id":"ns/win","display_name":"Windows","namespace":"ns"},
			{"id":"library/ubuntu-2204","display_name":"Ubuntu 22.04","namespace":"library"}
		]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestImageDSData(t, map[string]interface{}{"tenant_id": "acme", "display_name": "Ubuntu 22.04"})

	diags := dataSourceImageRead(context.Background(), d, c)
	if diags.HasError() {
		t.Fatalf("dataSourceImageRead returned unexpected error diagnostics: %v", diags)
	}
	if got, want := d.Id(), "acme/library/ubuntu-2204"; got != want {
		t.Errorf("Id() = %q, want %q", got, want)
	}
	if got, want := d.Get("image_id").(string), "library/ubuntu-2204"; got != want {
		t.Errorf("image_id = %q, want %q", got, want)
	}
	if got, want := d.Get("namespace").(string), "library"; got != want {
		t.Errorf("namespace = %q, want %q", got, want)
	}
}

// TestDataSourceImageRead_NotFound verifies an unmatched display_name is a hard error.
func TestDataSourceImageRead_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`[{"id":"ns/win","display_name":"Windows","namespace":"ns"}]`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestImageDSData(t, map[string]interface{}{"tenant_id": "acme", "display_name": "Ghost"})

	diags := dataSourceImageRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for an image display_name not in the list")
	}
}

// TestDataSourceImageRead_APIError verifies a failed list request surfaces as an error.
func TestDataSourceImageRead_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	c, _ := client.NewClient(server.URL, "test-token")
	d := newTestImageDSData(t, map[string]interface{}{"tenant_id": "acme", "display_name": "Ubuntu 22.04"})

	diags := dataSourceImageRead(context.Background(), d, c)
	if !diags.HasError() {
		t.Fatal("diags.HasError() = false, want true for a 500 response")
	}
}
