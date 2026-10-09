// Unit tests for nsg.go.
package client

import (
	"context"
	"net/http"
	"testing"
)

const nsgBase = "/v1/tenants/t1/projects/p1/security-groups"

// TestCreateNSG guards the POST path, rule forwarding, and parse of the response.
func TestCreateNSG(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != nsgBase {
			t.Errorf("got %s %s, want POST %s", r.Method, r.URL.Path, nsgBase)
		}
		var got NSGCreateRequest
		decodeBody(t, r, &got)
		if got.Name != "web" || len(got.Rules) != 1 || got.Rules[0].Priority != 100 {
			t.Errorf("body = %+v, want name=web with one rule at priority 100", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"sg-1","name":"web","rules":[{"name":"https","priority":100}],"status":"ACTIVE"}`))
	})
	nsg, err := c.CreateNSG(context.Background(), "t1", "p1", NSGCreateRequest{
		Name:  "web",
		Rules: []NSGRule{{Name: "https", Priority: 100, Direction: "inbound"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nsg.ID != "sg-1" || len(nsg.Rules) != 1 {
		t.Fatalf("nsg = %+v, want id sg-1 with one rule", nsg)
	}
}

// TestGetNSG covers 200 and the 404 sentinel.
func TestGetNSG(t *testing.T) {
	ctx := context.Background()

	t.Run("found", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != nsgBase+"/sg-1" {
				t.Errorf("path = %s, unexpected", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"id":"sg-1","name":"web","attachments":[{"id":"at-1","target_id":"vm-1"}]}`))
		})
		got, err := c.GetNSG(ctx, "t1", "p1", "sg-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got == nil || len(got.Attachments) != 1 || got.Attachments[0].ID != "at-1" {
			t.Fatalf("got = %+v, want one attachment at-1", got)
		}
	})

	t.Run("404 returns nil,nil", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		})
		got, err := c.GetNSG(ctx, "t1", "p1", "missing")
		if err != nil || got != nil {
			t.Fatalf("got=%v err=%v, want nil,nil on 404", got, err)
		}
	})
}

// TestListNSGs checks the bare-array parse.
func TestListNSGs(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != nsgBase {
			t.Errorf("path = %s, unexpected", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[{"id":"sg-1","name":"a"},{"id":"sg-2","name":"b"}]`))
	})
	nsgs, err := c.ListNSGs(context.Background(), "t1", "p1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nsgs) != 2 {
		t.Fatalf("nsgs = %+v, want two", nsgs)
	}
}

// TestUpdateNSGRules guards the PUT .../rules full-replace path.
func TestUpdateNSGRules(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != nsgBase+"/sg-1/rules" {
			t.Errorf("got %s %s, want PUT .../sg-1/rules", r.Method, r.URL.Path)
		}
		var got NSGUpdateRulesRequest
		decodeBody(t, r, &got)
		if len(got.Rules) != 2 {
			t.Errorf("rules = %v, want two", got.Rules)
		}
		_, _ = w.Write([]byte(`{"id":"sg-1","name":"web","rules":[{"name":"a"},{"name":"b"}]}`))
	})
	nsg, err := c.UpdateNSGRules(context.Background(), "t1", "p1", "sg-1",
		NSGUpdateRulesRequest{Rules: []NSGRule{{Name: "a"}, {Name: "b"}}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nsg.Rules) != 2 {
		t.Fatalf("rules = %v, want two", nsg.Rules)
	}
}

// TestDeleteNSG checks the DELETE path and that a 409 (active attachments) errors.
func TestDeleteNSG(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != nsgBase+"/sg-1" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		})
		if err := c.DeleteNSG(ctx, "t1", "p1", "sg-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("409 errors", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
		})
		if err := c.DeleteNSG(ctx, "t1", "p1", "sg-1"); err == nil {
			t.Fatal("err = nil, want an error when the NSG still has attachments (409)")
		}
	})
}

// TestCreateNSGAttachment guards the POST .../attachments path, body, and parse.
func TestCreateNSGAttachment(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != nsgBase+"/sg-1/attachments" {
			t.Errorf("got %s %s, want POST .../attachments", r.Method, r.URL.Path)
		}
		var got NSGAttachmentCreateRequest
		decodeBody(t, r, &got)
		if got.TargetType != "vm" || got.TargetID != "vm-1" {
			t.Errorf("body = %+v, want target vm/vm-1", got)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"at-1","sg_id":"sg-1","target_type":"vm","target_id":"vm-1"}`))
	})
	at, err := c.CreateNSGAttachment(context.Background(), "t1", "p1", "sg-1",
		NSGAttachmentCreateRequest{TargetType: "vm", TargetID: "vm-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if at.ID != "at-1" {
		t.Fatalf("attachment = %+v, want id at-1", at)
	}
}

// TestDeleteNSGAttachment checks the DELETE .../attachments/{id} path.
func TestDeleteNSGAttachment(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != nsgBase+"/sg-1/attachments/at-1" {
				t.Errorf("got %s %s, unexpected", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		})
		if err := c.DeleteNSGAttachment(ctx, "t1", "p1", "sg-1", "at-1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("error", func(t *testing.T) {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		if err := c.DeleteNSGAttachment(ctx, "t1", "p1", "sg-1", "at-1"); err == nil {
			t.Fatal("err = nil, want an error for HTTP 500")
		}
	})
}
