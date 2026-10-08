package acctest

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"terraform-provider-dcapi/internal/client"
)

// ExistsFunc reports whether the object behind a state ID still exists in DC-API.
// idParts is the state ID split on "/" (e.g. [tenant, project, vnet_uuid]).
// All client Get* functions return (nil, nil) on 404, which is exactly "gone".
type ExistsFunc func(ctx context.Context, c *client.DCAPIClient, idParts []string) (bool, error)

// DeleteFunc deletes the object behind a state ID directly through the client and, for
// async resources, waits until it is really gone.
type DeleteFunc func(ctx context.Context, c *client.DCAPIClient, idParts []string) error

// APICheckFunc inspects the object behind a state ID through the client, independent of
// Terraform state.
type APICheckFunc func(ctx context.Context, c *client.DCAPIClient, idParts []string) error

// CheckDestroy fails if any resource of resType left in the final state still exists in DC-API.
func CheckDestroy(resType string, exists ExistsFunc) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		c, err := mustAPIClient()
		if err != nil {
			return err
		}
		for _, rs := range s.RootModule().Resources {
			if rs.Type != resType {
				continue
			}
			found, err := exists(context.Background(), c, strings.Split(rs.Primary.ID, "/"))
			if err != nil {
				return fmt.Errorf("checking %s %s after destroy: %w", resType, rs.Primary.ID, err)
			}
			if found {
				return fmt.Errorf("%s %s still exists in DC-API after destroy", resType, rs.Primary.ID)
			}
		}
		return nil
	}
}

// Disappears deletes the object behind addr out-of-band. Used with ExpectNonEmptyPlan: true
// to prove Read clears the ID on 404 instead of erroring.
func Disappears(addr string, del DeleteFunc) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, err := primary(s, addr)
		if err != nil {
			return err
		}
		c, err := mustAPIClient()
		if err != nil {
			return err
		}
		if err := del(context.Background(), c, strings.Split(rs.ID, "/")); err != nil {
			return fmt.Errorf("deleting %s out-of-band: %w", addr, err)
		}
		return nil
	}
}

// CheckAPI runs f against the object behind addr, as an independent source of truth next to
// TestCheckResourceAttr (which only reads Terraform state).
func CheckAPI(addr string, f APICheckFunc) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, err := primary(s, addr)
		if err != nil {
			return err
		}
		c, err := mustAPIClient()
		if err != nil {
			return err
		}
		if err := f(context.Background(), c, strings.Split(rs.ID, "/")); err != nil {
			return fmt.Errorf("%s (API check): %w", addr, err)
		}
		return nil
	}
}

// CheckIDPart asserts that attribute attr of addr equals part index of otherAddr's state ID.
// Used where the provider exposes a UUID only inside the ID (G7, key vaults).
func CheckIDPart(addr, attr, otherAddr string, index int) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		other, err := primary(s, otherAddr)
		if err != nil {
			return err
		}
		parts := strings.Split(other.ID, "/")
		if index >= len(parts) {
			return fmt.Errorf("%s ID %q has no part %d", otherAddr, other.ID, index)
		}
		return resource.TestCheckResourceAttr(addr, attr, parts[index])(s)
	}
}

// IDSet records every ID an address has had across steps, so a ForceNew test can prove the
// replaced objects were really deleted, not just the last one.
type IDSet struct {
	mu  sync.Mutex
	ids []string
}

// Record is a check that adds addr's current ID to the set.
func (set *IDSet) Record(addr string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, err := primary(s, addr)
		if err != nil {
			return err
		}
		set.mu.Lock()
		defer set.mu.Unlock()
		set.ids = append(set.ids, rs.ID)
		return nil
	}
}

// CheckAllGone is a CheckDestroy that fails if any recorded ID still exists.
func (set *IDSet) CheckAllGone(exists ExistsFunc) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		c, err := mustAPIClient()
		if err != nil {
			return err
		}
		set.mu.Lock()
		defer set.mu.Unlock()
		for _, id := range set.ids {
			found, err := exists(context.Background(), c, strings.Split(id, "/"))
			if err != nil {
				return fmt.Errorf("checking %s after destroy: %w", id, err)
			}
			if found {
				return fmt.Errorf("%s still exists in DC-API after destroy", id)
			}
		}
		return nil
	}
}

func primary(s *terraform.State, addr string) (*terraform.InstanceState, error) {
	rs, ok := s.RootModule().Resources[addr]
	if !ok {
		return nil, fmt.Errorf("%s not found in state", addr)
	}
	if rs.Primary == nil || rs.Primary.ID == "" {
		return nil, fmt.Errorf("%s has no ID in state", addr)
	}
	return rs.Primary, nil
}

// AttrSnapshot remembers an attribute's value in one step so a later step can prove it didn't
// change. Used for one-time secrets across RefreshState steps, which can't use ConfigStateChecks.
type AttrSnapshot struct {
	mu    sync.Mutex
	value string
	saved bool
}

// Save records addr's attr, which must be non-empty.
func (a *AttrSnapshot) Save(addr, attr string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, err := primary(s, addr)
		if err != nil {
			return err
		}
		v := rs.Attributes[attr]
		if v == "" {
			return fmt.Errorf("%s.%s is empty", addr, attr)
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		a.value, a.saved = v, true
		return nil
	}
}

// Unchanged fails if addr's attr differs from the saved value.
func (a *AttrSnapshot) Unchanged(addr, attr string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, err := primary(s, addr)
		if err != nil {
			return err
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if !a.saved {
			return fmt.Errorf("no saved value for %s.%s", addr, attr)
		}
		if rs.Attributes[attr] != a.value {
			return fmt.Errorf("%s.%s changed (was set, now %q)", addr, attr, redact(rs.Attributes[attr]))
		}
		return nil
	}
}

// redact keeps secrets out of test output.
func redact(v string) string {
	if v == "" {
		return ""
	}
	return "<redacted>"
}
