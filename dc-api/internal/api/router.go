// Package api wires together the Chi router, middleware, and handlers.
//
// This file is the "composition root" of DC-API.
// The composition root is the single place where all dependencies are assembled.
// Think of it like the wiring diagram in an electrical schematic:
//   - Components (handlers, middleware) are defined elsewhere.
//   - The router is where the wires connect.
//
// Why keep this separate from main.go?
//   main.go handles OS concerns (signals, exit codes, env loading).
//   router.go handles HTTP concerns (routes, middleware order).
//   Testing router.go is easy: call NewRouter() with mock deps, send test requests.
//   You don't need to spin up the actual server process.
package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
	dcapi "github.com/wso2/dc-api"
	"github.com/wso2/dc-api/internal/api/auth"
	"github.com/wso2/dc-api/internal/api/handlers"
	"github.com/wso2/dc-api/internal/api/middleware"
	"github.com/wso2/dc-api/internal/db"
	"github.com/wso2/dc-api/internal/models"
	"github.com/wso2/dc-api/internal/providers"
	"github.com/wso2/dc-api/internal/providers/endpoints"
)

// RouterDeps bundles the dependencies needed to build the router.
// This is a form of Dependency Injection at the router level.
// main.go constructs this struct and passes it to NewRouter.
type RouterDeps struct {
	Repo            *db.Repository
	ComputeProvider providers.ComputeProvider
	ClusterProvider providers.ClusterProvider
	NetworkProvider providers.NetworkProvider
	// NATProvisioner is optional (nil if the network provider doesn't support VPC NAT,
	// e.g. in integration tests that don't have the external network configured).
	// When non-nil, the VNet handler uses it to provision SNAT for every new VPC.
	NATProvisioner  providers.VPCNATProvisioner
	// DNSProvisioner is optional (nil if F20 DNS is not configured).
	// When non-nil, the subnet handler uses it to provision per-VPC CoreDNS.
	DNSProvisioner  providers.VPCDNSProvisioner
	// DNSSearchDomain is the optional search domain injected into VPC VMs (F20).
	// Sourced from DCAPI_VPC_DNS_SEARCH_DOMAIN. Empty = no extra search domain.
	DNSSearchDomain string
	// BastionImage / BastionMgmtNAD configure F10 bastion provisioning.
	// Sourced from DCAPI_BASTION_IMAGE and DCAPI_BASTION_MGMT_NAD.
	BastionImage   string
	BastionMgmtNAD string
	// InfraReservedNADs (F21) is the set of `namespace/nad-name` references
	// the VM handler refuses on the legacy bridge path. Built from
	// DCAPI_INFRA_RESERVED_NADS at startup.
	InfraReservedNADs map[string]bool
	// NSProvisioner provisions the Kubernetes namespace + ResourceQuota for new
	// projects. Nil skips namespace creation (acceptable in tests or when running
	// without a Kubernetes backend).
	NSProvisioner providers.ProjectNamespaceProvisioner
	// TenantNSProvisioner provisions the per-tenant Kubernetes namespace
	// "dc-tenant-<slug>" at admin-tenant-create time. Hosts tenant-tier
	// managed-service Backends (keyvault HA cluster etc.). Nil skips the
	// step — same fallback as NSProvisioner.
	TenantNSProvisioner providers.TenantNamespaceProvisioner
	// KVIProvisioner drives the KVI operator's CRDs (KeyVaultBackend +
	// KeyVaultInstance). Nil means dc-api falls back to the chunk-1+2
	// behaviour (logical CRUD only; no backing OpenBao integration).
	KVIProvisioner providers.KVIProvisioner
	// DatabaseProvisioner drives the dbaas operator's DBInstance CRD
	// (Task 1). Nil means dc-api falls back to DB-only synchronous CRUD —
	// tests + no-K8s deployments.
	DatabaseProvisioner providers.DatabaseProvisioner
	// DBaaSOSImage is the operator-configured Harvester VM image
	// ("namespace/name") database VMs boot from (DCAPI_DBAAS_OS_IMAGE).
	// Empty defers to the controller's own default.
	DBaaSOSImage string
	// EndpointProvisioner is the generic Private Endpoint provisioner used by
	// every M3 managed service (Key Vault today; Postgres / Valkey / Harbor
	// later). Nil disables /v1/<service>/{id}/private-endpoints routes.
	EndpointProvisioner endpoints.Provisioner
	// KeyVaultBackendAddr / Port locate the OpenBao backend the KV Private
	// Endpoint proxy forwards to. When EndpointProvisioner is non-nil these
	// must be set (sourced from DCAPI_KV_BACKEND_ADDR / DCAPI_KV_BACKEND_PORT).
	KeyVaultBackendAddr string
	KeyVaultBackendPort int
	AuthMiddleware      middleware.AuthValidator
	// AuthService (F7 BFF) is optional. When non-nil, dc-api serves
	// /v1/auth/{login,callback,logout,me} for cloud-ui's session-cookie
	// auth path. When nil, those routes are not registered and the only
	// auth surface is the Bearer-header /v1/* dcctl uses.
	AuthService *auth.Service
	// TenantGroupPrefix mirrors the auth middleware's setting. Used by
	// POST /v1/admin/tenants to derive the Asgardeo group name from the
	// supplied tenant id.
	TenantGroupPrefix string
	Log               zerolog.Logger
}

// NewRouter creates and returns the fully configured Chi router.
// It wires all middleware, handlers, and routes.
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	// ── Global middleware (applied to ALL routes) ─────────────────────────────
	r.Use(chimiddleware.RealIP)
	r.Use(requestLogger(deps.Log))
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(60 * time.Second))

	// Override Chi's default 404 handler so unknown routes return the
	// spec's Error envelope as JSON. Without this Chi writes "404 page
	// not found\n" as text/plain, which violates every operation's
	// documented application/json response.
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"error":"method not allowed"}`))
	})

	// ── Health check (unauthenticated) ───────────────────────────────────────
	// Kubernetes liveness/readiness probes hit this endpoint.
	// It must NOT require auth — the probe has no JWT.
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// ── Spec serving (unauthenticated) ───────────────────────────────────────
	// /openapi.yaml — the raw embedded spec for tooling (Postman, dcctl
	// codegen via URL, partner SDK generators).
	// /docs        — Redoc HTML page that renders the spec for humans.
	// Both are deliberately public. The spec describes the API surface but
	// doesn't leak secrets, and exposing it is what makes the API
	// self-documenting for any new consumer.
	r.Get("/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(dcapi.OpenAPISpec)
	})
	r.Get("/docs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write([]byte(redocHTML))
	})

	// ── F7 BFF auth (unauthenticated, only registered when enabled) ─────────
	// /v1/auth/login + /v1/auth/callback are the OIDC handshake — no
	// session exists yet, so they can't sit behind the JWT middleware.
	// /v1/auth/logout + /v1/auth/me read the session cookie directly
	// (don't need the bearer middleware to inject a context).
	//
	// Mounted at /v1/auth so reverse proxies can route the whole /v1
	// prefix to dc-api without carving out exceptions.
	if deps.AuthService != nil {
		r.Get("/v1/auth/login", deps.AuthService.HandleLogin(deps.Log))
		r.Get("/v1/auth/callback", deps.AuthService.HandleCallback(deps.Log))
		r.Post("/v1/auth/logout", deps.AuthService.HandleLogout(deps.Log))
		r.Get("/v1/auth/me", deps.AuthService.HandleMe(deps.Log))
	}

	// ── API v1 (authenticated) ────────────────────────────────────────────────
	// All /v1/* routes require a valid Asgardeo JWT.
	// The auth middleware is applied only inside this route group.
	//
	// Using route groups (r.Route) keeps auth scoped to /v1 only.
	// If we add a public endpoint later (/v1/versions, /v1/catalog),
	// we can do so without touching the auth middleware.
	r.Route("/v1", func(r chi.Router) {
		// Apply auth middleware to the entire /v1 group.
		r.Use(deps.AuthMiddleware.Validate)

		// Instantiate handlers with their dependencies.
		// Dependency Injection: we pass repo and provider INTO the handler.
		// The handler does not create these — it receives them.
		vmHandler := handlers.NewVMHandler(deps.Repo, deps.ComputeProvider, deps.DNSSearchDomain, deps.InfraReservedNADs, deps.Log)
		bastionHandler := handlers.NewBastionHandler(deps.Repo, deps.ComputeProvider, deps.BastionImage, deps.BastionMgmtNAD, deps.DNSSearchDomain, deps.Log)
		clusterHandler := handlers.NewClusterHandler(deps.Repo, deps.ClusterProvider, deps.Log)

		// M1.5 member management handler.
		memberHandler := handlers.NewMemberHandler(deps.Repo, deps.Log)

		// M1.5 Chunk 7 — service account management handler.
		serviceAccountHandler := handlers.NewServiceAccountHandler(deps.Repo, deps.Log)

		// M2 network handlers — all share the same NetworkProvider.
		vnetHandler := handlers.NewVNetHandler(deps.Repo, deps.NetworkProvider, deps.NATProvisioner, deps.DNSProvisioner, deps.Log)
		subnetHandler := handlers.NewSubnetHandler(deps.Repo, deps.NetworkProvider, deps.NATProvisioner, deps.DNSProvisioner, deps.Log)
		rtHandler := handlers.NewRouteTableHandler(deps.Repo, deps.NetworkProvider, deps.Log)
		nsgHandler := handlers.NewNSGHandler(deps.Repo, deps.NetworkProvider, deps.Log)
		peeringHandler := handlers.NewPeeringHandler(deps.Repo, deps.NetworkProvider, deps.Log)
		dnsHandler := handlers.NewPrivateDnsZoneHandler(deps.Repo, deps.NetworkProvider, deps.Log)

		// Tenant list — used by cloud-ui's tenant switcher. Lives at the
		// /v1/ level (not under /v1/tenants/{tenant_id}) because it's how
		// the caller discovers which tenants exist for them in the first
		// place — no tenant_id is known yet.
		tenantHandler := handlers.NewTenantHandler(deps.Repo, deps.Log)
		r.Get("/tenants", tenantHandler.List) // GET /v1/tenants

		// Admin tenant registry — pre-register empty tenants so they're
		// visible to GET /v1/tenants before any member has logged in.
		// Platform-admin-only (enforced in the handler itself).
		adminTenantHandler := handlers.NewAdminTenantHandler(deps.Repo, deps.TenantGroupPrefix, deps.TenantNSProvisioner, deps.Log)
		r.Post("/admin/tenants", adminTenantHandler.Create) // POST /v1/admin/tenants

		// PATCH /v1/admin/tenants/{tenant_id} — adjust the tenant capacity cap.
		// Goes through TenantContext so the slug resolves to a tenant_uuid; the
		// handler enforces the platform-admin requirement itself.
		r.Route("/admin/tenants/{tenant_id}", func(r chi.Router) {
			r.Use(middleware.NewTenantContext(deps.Repo).Validate)
			r.Patch("/", adminTenantHandler.PatchCap) // PATCH /v1/admin/tenants/{tenant_id}
		})

		// ── M3 chunk 1+2: Key Vault handler + optional endpoint handler ────
		// Constructed once and reused inside the tenant-scoped route block
		// below.
		kvHandler := handlers.NewKeyVaultHandler(deps.Repo, deps.KVIProvisioner, deps.TenantNSProvisioner, deps.Log)
		var kvEpHandler *handlers.PrivateEndpointHandler
		if deps.EndpointProvisioner != nil {
			kvLookup := &handlers.KeyVaultTargetLookup{Repo: deps.Repo}
			kvResolver := &handlers.KeyVaultBackendResolver{
				Addr: deps.KeyVaultBackendAddr,
				Port: deps.KeyVaultBackendPort,
			}
			kvEpHandler = handlers.NewPrivateEndpointHandler(
				deps.Repo,
				deps.EndpointProvisioner,
				models.PrivateEndpointTargetKeyVault,
				"kv",
				"id",
				kvLookup,
				kvResolver,
				deps.Log,
			)
		}

		// ── Tenant-scoped routes ────────────────────────────────────────────
		// /v1/tenants/{tenant_id}/...
		// TenantContext middleware resolves the URL slug to the immutable
		// tenant_uuid and enforces that the caller has at least Viewer access.
		tenantCtx := middleware.NewTenantContext(deps.Repo)
		projectCtx := middleware.NewProjectContext(deps.Repo)
		projectHandler := handlers.NewProjectHandler(deps.Repo, deps.NSProvisioner, deps.Log)

		r.Route("/tenants/{tenant_id}", func(r chi.Router) {
			r.Use(tenantCtx.Validate)

			// ── M2.5 Project management ─────────────────────────────────────
			// POST /projects is tenant-owner-only; GET /projects is any member.
			r.Route("/projects", func(r chi.Router) {
				r.Post("/", projectHandler.Create) // POST /v1/tenants/{tenant_id}/projects
				r.Get("/", projectHandler.List)    // GET  /v1/tenants/{tenant_id}/projects

				// ── Project-scoped subroutes ────────────────────────────────
				// ProjectContext validates access and injects project_id / project_uuid.
				r.Route("/{project_id}", func(r chi.Router) {
					r.Use(projectCtx.Validate)

					r.Get("/", projectHandler.Get)       // GET    /v1/tenants/{tid}/projects/{pid}
					r.Patch("/", projectHandler.Patch)   // PATCH  /v1/tenants/{tid}/projects/{pid}
					r.Delete("/", projectHandler.Delete) // DELETE /v1/tenants/{tid}/projects/{pid}

					// ── M1.5 Chunk 7 — Service accounts (project-scoped) ────
					r.Route("/service-accounts", func(r chi.Router) {
						r.Post("/", serviceAccountHandler.Create)          // POST   .../service-accounts
						r.Get("/", serviceAccountHandler.List)             // GET    .../service-accounts
						r.Get("/{sa_id}", serviceAccountHandler.Get)       // GET    .../service-accounts/{sa_id}
						r.Delete("/{sa_id}", serviceAccountHandler.Delete) // DELETE .../service-accounts/{sa_id}
					})

					// ── Virtual Machines ────────────────────────────────────
					r.Route("/virtual-machines", func(r chi.Router) {
						r.Post("/", vmHandler.Create)       // POST   .../virtual-machines
						r.Get("/", vmHandler.List)          // GET    .../virtual-machines
						r.Get("/{id}", vmHandler.Get)       // GET    .../virtual-machines/{id}
						r.Delete("/{id}", vmHandler.Delete) // DELETE .../virtual-machines/{id}
					})

					// ── Bastions (F10) ──────────────────────────────────────
					r.Route("/bastions", func(r chi.Router) {
						r.Post("/", bastionHandler.Create)       // POST   .../bastions
						r.Get("/", bastionHandler.List)          // GET    .../bastions
						r.Get("/{id}", bastionHandler.Get)       // GET    .../bastions/{id}
						r.Delete("/{id}", bastionHandler.Delete) // DELETE .../bastions/{id}
					})

					// ── Clusters ────────────────────────────────────────────
					r.Route("/clusters", func(r chi.Router) {
						r.Post("/", clusterHandler.Create) // POST   .../clusters
						r.Get("/", clusterHandler.List)    // GET    .../clusters

						r.Route("/{id}", func(r chi.Router) {
							r.Get("/", clusterHandler.Get)                      // GET    .../clusters/{id}
							r.Delete("/", clusterHandler.Delete)                // DELETE .../clusters/{id}
							r.Get("/kubeconfig", clusterHandler.GetKubeconfig)  // GET    .../clusters/{id}/kubeconfig

							// ── AKS-style node pool management (R5) ────────────
							r.Route("/node-pools", func(r chi.Router) {
								r.Get("/", clusterHandler.ListNodePools)    // GET    .../node-pools
								r.Post("/", clusterHandler.AddNodePool)     // POST   .../node-pools

								r.Route("/{pool_name}", func(r chi.Router) {
									r.Get("/", clusterHandler.GetNodePool)             // GET    .../node-pools/{pool_name}
									r.Patch("/", clusterHandler.ScaleOrUpdateNodePool) // PATCH  .../node-pools/{pool_name}
									r.Delete("/", clusterHandler.RemoveNodePool)       // DELETE .../node-pools/{pool_name}
								})
							})
						})
					})

					// ── M2 Networking — VNets ────────────────────────────────
					r.Route("/vnets", func(r chi.Router) {
						r.Post("/", vnetHandler.Create) // POST   .../vnets
						r.Get("/", vnetHandler.List)    // GET    .../vnets

						r.Route("/{vnet_id}", func(r chi.Router) {
							r.Get("/", vnetHandler.Get)       // GET    .../vnets/{vnet_id}
							r.Delete("/", vnetHandler.Delete) // DELETE .../vnets/{vnet_id}

							// Subnets
							r.Route("/subnets", func(r chi.Router) {
								r.Post("/", subnetHandler.Create)              // POST   .../subnets
								r.Get("/", subnetHandler.List)                 // GET    .../subnets
								r.Get("/{subnet_id}", subnetHandler.Get)       // GET    .../subnets/{subnet_id}
								r.Delete("/{subnet_id}", subnetHandler.Delete) // DELETE .../subnets/{subnet_id}
							})

							// Route Tables
							r.Route("/route-tables", func(r chi.Router) {
								r.Post("/", rtHandler.Create) // POST   .../route-tables
								r.Get("/", rtHandler.List)    // GET    .../route-tables

								r.Route("/{rt_id}", func(r chi.Router) {
									r.Get("/", rtHandler.Get)          // GET    .../route-tables/{rt_id}
									r.Put("/", rtHandler.UpdateRoutes) // PUT    .../route-tables/{rt_id}
									r.Delete("/", rtHandler.Delete)    // DELETE .../route-tables/{rt_id}

									r.Post("/associations", rtHandler.Associate)                  // POST   .../associations
									r.Delete("/associations/{assoc_id}", rtHandler.Disassociate) // DELETE .../associations/{assoc_id}
								})
							})

							// VNet Peerings
							r.Route("/peerings", func(r chi.Router) {
								r.Post("/", peeringHandler.Create)               // POST   .../peerings
								r.Get("/", peeringHandler.List)                  // GET    .../peerings
								r.Get("/{peering_id}", peeringHandler.Get)       // GET    .../peerings/{peering_id}
								r.Delete("/{peering_id}", peeringHandler.Delete) // DELETE .../peerings/{peering_id}
							})

							// Private DNS Zones
							r.Route("/dns-zones", func(r chi.Router) {
								r.Post("/", dnsHandler.CreateZone) // POST   .../dns-zones
								r.Get("/", dnsHandler.ListZones)   // GET    .../dns-zones

								r.Route("/{zone_id}", func(r chi.Router) {
									r.Get("/", dnsHandler.GetZone)       // GET    .../dns-zones/{zone_id}
									r.Delete("/", dnsHandler.DeleteZone) // DELETE .../dns-zones/{zone_id}

									r.Post("/records", dnsHandler.UpsertRecord)               // POST   .../records
									r.Get("/records", dnsHandler.ListRecords)                 // GET    .../records
									r.Get("/records/{record_id}", dnsHandler.GetRecord)       // GET    .../records/{record_id}
									r.Put("/records/{record_id}", dnsHandler.UpdateRecord)    // PUT    .../records/{record_id}
									r.Delete("/records/{record_id}", dnsHandler.DeleteRecord) // DELETE .../records/{record_id}
								})
							})
						})
					})

					// ── M2 Networking — NSGs ─────────────────────────────────
					r.Route("/security-groups", func(r chi.Router) {
						r.Post("/", nsgHandler.Create) // POST   .../security-groups
						r.Get("/", nsgHandler.List)    // GET    .../security-groups

						r.Route("/{sg_id}", func(r chi.Router) {
							r.Get("/", nsgHandler.Get)       // GET    .../security-groups/{sg_id}
							r.Delete("/", nsgHandler.Delete) // DELETE .../security-groups/{sg_id}

							r.Put("/rules", nsgHandler.UpdateRules) // PUT    .../security-groups/{sg_id}/rules

							r.Post("/attachments", nsgHandler.Attach)                   // POST   .../attachments
							r.Delete("/attachments/{attachment_id}", nsgHandler.Detach) // DELETE .../attachments/{attachment_id}
						})
					})

					// ── M3 Key Vaults ───────────────────────────────────────
					r.Route("/keyvaults", func(r chi.Router) {
						r.Post("/", kvHandler.Create)                       // POST   .../keyvaults
						r.Get("/", kvHandler.List)                          // GET    .../keyvaults
						r.Get("/{id}", kvHandler.Get)                       // GET    .../keyvaults/{id}
						r.Delete("/{id}", kvHandler.Delete)                 // DELETE .../keyvaults/{id}
						r.Get("/{id}/credentials", kvHandler.Credentials)            // GET    .../keyvaults/{id}/credentials (shown-once)
						r.Post("/{id}/credentials/rotate", kvHandler.RotateCredentials) // POST   .../keyvaults/{id}/credentials/rotate (atomic rotate, shown-once)

						// ── M3 chunk 3 — Secret CRUD (proxy to OpenBao) ─────
						// Routes registered only when the KVI operator is wired
						// (kvi != nil). When nil, requests to these paths return
						// the Chi 404 handler (JSON {"error":"not found"}).
						if deps.KVIProvisioner != nil {
							kvSecretsHandler := handlers.NewKeyVaultSecretsHandler(deps.Repo, deps.KVIProvisioner, deps.Log)
							r.Get("/{id}/secrets", kvSecretsHandler.ListKeyVaultSecrets)           // GET    .../secrets
							r.Get("/{id}/secrets/{key}", kvSecretsHandler.GetKeyVaultSecret)       // GET    .../secrets/{key}
							r.Put("/{id}/secrets/{key}", kvSecretsHandler.PutKeyVaultSecret)       // PUT    .../secrets/{key}
							r.Delete("/{id}/secrets/{key}", kvSecretsHandler.DeleteKeyVaultSecret)         // DELETE .../secrets/{key}
							r.Post("/{id}/secrets/{key}/restore", kvSecretsHandler.RestoreKeyVaultSecret) // POST   .../secrets/{key}/restore
						}

						if kvEpHandler != nil {
							r.Route("/{id}/private-endpoints", func(r chi.Router) {
								r.Post("/", kvEpHandler.Create)
								r.Get("/", kvEpHandler.List)
								r.Get("/{ep_id}", kvEpHandler.Get)
								r.Delete("/{ep_id}", kvEpHandler.Delete)
							})
						}
					})

					// ── Task 1 — DBaaS Databases ────────────────────────────
					dbHandler := handlers.NewDatabaseHandler(deps.Repo, deps.DatabaseProvisioner, deps.DBaaSOSImage, deps.Log)
					r.Route("/databases", func(r chi.Router) {
						r.Post("/", dbHandler.Create)                     // POST   .../databases
						r.Get("/", dbHandler.List)                        // GET    .../databases
						r.Get("/{id}", dbHandler.Get)                     // GET    .../databases/{id}
						r.Delete("/{id}", dbHandler.Delete)               // DELETE .../databases/{id}
						r.Get("/{id}/credentials", dbHandler.Credentials) // GET    .../databases/{id}/credentials (shown-once)
					})
				})
			})

			// ── Tenant-level endpoints (no project scope required) ──────────

			// M1.5 Tenant membership management
			r.Route("/members", func(r chi.Router) {
				r.Post("/", memberHandler.Invite)                 // POST   /v1/tenants/{tenant_id}/members
				r.Get("/", memberHandler.List)                    // GET    /v1/tenants/{tenant_id}/members
				r.Delete("/{principal_id}", memberHandler.Remove) // DELETE /v1/tenants/{tenant_id}/members/{principal_id}
			})

			// Images and NADs remain at tenant scope (catalog resources, not project resources)
			r.Get("/images", vmHandler.ListImages)   // GET  /v1/tenants/{tenant_id}/images
			r.Post("/images", vmHandler.CreateImage) // POST /v1/tenants/{tenant_id}/images
			r.Get("/networks", vmHandler.ListNetworks) // GET /v1/tenants/{tenant_id}/networks

			// Capacity cap + current allocation across projects, in one shot.
			// Used by the RegisterProjectDialog to show "X cpu available" inline
			// so the user knows what fits before submitting. Any tenant member
			// can read; mutation is admin-only via PATCH /v1/admin/tenants/{tid}.
			r.Get("/cap-usage", projectHandler.GetTenantCapUsage) // GET /v1/tenants/{tenant_id}/cap-usage
		})
	})

	return r
}

// requestLogger returns a middleware that logs each request as a structured
// zerolog JSON line, consistent with the rest of DC-API's log output.
//
// Example output:
//
//	{"level":"info","method":"POST","path":"/v1/virtual-machines","status":202,"bytes":312,"duration_ms":47,"remote":"10.0.0.1","time":"..."}
func requestLogger(log zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			status := ww.Status()
			log.Info().
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", status).
				Int("bytes", ww.BytesWritten()).
				Int64("duration_ms", time.Since(start).Milliseconds()).
				Str("remote", r.RemoteAddr).
				Msg("request")
		})
	}
}
