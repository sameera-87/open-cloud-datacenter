// Unit tests for dns_record.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

const dnsRecordsBase = "/v1/tenants/t1/projects/p1/vnets/vn1/dns-zones/z1/records"

// TestUpsertDnsRecord guards the POST (upsert) path, body forwarding, and parse.
func TestUpsertDnsRecord(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != dnsRecordsBase {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, dnsRecordsBase)
		}
		var got DnsRecordUpsertRequest
		decodeBody(t, r, &got)
		if got.Name != "www" || got.Type != "A" || len(got.Values) != 1 {
			t.Errorf("body = %+v, want name=www type=A one value", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"r-1","zone_id":"z1","name":"www","type":"A","values":["10.0.0.1"],"ttl":300}`))
	})

	rec, err := c.UpsertDnsRecord(context.Background(), "t1", "p1", "vn1", "z1",
		DnsRecordUpsertRequest{Name: "www", Type: "A", Values: []string{"10.0.0.1"}, TTL: 300})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.ID != "r-1" || len(rec.Values) != 1 || rec.Values[0] != "10.0.0.1" {
		t.Fatalf("record = %+v, want id r-1 value 10.0.0.1", rec)
	}
}

// TestUpsertDnsRecord_Error surfaces an API failure.
func TestUpsertDnsRecord_Error(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad type"}`))
	})
	if _, err := c.UpsertDnsRecord(context.Background(), "t1", "p1", "vn1", "z1", DnsRecordUpsertRequest{}); err == nil {
		t.Fatal("err = nil, want an error for HTTP 400")
	}
}

// TestListDnsRecords checks the bare-array parse used by the data source's
// client-side (name,type) filter.
func TestListDnsRecords(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != dnsRecordsBase {
			t.Errorf("path = %s, unexpected", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"r-1","name":"www","type":"A"},{"id":"r-2","name":"db","type":"CNAME"}]`))
	})
	recs, err := c.ListDnsRecords(context.Background(), "t1", "p1", "vn1", "z1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(recs) != 2 || recs[1].Name != "db" {
		t.Fatalf("records = %+v, want two records", recs)
	}
}

// TestGetDnsRecord covers the 200 / 404-sentinel outcomes.
func TestGetDnsRecord(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != dnsRecordsBase+"/r-1" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"r-1","name":"www","type":"A","ttl":60}`))
		})
		got, err := c.GetDnsRecord(ctx, "t1", "p1", "vn1", "z1", "r-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || got.TTL != 60 {
			t.Fatalf("got = %+v, want ttl 60", got)
		}
	})

	t.Run("404 returns nil,nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		got, err := c.GetDnsRecord(ctx, "t1", "p1", "vn1", "z1", "missing")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil on 404", got, err)
		}
	})
}

// TestUpdateDnsRecord guards the PUT path and full-replace body.
func TestUpdateDnsRecord(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != dnsRecordsBase+"/r-1" {
			t.Errorf("got %s %s, want PUT .../r-1", r.Method, r.URL.Path)
		}
		var got DnsRecordUpdateRequest
		decodeBody(t, r, &got)
		if len(got.Values) != 2 {
			t.Errorf("values = %v, want two", got.Values)
		}
		_, _ = w.Write([]byte(`{"id":"r-1","values":["10.0.0.1","10.0.0.2"],"ttl":120}`))
	})
	rec, err := c.UpdateDnsRecord(context.Background(), "t1", "p1", "vn1", "z1", "r-1",
		DnsRecordUpdateRequest{Values: []string{"10.0.0.1", "10.0.0.2"}, TTL: 120})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rec.Values) != 2 {
		t.Fatalf("record values = %v, want two", rec.Values)
	}
}

// TestDeleteDnsRecord checks the DELETE path and error wrapping.
func TestDeleteDnsRecord(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != dnsRecordsBase+"/r-1" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		})
		if err := c.DeleteDnsRecord(ctx, "t1", "p1", "vn1", "z1", "r-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if err := c.DeleteDnsRecord(ctx, "t1", "p1", "vn1", "z1", "r-1"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 500")
		}
	})
}
