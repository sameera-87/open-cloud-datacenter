// Package acctest holds the acceptance tests for the dcapi provider and the helpers they share.
//
// Acceptance tests run the real Terraform CLI against a real DC-API. They only run when
// TF_ACC=1 is set, so `go test ./...` stays offline and fast. See docs/testsuite/ for the plan:
// 03-acceptance-test-framework.md explains the helpers in this package, 04-test-isolation.md
// the naming and CIDR rules, and resources/*.md what each resource's tests cover.
package acctest

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov5"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"

	"terraform-provider-dcapi/internal/provider"
)

// ProviderFactories serves the provider in-process: the test binary starts it as a gRPC
// server and Terraform attaches to it, so nothing needs to be built or installed.
var ProviderFactories = map[string]func() (tfprotov5.ProviderServer, error){
	"dcapi": func() (tfprotov5.ProviderServer, error) {
		return schema.NewGRPCProviderServer(provider.New()), nil
	},
}

// requiredEnv is the env-var contract every acceptance test depends on (docs/testsuite/02 §4).
var requiredEnv = []string{
	"DCAPI_ENDPOINT", "DCAPI_TOKEN", "DCAPI_ACC_TENANT_ID", "DCAPI_ACC_PROJECT_ID",
	"DCAPI_ACC_REGION", "DCAPI_ACC_RUN_ID",
}

// runIDPattern keeps generated names valid for the strictest DC-API naming rules
// (lowercase, DNS-label safe, short enough for 32-character cluster names).
var runIDPattern = regexp.MustCompile(`^[a-z0-9]{1,8}$`)

// PreCheck fails fast with an actionable message instead of a cryptic 401 mid-apply.
func PreCheck(t *testing.T) {
	t.Helper()
	for _, k := range requiredEnv {
		if os.Getenv(k) == "" {
			t.Fatalf("%s must be set for acceptance tests ", k)
		}
	}
	if !runIDPattern.MatchString(RunID()) {
		t.Fatalf("DCAPI_ACC_RUN_ID %q must be 1-8 lowercase letters or digits", RunID())
	}
}

// RequireEnv returns the value of key, or fails the test if it isn't set. Used for the
// inputs only some tests need, such as images and the Kubernetes version. It runs before
// resource.Test, so it skips like the framework does when TF_ACC isn't set.
func RequireEnv(t *testing.T, key string) string {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("Acceptance tests skipped unless env 'TF_ACC' set")
	}
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("%s must be set for this test (see docs/testsuite/02 §4)", key)
	}
	return v
}

func TenantID() string  { return os.Getenv("DCAPI_ACC_TENANT_ID") }
func ProjectID() string { return os.Getenv("DCAPI_ACC_PROJECT_ID") }
func Region() string    { return os.Getenv("DCAPI_ACC_REGION") }
func RunID() string     { return os.Getenv("DCAPI_ACC_RUN_ID") }

// ExpectErr builds a regexp for ExpectError that matches words even when Terraform wraps
// the error message across lines: the words are quoted and joined with `\s+`.
func ExpectErr(phrase string) *regexp.Regexp {
	words := strings.Fields(phrase)
	for i, w := range words {
		words[i] = regexp.QuoteMeta(w)
	}
	return regexp.MustCompile(strings.Join(words, `\s+`))
}
