package client

import (
	"encoding/json"
	"fmt"
)

// decodeList parses a DC-API list response into out (a pointer to a slice).
//
// The API reference documents two list shapes: most collections return a bare JSON
// array (VNets, NSGs, subnets), while a few wrap it as {"items": [...]} (images,
// key vault secrets). The collections without a documented shape — VMs, bastions,
// clusters, node pools, service accounts, private endpoints — are decoded with this
// helper so either shape works.
func decodeList(respBytes []byte, out interface{}) error {
	if err := json.Unmarshal(respBytes, out); err == nil {
		return nil
	}

	var wrapper struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(respBytes, &wrapper); err != nil || wrapper.Items == nil {
		return fmt.Errorf("expected a JSON array or an object with \"items\": %s", string(respBytes))
	}
	return json.Unmarshal(wrapper.Items, out)
}
