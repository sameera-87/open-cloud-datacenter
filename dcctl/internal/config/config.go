// Package config manages dcctl's persisted configuration.
//
// Configuration is stored at ~/.dcctl/config.yaml. The file is created
// automatically on first login. Users can override individual settings with
// environment variables (DCCTL_*) or global CLI flags.
//
// Credential tokens (access_token, refresh_token) are stored separately in
// ~/.dcctl/credentials.json to allow the config file to be shared/committed
// without leaking tokens.
//
// The active tenant selection is stored in ~/.dcctl/context.yaml and can be
// set with 'dcctl tenant set <id>'. Per-command --tenant flags and the
// DCCTL_TENANT env var override context.yaml.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	configDir   = ".dcctl"
	configFile  = "config.yaml"
	credsFile   = "credentials.json"
	contextFile = "context.yaml"
)

// Config holds dcctl's non-secret settings.
type Config struct {
	// DCAPI is the base URL of the DC-API server.
	DCAPI string `mapstructure:"dcapi_url" yaml:"dcapi_url"`
	// OIDCIssuer is the Asgardeo issuer URL (shared with DC-API config).
	OIDCIssuer string `mapstructure:"oidc_issuer" yaml:"oidc_issuer"`
	// ClientID is the Asgardeo public client ID for dcctl (no client secret — PKCE only).
	ClientID string `mapstructure:"client_id" yaml:"client_id"`
	// CallbackPort is the local port used for the OIDC redirect_uri.
	CallbackPort int `mapstructure:"callback_port" yaml:"callback_port"`
}

// Credentials holds the OAuth2 tokens. Stored separately from Config.
type Credentials struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	Expiry       time.Time `json:"expiry"`
	TenantID     string    `json:"tenant_id"` // extracted from the JWT on login
	Sub          string    `json:"sub"`        // user ID from the JWT
}

// Context holds the user's current active selections (tenant, project).
// Stored at ~/.dcctl/context.yaml.
//
// This mirrors the shape of `az account` / `kubectl` current-context — a small
// file that remembers "where am I working right now" so the user doesn't have
// to pass --tenant or --project on every command.
type Context struct {
	// ActiveTenant is the slug of the tenant that CLI commands run against
	// when no --tenant flag or DCCTL_TENANT env var is provided.
	ActiveTenant string `yaml:"active_tenant,omitempty"`

	// ActiveProjects maps tenant slug → active project slug for that tenant.
	// Keyed by tenant so switching tenants automatically uses that tenant's
	// last-set project. Set with 'dcctl project set <id>'.
	ActiveProjects map[string]string `yaml:"active_projects,omitempty"`
}

// LoadContext reads ~/.dcctl/context.yaml. If the file does not exist yet
// (e.g., before the first 'dcctl tenant set'), an empty Context is returned
// with no error — callers should treat an empty ActiveTenant as "not set".
func LoadContext() (*Context, error) {
	d, err := dir()
	if err != nil {
		return &Context{}, nil
	}
	data, err := os.ReadFile(filepath.Join(d, contextFile))
	if os.IsNotExist(err) {
		return &Context{}, nil
	}
	if err != nil {
		return &Context{}, fmt.Errorf("read context file: %w", err)
	}
	c, err := unmarshalContextYAML(data)
	if err != nil {
		return &Context{}, fmt.Errorf("parse context file: %w", err)
	}
	return c, nil
}

// SaveContext writes ctx to ~/.dcctl/context.yaml atomically (write to a
// tempfile in the same directory, then rename). The file is created with 0600
// permissions (owner-read only).
func SaveContext(c *Context) error {
	d, err := dir()
	if err != nil {
		return err
	}
	data, err := marshalContextYAML(c)
	if err != nil {
		return fmt.Errorf("marshal context: %w", err)
	}
	dest := filepath.Join(d, contextFile)
	// Write to a tempfile in the same directory so the rename is atomic on
	// all POSIX filesystems (both src and dst are on the same mount).
	tmp, err := os.CreateTemp(d, ".context-*.yaml.tmp")
	if err != nil {
		return fmt.Errorf("create temp context file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		// Clean up the tempfile if we never renamed it (e.g., write error).
		os.Remove(tmpName) //nolint:errcheck
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write context file: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod context file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp context file: %w", err)
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return fmt.Errorf("rename context file: %w", err)
	}
	return nil
}

// marshalContextYAML serialises Context to YAML without importing a library.
// Produces a simple YAML file with active_tenant and an optional
// active_projects block.
func marshalContextYAML(c *Context) ([]byte, error) {
	var s string
	if c.ActiveTenant != "" {
		s += fmt.Sprintf("active_tenant: %q\n", c.ActiveTenant)
	}
	if len(c.ActiveProjects) > 0 {
		s += "active_projects:\n"
		// Emit in a deterministic order so the file diffs cleanly.
		keys := sortedStringKeys(c.ActiveProjects)
		for _, k := range keys {
			s += fmt.Sprintf("  %s: %q\n", k, c.ActiveProjects[k])
		}
	}
	if s == "" {
		return []byte("{}\n"), nil
	}
	return []byte(s), nil
}

// unmarshalContextYAML deserialises the minimal context.yaml format we write.
// We parse it manually to avoid adding a yaml import to the config package.
// Supports:
//
//	active_tenant: "foo"
//	active_projects:
//	  foo: "bar"
//	  baz: "qux"
func unmarshalContextYAML(data []byte) (*Context, error) {
	c := &Context{}
	inActiveProjects := false

	for _, line := range splitLines(string(data)) {
		raw := line
		line = trimSpace(line)
		if line == "" || line[0] == '#' {
			inActiveProjects = false
			continue
		}

		// Detect if we're inside the active_projects map block.
		// Lines inside start with 2+ spaces (indented).
		if inActiveProjects {
			// A non-indented line ends the block.
			if len(raw) > 0 && (raw[0] != ' ' && raw[0] != '\t') {
				inActiveProjects = false
				// Fall through to re-parse as a top-level key.
			} else {
				// Parse "  tenant: "project"" indented entries.
				key, val, ok := cutColon(line)
				if ok {
					key = trimSpace(key)
					val = trimSpace(val)
					if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
						val = val[1 : len(val)-1]
					}
					if c.ActiveProjects == nil {
						c.ActiveProjects = make(map[string]string)
					}
					c.ActiveProjects[key] = val
				}
				continue
			}
		}

		key, val, ok := cutColon(line)
		if !ok {
			continue
		}
		key = trimSpace(key)
		val = trimSpace(val)
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		switch key {
		case "active_tenant":
			c.ActiveTenant = val
		case "active_projects":
			inActiveProjects = true
		}
	}
	return c, nil
}

// sortedStringKeys returns the keys of m in lexicographic order.
func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort — the map is tiny (one entry per tenant).
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// splitLines splits a string on newlines, handling both \n and \r\n.
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			end := i
			if end > start && s[end-1] == '\r' {
				end--
			}
			lines = append(lines, s[start:end])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// trimSpace trims leading and trailing ASCII whitespace.
func trimSpace(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// cutColon splits a string at the first ':' character.
func cutColon(s string) (key, val string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return s[:i], s[i+1:], true
		}
	}
	return "", "", false
}

// homeDir returns the user's home directory.
func homeDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return home, nil
}

// dir returns the path to ~/.dcctl, creating it if needed.
func dir() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(home, configDir)
	if err := os.MkdirAll(d, 0700); err != nil {
		return "", fmt.Errorf("create config dir %s: %w", d, err)
	}
	return d, nil
}

// Default returns a Config with sensible defaults.
// Users should call Save() after adjusting fields.
func Default() *Config {
	return &Config{
		DCAPI:        "https://dc-api.internal.wso2.com",
		OIDCIssuer:   "https://api.asgardeo.io/t/wso2",
		ClientID:     "dcctl-public-client",
		CallbackPort: 8085,
	}
}

// Save writes cfg to ~/.dcctl/config.yaml.
// The file is written with 0600 permissions (owner-read only).
func (cfg *Config) Save() error {
	d, err := dir()
	if err != nil {
		return err
	}
	data, err := marshalYAML(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(filepath.Join(d, configFile), data, 0600)
}

// SaveCredentials persists tokens to ~/.dcctl/credentials.json.
func SaveCredentials(creds *Credentials) error {
	d, err := dir()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	return os.WriteFile(filepath.Join(d, credsFile), data, 0600)
}

// GetTenantID resolves the tenant ID for a CLI command.
//
// Resolution order (first non-empty value wins):
//  1. explicit — value from the --tenant flag passed by the caller.
//  2. DCCTL_TENANT environment variable.
//  3. active_tenant in ~/.dcctl/context.yaml (set with 'dcctl tenant set').
//  4. tenant_id in ~/.dcctl/credentials.json (legacy: set by dcctl login).
//  5. Error with a hint to run 'dcctl tenant set' or 'dcctl tenant list'.
//
// Callers pass the --tenant flag value as explicit; pass "" when the flag was
// not provided and the caller wants the automatic resolution to take over.
func GetTenantID(explicit string) (string, error) {
	// 1. Explicit --tenant flag.
	if explicit != "" {
		return explicit, nil
	}

	// 2. DCCTL_TENANT environment variable.
	if envTenant := os.Getenv("DCCTL_TENANT"); envTenant != "" {
		return envTenant, nil
	}

	// 3. Active tenant from context.yaml.
	ctx, _ := LoadContext() // ignore error — empty Context is fine
	if ctx != nil && ctx.ActiveTenant != "" {
		return ctx.ActiveTenant, nil
	}

	// 4. Legacy fallback: tenant_id embedded in credentials.json.
	creds, err := LoadCredentials()
	if err == nil && creds.TenantID != "" {
		return creds.TenantID, nil
	}

	// 5. Nothing found — give an actionable error.
	return "", fmt.Errorf(
		"no tenant — run 'dcctl tenant set <id>' to choose one, " +
			"pass --tenant <id>, or set DCCTL_TENANT env var. " +
			"See 'dcctl tenant list' for available tenants.",
	)
}

// GetProjectID resolves the project ID for a CLI command.
//
// Resolution order (first non-empty value wins):
//  1. explicit — value from the --project flag passed by the caller.
//  2. DCCTL_PROJECT environment variable.
//  3. ActiveProjects[tenantID] in ~/.dcctl/context.yaml (set with 'dcctl project set').
//  4. Error with a hint to run 'dcctl project set' or pass --project.
//
// tenantID must already be resolved (call GetTenantID first). The per-tenant
// lookup means switching tenants with --tenant automatically uses that
// tenant's previously pinned project.
func GetProjectID(explicit, tenantID string) (string, error) {
	// 1. Explicit --project flag.
	if explicit != "" {
		return explicit, nil
	}

	// 2. DCCTL_PROJECT environment variable.
	if envProject := os.Getenv("DCCTL_PROJECT"); envProject != "" {
		return envProject, nil
	}

	// 3. Per-tenant active project from context.yaml.
	if tenantID != "" {
		ctx, _ := LoadContext() // ignore error — empty Context is fine
		if ctx != nil && ctx.ActiveProjects != nil {
			if pid := ctx.ActiveProjects[tenantID]; pid != "" {
				return pid, nil
			}
		}
	}

	// 4. Nothing found — give an actionable error.
	return "", fmt.Errorf(
		"no active project — run 'dcctl project set <id>' to choose one, " +
			"or pass --project <id>. See 'dcctl list projects' for available projects.",
	)
}

// SetActiveProject pins projectID as the active project for tenantID in
// ~/.dcctl/context.yaml. Existing entries for other tenants are preserved.
func SetActiveProject(tenantID, projectID string) error {
	ctx, err := LoadContext()
	if err != nil {
		return err
	}
	if ctx.ActiveProjects == nil {
		ctx.ActiveProjects = make(map[string]string)
	}
	ctx.ActiveProjects[tenantID] = projectID
	return SaveContext(ctx)
}

// LoadCredentials reads tokens from ~/.dcctl/credentials.json.
// Returns an error if the file does not exist (user not logged in).
func LoadCredentials() (*Credentials, error) {
	d, err := dir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(d, credsFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("not logged in — run `dcctl login` first")
	}
	if err != nil {
		return nil, fmt.Errorf("read credentials: %w", err)
	}
	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials: %w", err)
	}
	return &creds, nil
}

// marshalYAML is a minimal YAML marshaller for Config.
// We avoid importing a YAML library just for this — the config is simple enough.
func marshalYAML(cfg *Config) ([]byte, error) {
	s := fmt.Sprintf(
		"dcapi_url: %q\noidc_issuer: %q\nclient_id: %q\ncallback_port: %d\n",
		cfg.DCAPI, cfg.OIDCIssuer, cfg.ClientID, cfg.CallbackPort,
	)
	return []byte(s), nil
}
