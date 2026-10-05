// Unit tests for the List* functions used by the acceptance-test sweepers.
//
// DC-API doesn't document the list shape for these collections, so each function must
// accept both a bare JSON array and an {"items": [...]} wrapper (see decodeList).
package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// listServer returns a test server that answers GET wantPath with body, and fails
// the test on any other request.
func listServer(t *testing.T, wantPath, body string) *DCAPIClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != wantPath {
			t.Errorf("path = %s, want %s", r.URL.Path, wantPath)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	c, err := NewClient(server.URL, "test-token")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

// shapes is the two list encodings every List* function must accept.
var shapes = map[string]string{
	"bare array":    `[{"id":"a","name":"acc-x-1"},{"id":"b","name":"acc-x-2"}]`,
	"items wrapper": `{"items":[{"id":"a","name":"acc-x-1"},{"id":"b","name":"acc-x-2"}],"next_cursor":null}`,
}

func TestListFunctions_AcceptBothShapes(t *testing.T) {
	ctx := context.Background()
	const base = "/v1/tenants/t1/projects/p1"

	cases := []struct {
		name string
		path string
		call func(c *DCAPIClient) (ids []string, err error)
	}{
		{"ListVMs", base + "/virtual-machines", func(c *DCAPIClient) ([]string, error) {
			items, err := c.ListVMs(ctx, "t1", "p1")
			ids := []string{}
			for _, i := range items {
				ids = append(ids, i.ID)
			}
			return ids, err
		}},
		{"ListBastions", base + "/bastions", func(c *DCAPIClient) ([]string, error) {
			items, err := c.ListBastions(ctx, "t1", "p1")
			ids := []string{}
			for _, i := range items {
				ids = append(ids, i.ID)
			}
			return ids, err
		}},
		{"ListClusters", base + "/clusters", func(c *DCAPIClient) ([]string, error) {
			items, err := c.ListClusters(ctx, "t1", "p1")
			ids := []string{}
			for _, i := range items {
				ids = append(ids, i.ID)
			}
			return ids, err
		}},
		{"ListNodePools", base + "/clusters/c1/node-pools", func(c *DCAPIClient) ([]string, error) {
			items, err := c.ListNodePools(ctx, "t1", "p1", "c1")
			ids := []string{}
			for _, i := range items {
				ids = append(ids, i.ID)
			}
			return ids, err
		}},
		{"ListServiceAccounts", base + "/service-accounts", func(c *DCAPIClient) ([]string, error) {
			items, err := c.ListServiceAccounts(ctx, "t1", "p1")
			ids := []string{}
			for _, i := range items {
				ids = append(ids, i.ID)
			}
			return ids, err
		}},
		{"ListPrivateEndpoints", base + "/keyvaults/kv1/private-endpoints", func(c *DCAPIClient) ([]string, error) {
			items, err := c.ListPrivateEndpoints(ctx, "t1", "p1", "kv1")
			ids := []string{}
			for _, i := range items {
				ids = append(ids, i.ID)
			}
			return ids, err
		}},
	}

	for _, tc := range cases {
		for shapeName, body := range shapes {
			t.Run(tc.name+"/"+shapeName, func(t *testing.T) {
				c := listServer(t, tc.path, body)
				ids, err := tc.call(c)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
					t.Errorf("ids = %v, want [a b]", ids)
				}
			})
		}
	}
}

func TestDecodeList_RejectsUnknownShape(t *testing.T) {
	var out []VMReadResponse
	if err := decodeList([]byte(`{"vms":[]}`), &out); err == nil {
		t.Error("expected an error for an object without \"items\"")
	}
}
