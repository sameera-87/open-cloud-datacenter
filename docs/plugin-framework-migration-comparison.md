# Migrating `terraform-provider-dcapi`: SDKv2 → terraform-plugin-framework

This document compares the current implementation of this provider (built on
`terraform-plugin-sdk/v2`) against what it would look like after migrating to
`terraform-plugin-framework`, HashiCorp's current recommended SDK. It covers
the architectural differences, shows concrete before/after code from this
repo, and explains the performance and code-quality advantages of migrating.

## Current state of this repo

- Module: `terraform-provider-dcapi`, Go 1.25
- SDK: `github.com/hashicorp/terraform-plugin-sdk/v2 v2.34.0` only — no
  `terraform-plugin-framework` dependency exists yet.
- 19 resources (`internal/resources/`), 12 data sources
  (`internal/datasources/`), 1 provider (`internal/provider/provider.go`).
- ~6,900 lines of resource/data-source code, all SDKv2-idiomatic
  (`schema.Resource`, `*schema.ResourceData`, `diag.Diagnostics`,
  `d.Get`/`d.Set`/`d.SetId`).
- Hand-written HTTP client (`internal/client/`, 18 files) — this layer is
  **SDK-agnostic** and does not need to change at all during migration.
- No automated tests exist today (no `_test.go` files); only manual example
  `.tf` configs under `examples/` and `test/`.
- 9 resources do async polling via `retry.StateChangeConf` (VM, cluster,
  subnet, VNet, bastion, node pool, key vault, private DNS zone, VNet
  peering). 1 resource (`route_table.go`) uses `CustomizeDiff` for
  cross-field validation. 6 resources support `ImportStatePassthroughContext`.

## 1. Architecture: what actually changes

| Layer | SDKv2 (today) | plugin-framework (after migration) |
|---|---|---|
| Provider entry point | `plugin.Serve(&plugin.ServeOpts{ProviderFunc: provider.New})` | `providerserver.Serve(ctx, provider.New, providerserver.ServeOpts{Address: ...})` |
| Provider type | `func New() *schema.Provider` | `type dcapiProvider struct{...}` implementing `provider.Provider` (`Metadata`, `Schema`, `Configure`, `Resources`, `DataSources`) |
| Resource schema | `map[string]*schema.Schema` (untyped `Type: schema.TypeString`) | `schema.Schema{ Attributes: map[string]schema.Attribute{...} }` with typed attributes (`schema.StringAttribute`, `schema.Int64Attribute`, ...) |
| State/plan access | `*schema.ResourceData` + `d.Get("x").(string)` (runtime type assertion, `interface{}` everywhere) | Typed Go structs with `tfsdk` tags, populated via `req.Plan.Get(ctx, &plan)` / `resp.State.Set(ctx, &state)` — compile-time checked |
| CRUD signatures | `func resourceXCreate(ctx, d *schema.ResourceData, meta interface{}) diag.Diagnostics` | `func (r *xResource) Create(ctx, req resource.CreateRequest, resp *resource.CreateResponse)` |
| Error/diagnostics | `diag.FromErr(err)`, `diag.Diagnostic{Severity: ..., Summary: ..., Detail: ...}` built by hand | `resp.Diagnostics.AddError(summary, detail)` / `AddWarning(...)` — same concept, less manual struct plumbing |
| Provider-level data ("meta") | `interface{}` type-asserted in every single CRUD function: `c := meta.(*client.DCAPIClient)` | Typed once via `resp.ResourceData = client` in `Configure`, retrieved with a compile-time-checked assertion in each resource's `Configure(ctx, req, resp)` — still one assertion per resource type, but centralized and consistent |
| Cross-field validation | `CustomizeDiff` (untyped `*schema.ResourceDiff`) | `ValidateConfig(ctx, req, resp)` on the resource, operating on the typed model |
| Import | `Importer: &schema.ResourceImporter{StateContext: schema.ImportStatePassthroughContext}` | Resource implements `resource.ResourceWithImportState`; `ImportState` calls `resource.ImportStatePassthroughID(...)` |
| Protocol | Both ultimately speak Terraform's plugin protocol (protocol v5/v6) over gRPC — SDKv2 wraps it in `terraform-plugin-go`; the framework is a thinner, purpose-built layer over the same `terraform-plugin-go` primitives | Same wire protocol; the framework can also target protocol v6 in the same server startup call without extra shims (SDKv2 requires `tf5to6server` muxing to get v6 features) |
| Wire type distinctions | No `types.String`/etc. — SDKv2 flattens config `null` vs. `""`/`0`, so unset-vs-empty is often ambiguous (`d.GetOk` is the workaround) | `types.String` distinguishes `Null`, `Unknown` (not yet computed at plan time), and a concrete value — closes a whole class of "plan not empty after apply" bugs |
| Tests | None today | `terraform-plugin-testing` (`resource.Test`/`resource.UnitTest`) — the modern successor to SDKv2's `helper/resource`, works with framework or SDKv2 providers alike |

## 2. Code-level comparison, using this repo's own resources

### 2.1 Provider definition

**Before — `internal/provider/provider.go` (current):**

```go
func New() *schema.Provider {
	p := &schema.Provider{
		Schema: map[string]*schema.Schema{
			"endpoint": {
				Type:        schema.TypeString,
				Optional:    true,
				DefaultFunc: schema.EnvDefaultFunc("DCAPI_ENDPOINT", nil),
			},
			"token": {
				Type:        schema.TypeString,
				Optional:    true,
				Sensitive:   true,
				DefaultFunc: schema.EnvDefaultFunc("DCAPI_TOKEN", nil),
			},
		},
		ResourcesMap: map[string]*schema.Resource{
			"dcapi_virtual_machine": resources.ResourceVirtualMachine(),
			// ...18 more
		},
		DataSourcesMap: map[string]*schema.Resource{ /* ... */ },
		ConfigureContextFunc: configureProvider,
	}
	return p
}

func configureProvider(ctx context.Context, d *schema.ResourceData) (interface{}, diag.Diagnostics) {
	var diags diag.Diagnostics
	endpoint, _ := d.Get("endpoint").(string)
	token, _ := d.Get("token").(string)
	if endpoint == "" {
		diags = append(diags, diag.Diagnostic{Severity: diag.Error, Summary: "Missing endpoint"})
	}
	// ...
	c, err := client.NewClient(endpoint, token)
	return c, diags
}
```

**After — framework equivalent:**

```go
type dcapiProvider struct{ version string }

type dcapiProviderModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	Token    types.String `tfsdk:"token"`
}

func (p *dcapiProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "dcapi"
}

func (p *dcapiProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{Optional: true},
			"token":    schema.StringAttribute{Optional: true, Sensitive: true},
		},
	}
}

func (p *dcapiProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg dcapiProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := cfg.Endpoint.ValueString()
	if endpoint == "" {
		endpoint = os.Getenv("DCAPI_ENDPOINT")
	}
	token := cfg.Token.ValueString()
	if token == "" {
		token = os.Getenv("DCAPI_TOKEN")
	}
	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Missing endpoint", "Set 'endpoint' or export DCAPI_ENDPOINT.")
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Missing token", "Set 'token' or export DCAPI_TOKEN.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	c, err := client.NewClient(endpoint, token)
	if err != nil {
		resp.Diagnostics.AddError("Failed to initialise DC-API client", err.Error())
		return
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *dcapiProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewVirtualMachineResource,
		// ...18 more
	}
}

func (p *dcapiProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{ /* ... */ }
}
```

Note the framework does **not** eliminate the env-var fallback boilerplate or
the endpoint/token validation logic — that stays essentially the same. What
it removes is `interface{}` returns and manual `diag.Diagnostic{}` struct
literals; `AddAttributeError` also attaches the error to the specific
`endpoint`/`token` attribute in Terraform's plan output instead of a generic
top-level error.

### 2.2 A resource: `dcapi_virtual_machine`

This is the best example in the repo because it combines typed
required/optional fields, computed fields, sensitive "shown-once" secrets,
manual cross-field validation, a composite string ID, and async polling —
every pattern that changes shape in a migration.

**Before — schema (`internal/resources/vm.go`, current):**

```go
func ResourceVirtualMachine() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceVMCreate,
		ReadContext:   resourceVMRead,
		DeleteContext: resourceVMDelete,
		Timeouts: &schema.ResourceTimeout{
			Create: schema.DefaultTimeout(15 * time.Minute),
			Delete: schema.DefaultTimeout(10 * time.Minute),
		},
		Schema: map[string]*schema.Schema{
			"name": {Type: schema.TypeString, Required: true, ForceNew: true},
			"size": {
				Type: schema.TypeString, Required: true, ForceNew: true,
				ValidateFunc: validation.StringInSlice([]string{"small", "medium", "large", "xlarge"}, false),
			},
			"vnet_id":    {Type: schema.TypeString, Optional: true, ForceNew: true},
			"subnet_id":  {Type: schema.TypeString, Optional: true, ForceNew: true},
			"status":     {Type: schema.TypeString, Computed: true},
			"private_key": {Type: schema.TypeString, Computed: true, Sensitive: true},
			// ... 10 more fields
		},
	}
}
```

Every field is a bag of untyped flags on `schema.Schema`. Nothing here is
checked by the Go compiler — a typo in a `ValidateFunc` slice, or reading the
wrong field name in `d.Get(...)`, is only caught at `terraform plan` time (or
not at all).

**After — schema + typed model:**

```go
type vmResourceModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Size            types.String `tfsdk:"size"`
	DiskGB          types.Int64  `tfsdk:"disk_gb"`
	ImageName       types.String `tfsdk:"image_name"`
	NetworkName     types.String `tfsdk:"network_name"`
	VNetID          types.String `tfsdk:"vnet_id"`
	SubnetID        types.String `tfsdk:"subnet_id"`
	TenantID        types.String `tfsdk:"tenant_id"`
	ProjectID       types.String `tfsdk:"project_id"`
	Status          types.String `tfsdk:"status"`
	ProviderType    types.String `tfsdk:"provider_type"`
	IPAddress       types.String `tfsdk:"ip_address"`
	Message         types.String `tfsdk:"message"`
	CreatedAt       types.String `tfsdk:"created_at"`
	PrivateKey      types.String `tfsdk:"private_key"`
	ConsolePassword types.String `tfsdk:"console_password"`
}

func (r *vmResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"size": schema.StringAttribute{
				Required: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.OneOf("small", "medium", "large", "xlarge"),
				},
			},
			"vnet_id": schema.StringAttribute{
				Optional: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"status": schema.StringAttribute{Computed: true},
			"private_key": schema.StringAttribute{
				Computed: true, Sensitive: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			// ... rest of the fields
		},
	}
}
```

Two things worth calling out precisely because they map 1:1 onto patterns
already in this file:

- `ForceNew: true` (13 occurrences in `vm.go`) becomes an explicit
  `RequiresReplace()` **plan modifier**, attached per-attribute instead of a
  boolean flag buried in the schema map.
- The "shown-once secrets" trick — `vm.go:276-292` manually reads
  `d.Get("private_key")` during `Read` and writes it straight back so
  Terraform doesn't see a diff — becomes the built-in
  `stringplanmodifier.UseStateForUnknown()` plan modifier. That's roughly 15
  lines of hand-written state-preservation logic replaced by one line of
  declared intent, and it can no longer be forgotten on a future field
  because it's declared next to the attribute, not buried in `Read`.

**Before — Create (current, `resourceVMCreate`):**

```go
func resourceVMCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)
	tenantID := d.Get("tenant_id").(string)
	networkName := d.Get("network_name").(string)
	vnetID := d.Get("vnet_id").(string)
	subnetID := d.Get("subnet_id").(string)

	hasLegacy := networkName != ""
	hasVPC := vnetID != "" || subnetID != ""
	if hasLegacy && hasVPC {
		return diag.FromErr(fmt.Errorf("invalid network configuration: ..."))
	}
	// ...

	req := client.VMCreateRequest{
		Name: d.Get("name").(string),
		Size: d.Get("size").(string),
		// ...
	}
	resp, err := c.CreateVM(ctx, tenantID, projectID, req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("error creating VirtualMachine: %w", err))
	}

	d.SetId(fmt.Sprintf("%s/%s/%s", tenantID, projectID, resp.Resource.ID))
	var diags diag.Diagnostics
	diags = appendSet(diags, d, "private_key", resp.PrivateKey)
	diags = appendSet(diags, d, "status", resp.Resource.Status)
	// ... 5 more appendSet calls
	if diags.HasError() {
		return diags
	}
	if err := waitForVMActive(ctx, c, tenantID, projectID, resp.Resource.ID, d.Timeout(schema.TimeoutCreate)); err != nil {
		return diag.FromErr(err)
	}
	return resourceVMRead(ctx, d, meta)
}
```

Note the `network_name`/`vnet_id`/`subnet_id` mutual-exclusion check is
**hand-rolled business logic inside Create**, and every field is read with a
runtime `.(string)`/`.(int)` type assertion — 15+ of them in this function
alone, each one a potential panic if a schema type is ever changed without
updating the assertion.

**After — Create:**

```go
func (r *vmResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vmResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	hasLegacy := plan.NetworkName.ValueString() != ""
	hasVPC := plan.VNetID.ValueString() != "" || plan.SubnetID.ValueString() != ""
	if hasLegacy && hasVPC {
		resp.Diagnostics.AddError("Invalid network configuration",
			"Provide EITHER network_name (legacy) OR vnet_id + subnet_id (VPC), not both.")
		return
	}
	// ...same business rule, no type assertions needed — plan fields are already typed

	createReq := client.VMCreateRequest{
		Name:      plan.Name.ValueString(),
		Size:      plan.Size.ValueString(),
		DiskGB:    int(plan.DiskGB.ValueInt64()),
		ImageName: plan.ImageName.ValueString(),
	}
	apiResp, err := r.client.CreateVM(ctx, plan.TenantID.ValueString(), plan.ProjectID.ValueString(), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Error creating VirtualMachine", err.Error())
		return
	}

	plan.ID = types.StringValue(fmt.Sprintf("%s/%s/%s", plan.TenantID.ValueString(), plan.ProjectID.ValueString(), apiResp.Resource.ID))
	plan.PrivateKey = types.StringValue(apiResp.PrivateKey)
	plan.Status = types.StringValue(apiResp.Resource.Status)
	// ... assign remaining computed fields directly onto the struct

	if err := waitForVMActive(ctx, r.client, plan.TenantID.ValueString(), plan.ProjectID.ValueString(), apiResp.Resource.ID, 15*time.Minute); err != nil {
		resp.Diagnostics.AddError("Error waiting for VirtualMachine to become active", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
```

The mutual-exclusion validation logic itself is unchanged — the framework
doesn't remove business logic, it removes **incidental** complexity around
it: no `.(string)` assertions, no `appendSet` helper needed (the repo's own
`helpers.go` shim exists specifically to paper over the fact that
`d.Set` doesn't return a diagnostic-compatible error — that helper disappears
entirely), and diagnostics attach naturally to `resp.Diagnostics` instead of
being threaded through every function's return value by hand.

**Async polling (`waitForVMActive`/`waitForVMDeleted` using
`retry.StateChangeConf`)** carries over essentially unchanged — this is one
of the few things the framework does not replace. `retry.StateChangeConf`
still lives in `terraform-plugin-sdk/v2/helper/retry` (a small, standalone
subpackage; you can depend on the framework and this one SDKv2 helper
subpackage together without pulling in all of SDKv2). All 9 resources that
poll today will keep essentially the same polling loop, just fed typed model
fields instead of `d.Get(...)`.

### 2.3 `CustomizeDiff` → `ValidateConfig` (route_table.go)

**Before:**

```go
func routeTableCustomizeDiff(ctx context.Context, d *schema.ResourceDiff, meta interface{}) error {
	routes := d.Get("route").([]interface{})
	for _, r := range routes {
		route := r.(map[string]interface{})
		if route["next_hop_type"] != "virtual_appliance" && route["next_hop_ip"] != "" {
			return fmt.Errorf("next_hop_ip is only valid when next_hop_type is virtual_appliance")
		}
	}
	return nil
}
```

Note the untyped `[]interface{}` → `map[string]interface{}` double
type-assertion just to inspect one nested field — this is the single most
error-prone pattern in SDKv2 nested-block code.

**After:**

```go
func (r *routeTableResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config routeTableResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var routes []routeModel
	config.Route.ElementsAs(ctx, &routes, false)
	for i, route := range routes {
		if route.NextHopType.ValueString() != "virtual_appliance" && route.NextHopIP.ValueString() != "" {
			resp.Diagnostics.AddAttributeError(
				path.Root("route").AtListIndex(i).AtName("next_hop_ip"),
				"Invalid route configuration",
				"next_hop_ip is only valid when next_hop_type is \"virtual_appliance\".",
			)
		}
	}
}
```

`route` is a real `[]routeModel` typed slice instead of nested
`interface{}` maps, and the error can be pinpointed to
`route[i].next_hop_ip` in Terraform's plan output — SDKv2's `CustomizeDiff`
can only return a single flat `error` with no attribute path.

### 2.4 Data source: `dcapi_project`

**Before:**

```go
func dataSourceProjectRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := meta.(*client.DCAPIClient)
	tenantID := d.Get("tenant_id").(string)
	projectID := d.Get("project_id").(string)
	p, err := c.GetProjectByID(ctx, tenantID, projectID)
	if err != nil {
		return diag.FromErr(err)
	}
	if p == nil {
		return diag.Errorf("project %q not found", projectID)
	}
	d.SetId(fmt.Sprintf("%s/%s", tenantID, projectID))
	var diags diag.Diagnostics
	diags = appendSet(diags, d, "name", p.Name)
	// ... ~14 more appendSet calls
	return diags
}
```

**After:**

```go
func (d *projectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config projectDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	p, err := d.client.GetProjectByID(ctx, config.TenantID.ValueString(), config.ProjectID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading project", err.Error())
		return
	}
	if p == nil {
		resp.Diagnostics.AddError("Project not found", fmt.Sprintf("project %q not found", config.ProjectID.ValueString()))
		return
	}

	config.ID = types.StringValue(fmt.Sprintf("%s/%s", config.TenantID.ValueString(), config.ProjectID.ValueString()))
	config.Name = types.StringValue(p.Name)
	// ... assign remaining ~14 fields directly

	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
```

Roughly the same length, but the ~14 `appendSet(diags, d, "field", value)`
calls (a helper that exists purely to reconcile `d.Set`'s `error` return with
`diag.Diagnostics`) become plain struct field assignments, and one
`resp.State.Set(ctx, &config)` at the end serializes everything at once with
compile-time field-name checking.

## 3. Performance differences

The two SDKs speak the **same Terraform plugin wire protocol** (gRPC over a
Unix socket, negotiated as protocol v5 or v6) — migrating does not change how
Terraform Core talks to the binary, and does not reduce network calls made by
the provider itself (`internal/client/*.go` stays exactly the same, so calls
against DC-API are unaffected either way). The performance differences are
narrower but real:

1. **CPU cost of encoding/decoding values.** SDKv2 represents every
   attribute internally as `map[string]interface{}` / `[]interface{}`
   ("cty"-adjacent dynamic values), then reflects over `schema.Schema` at
   runtime on every `d.Get`/`d.Set` call to figure out how to coerce types.
   The framework decodes protocol values directly into your typed Go structs
   via `tfsdk` struct tags using `reflect` only once per request (not once
   per field access), which is measurably cheaper for resources with many
   attributes or deeply nested blocks — `cluster.go` (499 lines, nested
   node-pool blocks) and `nsg.go` (300 lines, nested rule lists) are this
   repo's resources that would benefit most.
2. **Protocol v6 without a shim.** SDKv2 alone only speaks protocol v5;
   getting v6-only features (e.g. richer, more precise null/unknown
   propagation for nested attributes) requires wrapping the provider with
   `tf5to6server.UpgradeServer`, adding a translation layer at every RPC.
   The framework speaks v6 natively, so nested-block-heavy resources like
   `cluster.go`/`nsg.go`/`vnet.go` (which currently use
   `schema.TypeList`/`TypeSet` with `Elem: &schema.Resource{}`) get more
   accurate plan diffs without an extra translation hop per request.
3. **Fewer full-state round trips for unchanged sensitive fields.** Today,
   `vm.go`'s Read explicitly re-`Set`s `private_key`/`console_password`
   every single Read to avoid Terraform computing a false diff. This is
   correct but is dead weight recomputed on every refresh. With
   `UseStateForUnknown()` as a declared plan modifier, the framework decides
   this at plan time from the schema declaration rather than executing
   extra logic inside every Read call.
4. **Where it does *not* help:** none of this changes the actual bottleneck
   in this provider today, which is the DC-API HTTP round trips and the
   `retry.StateChangeConf` polling loops (15s `MinTimeout`, up to 15 minutes
   for VM creation). Migrating the schema/CRUD layer has no effect on wall-clock
   apply time for VM/cluster/subnet provisioning — that's bounded by the
   backend API, not the Terraform SDK layer.

In short: the performance win is real but modest — it shows up as lower CPU
overhead per plan/apply cycle (especially for the nested-block resources),
not as faster infrastructure provisioning.

## 4. Code-quality / maintainability advantages

These are the advantages that actually matter for a provider of this size
(19 resources, 6,900 lines, zero tests today):

- **Compile-time type safety.** Every `d.Get("x").(string)` in this codebase
  (100+ occurrences) is a potential panic if a schema type is changed without
  updating every call site — the compiler cannot catch a mismatch between
  `schema.TypeInt` and `.(string)`. Typed models make this a compile error
  instead of a runtime panic discovered during `terraform apply`.
- **Null vs. unknown vs. zero-value, correctly.** SDKv2 collapses "not set in
  config" and "set to the zero value" in several code paths, which is why
  `vm.go` needs `hasLegacy := networkName != ""` string-emptiness checks to
  detect "was this optional field provided." `types.String` in the framework
  distinguishes `Null` (not set), `Unknown` (not known until apply), and a
  real value explicitly, removing a category of "optional field bugs."
- **Attribute-scoped diagnostics.** SDKv2's `diag.FromErr`/`CustomizeDiff`
  return errors with no attribute path; the framework's
  `AddAttributeError(path.Root("route").AtListIndex(i)...)` lets Terraform
  point at the exact nested field (seen directly in the `route_table.go`
  example above) — meaningfully better UX for users of this provider when
  they misconfigure nested blocks in `cluster.go`, `nsg.go`, `vnet.go`,
  `node_pool.go`, `dns_record.go`, `route_table.go`.
- **Plan modifiers replace scattered hand-written workarounds.** The
  `ForceNew` flag, the shown-once-secret Read hack, and `CustomizeDiff` all
  become declarative, composable `planmodifier`/`validator` values attached
  next to the attribute they affect, instead of being logic buried inside
  Create/Read/CustomizeDiff functions that's easy to forget when adding a new
  field.
- **No `interface{}` "meta" threading.** Every one of the 31 resource/data
  source files in this repo repeats `c := meta.(*client.DCAPIClient)`. The
  framework still needs one assertion (in `Configure`), but it's centralized
  per-resource rather than repeated inside every CRUD function, and the
  provider-configured client is exposed on the resource struct instead of an
  untyped parameter passed through every call.
- **`terraform-plugin-testing` unlocks real acceptance tests.** Since this
  provider currently has zero automated tests, migrating is a natural point
  to introduce `resource.Test`/`resource.UnitTest` coverage — this tooling
  works identically well for framework or SDKv2 providers, so it's not
  strictly a migration requirement, but doing both together means new
  resources get compile-time safety *and* test coverage from day one instead
  of accumulating more untested SDKv2 code first.
- **Long-term support.** HashiCorp has stated `terraform-plugin-sdk/v2` is in
  maintenance mode — new Terraform protocol features (e.g. write-only
  arguments, richer function support, deferred actions) land in the
  framework first or exclusively. Staying on SDKv2 means this provider
  gradually falls behind newer Terraform Core capabilities.

## 5. Migration cost estimate for this repo

| Item | Effort driver |
|---|---|
| Provider (`provider.go`) | Small — one file, ~140 lines, mechanical rewrite |
| Simple resources (no nesting, no polling): `tenant.go`, `service_account.go`, `tenant_member.go`, `key_vault_secret.go`, `route_table_association.go`, `nsg_attachment.go` | Low — direct 1:1 field mapping |
| Resources with polling only, no nesting: `bastion.go`, `subnet.go`, `private_endpoint.go`, `vnet_peering.go` | Medium — polling loop logic carries over via `retry` subpackage, schema/CRUD rewritten |
| Resources with nested blocks (`schema.TypeList`/`TypeSet` + `Elem`): `cluster.go`, `node_pool.go`, `nsg.go`, `vnet.go`, `route_table.go`, `dns_record.go` | High — nested blocks need `types.List`/`types.Set` of `types.Object` with `NestedAttributeObject`, plus `ElementsAs`/`ElementsAs` conversions; this is the bulk of the migration risk |
| `route_table.go`'s `CustomizeDiff` | Medium — rewritten as `ValidateConfig`, logic itself is simple |
| Data sources (12 files) | Low–Medium — no Create/Update/Delete, same field-mapping pattern as `vm.go`/`project.go` above |
| Net-new test suite | Not required by the migration itself, but recommended to do concurrently since none exists |
| Client layer (`internal/client/`) | **No changes needed** — already SDK-agnostic |

A reasonable approach given zero existing tests: migrate one simple resource
first end-to-end (e.g. `tenant.go`) to validate the pattern and provider
scaffolding, then tackle nested-block resources (`cluster.go`, `nsg.go`)
last since they carry the highest risk of subtly changed plan behavior.
Both SDKs can coexist in the same provider binary via
`tf5muxserver`/`tf6muxserver`, so this migration can be done resource-by-resource
incrementally rather than as one big-bang rewrite.

---

*This document is based on the current state of the `terraform-provider-dcapi`
branch as of 2026-09-14. Code samples labeled "Before" are taken directly
from the repo; "After" samples are illustrative migration targets, not yet
implemented.*
