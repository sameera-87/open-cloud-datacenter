package cluster

// pool_input.go — shared parsing for inline worker-pool specs.
//
// The --worker-pool flag on `dcctl cluster create` accepts a compact
// key=value string.  This file contains the parser so that both
// create.go and cluster_test.go can reference it without duplicating
// the logic that lives in node_pool.go.
//
// Format (Format A — multi-key string):
//
//	"name=<name>,size=<size>,count=<n>"
//	"name=<name>,size=<size>,count=<n>,taint=<key=value:effect>,label=<k=v>"
//
// Multiple taints and labels are expressed by repeating the sub-key:
//
//	"name=gpu,size=xlarge,count=2,taint=nvidia.com/gpu=:NoSchedule,taint=spot=true:NoExecute,label=accel=a100"
//
// Separators: commas between all key=value pairs.  Because taint and label
// values already contain their own '=' signs and taints contain ':', the
// parser identifies each sub-key by the prefix up to the first '=' and
// groups consecutive taint/label tokens with the same sub-key type.
// Note that the taint value itself (e.g. "nvidia.com/gpu=:NoSchedule") must
// not contain a literal comma — that is the field separator.
//
// Validation mirrors runNodePoolAdd in node_pool.go:
//   - name: matches poolNameRE, not "system", required
//   - size: one of validSizes, required
//   - count: 1-50, required
//   - disk-gb: optional, > 0
//   - image: optional, passed through verbatim
//   - taint: parsed via parseSingleTaint (same function as node-pool add)
//   - label: parsed via parseLabels     (same function as node-pool add)

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/wso2/dcctl/internal/client"
)

const maxWorkerPools = 10

// parseWorkerPoolString parses a single --worker-pool flag value into an
// AddNodePoolRequest.  The raw string format is described in the file header.
func parseWorkerPoolString(raw string) (client.AddNodePoolRequest, error) {
	// Split on commas to get individual sub-key=value tokens.
	// We then iterate and aggregate taint/label tokens.
	tokens := splitPoolTokens(raw)

	var (
		name      string
		size      string
		count     int
		diskGB    int
		imageName string
		rawTaints []string
		rawLabels []string
		countSet  bool
	)

	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		// Each token is  subkey=remainder  where remainder may itself contain '='.
		eqIdx := strings.Index(tok, "=")
		if eqIdx < 0 {
			return client.AddNodePoolRequest{}, fmt.Errorf(
				"invalid --worker-pool token %q: expected key=value format", tok)
		}
		subKey := tok[:eqIdx]
		value := tok[eqIdx+1:]

		switch subKey {
		case "name":
			name = value
		case "size":
			size = value
		case "count":
			n, err := strconv.Atoi(value)
			if err != nil {
				return client.AddNodePoolRequest{}, fmt.Errorf(
					"invalid --worker-pool count %q: must be an integer", value)
			}
			count = n
			countSet = true
		case "disk-gb", "disk_gb", "diskgb":
			n, err := strconv.Atoi(value)
			if err != nil {
				return client.AddNodePoolRequest{}, fmt.Errorf(
					"invalid --worker-pool disk-gb %q: must be an integer", value)
			}
			diskGB = n
		case "image":
			imageName = value
		case "taint":
			rawTaints = append(rawTaints, value)
		case "label":
			rawLabels = append(rawLabels, value)
		default:
			return client.AddNodePoolRequest{}, fmt.Errorf(
				"unknown --worker-pool sub-key %q; valid keys: name, size, count, disk-gb, image, taint, label",
				subKey)
		}
	}

	// ── Required-field checks ────────────────────────────────────────────────────

	if name == "" {
		return client.AddNodePoolRequest{}, fmt.Errorf(
			"--worker-pool is missing required sub-key 'name'")
	}
	if name == "system" {
		return client.AddNodePoolRequest{}, fmt.Errorf(
			"pool name 'system' is reserved; choose a different name")
	}
	if !poolNameRE.MatchString(name) {
		return client.AddNodePoolRequest{}, fmt.Errorf(
			"--worker-pool name %q is invalid: must be 1-40 lowercase alphanumeric characters or hyphens, starting with a letter",
			name)
	}
	if size == "" {
		return client.AddNodePoolRequest{}, fmt.Errorf(
			"--worker-pool is missing required sub-key 'size'")
	}
	if !isValidSize(size) {
		return client.AddNodePoolRequest{}, fmt.Errorf(
			"--worker-pool size %q is not valid; must be one of: %s",
			size, strings.Join(validSizes, ", "))
	}
	if !countSet {
		return client.AddNodePoolRequest{}, fmt.Errorf(
			"--worker-pool is missing required sub-key 'count'")
	}
	if count < 1 || count > 50 {
		return client.AddNodePoolRequest{}, fmt.Errorf(
			"--worker-pool count %d is out of range; must be 1-50", count)
	}

	// ── Optional-field parsing ───────────────────────────────────────────────────

	taints, err := parseTaints(rawTaints)
	if err != nil {
		return client.AddNodePoolRequest{}, fmt.Errorf("--worker-pool taint: %w", err)
	}
	labels, err := parseLabels(rawLabels)
	if err != nil {
		return client.AddNodePoolRequest{}, fmt.Errorf("--worker-pool label: %w", err)
	}

	req := client.AddNodePoolRequest{
		Name:      name,
		Size:      size,
		Count:     count,
		ImageName: imageName,
		Taints:    taints,
		Labels:    labels,
	}
	if diskGB > 0 {
		req.DiskGB = diskGB
	}
	return req, nil
}

// splitPoolTokens splits the raw --worker-pool value on commas but preserves
// commas that appear inside taint values.  Taints follow the form
// "taint=<key=value:effect>"; since the taint sub-value never contains a
// bare comma (commas are not valid in Kubernetes taint keys or effects), a
// simple strings.Split suffices.  If a future format needs quoted commas,
// this is the only function that needs to change.
func splitPoolTokens(raw string) []string {
	return strings.Split(raw, ",")
}

// parseWorkerPools parses and validates all --worker-pool flag values.
// It enforces the maxWorkerPools cap and rejects duplicate pool names.
func parseWorkerPools(rawFlags []string) ([]client.AddNodePoolRequest, error) {
	if len(rawFlags) == 0 {
		return nil, nil
	}
	if len(rawFlags) > maxWorkerPools {
		return nil, fmt.Errorf(
			"too many --worker-pool flags: got %d, maximum is %d",
			len(rawFlags), maxWorkerPools)
	}

	pools := make([]client.AddNodePoolRequest, 0, len(rawFlags))
	seen := make(map[string]struct{}, len(rawFlags))

	for _, raw := range rawFlags {
		pool, err := parseWorkerPoolString(raw)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[pool.Name]; dup {
			return nil, fmt.Errorf("duplicate pool name %q in --worker-pool flags", pool.Name)
		}
		seen[pool.Name] = struct{}{}
		pools = append(pools, pool)
	}
	return pools, nil
}
