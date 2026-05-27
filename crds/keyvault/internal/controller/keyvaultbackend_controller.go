// Package controller hosts the KVI controllers.
//
// KeyVaultBackendReconciler manages the lifecycle of a per-tenant OpenBao
// HA cluster:
//   1. Render + apply child objects (SA, RBAC, Services, ConfigMap, StatefulSet)
//   2. Wait for pod 0 to be Running
//   3. Init OpenBao on pod 0; persist root_token + unseal keys in a Secret
//   4. Unseal pod 0 with the threshold number of keys
//   5. As followers come up: raft-join + unseal each
//   6. Watch for leader election → set status.endpoint + phase Ready
//
// Steps 1-2 are step 3 of docs/kvi-controller-design.md §10.
// Steps 3-6 are step 4 (this file).
package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	keyvaultv1alpha1 "github.com/wso2/keyvault-operator/api/v1alpha1"
	"github.com/wso2/keyvault-operator/internal/controller/openbao"
	"github.com/wso2/keyvault-operator/internal/controller/render"
)

const (
	// fieldManager identifies this controller in server-side-apply
	// FieldManager registers so we can co-own fields cleanly with
	// future controllers.
	fieldManager = "kvi-keyvaultbackend"

	phasePending      = "Pending"
	phaseProvisioning = "Provisioning"
	phaseReady        = "Ready"
	phaseFailed       = "Failed"
	phaseTerminating  = "Terminating"

	conditionReady = "Ready"

	// Shamir defaults: 5 key shares, threshold of 3 to unseal. Matches
	// the recommended default in the OpenBao docs and the openbao-helm
	// chart's init pattern. Adjustable per-Backend via spec later.
	defaultShares    = 5
	defaultThreshold = 3

	// Port that the OpenBao API listens on (matches the StatefulSet
	// container port and the listener block in the ConfigMap).
	openbaoAPIPort = 8200

	// Backend finalizer. Refuses delete while KVIs still reference us,
	// then explicitly tears down child objects + PVCs (Longhorn retains
	// PVCs by default) + the keys Secret.
	backendFinalizer = "keyvault.opencloud.wso2.com/backend-cleanup"
)

// KeyVaultBackendReconciler reconciles a KeyVaultBackend object.
type KeyVaultBackendReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	// RESTConfig is required for the openbao package to construct a REST
	// client that talks to pods via the kube API server proxy. Manager
	// exposes this via GetConfig().
	RESTConfig *rest.Config
}

// +kubebuilder:rbac:groups=keyvault.opencloud.wso2.com,resources=keyvaultbackends,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=keyvault.opencloud.wso2.com,resources=keyvaultbackends/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=keyvault.opencloud.wso2.com,resources=keyvaultbackends/finalizers,verbs=update
// +kubebuilder:rbac:groups=keyvault.opencloud.wso2.com,resources=keyvaultinstances,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;configmaps;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=pods/proxy,verbs=get;create;update
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete

func (r *KeyVaultBackendReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cr keyvaultv1alpha1.KeyVaultBackend
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get KeyVaultBackend: %w", err)
	}

	// Ensure finalizer is present on a healthy CR. On deletion, divert
	// into handleDelete.
	if cr.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&cr, backendFinalizer) {
			controllerutil.AddFinalizer(&cr, backendFinalizer)
			if err := r.Update(ctx, &cr); err != nil {
				return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
			}
			return ctrl.Result{Requeue: true}, nil
		}
	} else {
		return r.handleBackendDelete(ctx, &cr, log)
	}

	// 1. Render + apply child objects.
	applied, err := r.applyChildren(ctx, &cr)
	if err != nil {
		return r.transient(ctx, req.NamespacedName, nil, "apply children", err)
	}

	// 2. Inspect StatefulSet — bail early until we have pods to talk to.
	var sts appsv1.StatefulSet
	if err := r.Get(ctx, client.ObjectKey{Namespace: cr.Namespace, Name: render.StatefulSetName(&cr)}, &sts); err != nil {
		return r.transient(ctx, req.NamespacedName, applied, "get StatefulSet", err)
	}
	if sts.Status.Replicas == 0 {
		// StatefulSet hasn't created pod 0 yet — wait.
		return r.requeueWithStatus(ctx, req.NamespacedName, applied, phaseProvisioning,
			"StatefulSet has no pods yet; awaiting StatefulSet controller", 10*time.Second)
	}

	// 3. Get pod 0; it has to be Running before we can hit the OpenBao API.
	pod0, err := r.getPod(ctx, cr.Namespace, render.StatefulSetName(&cr)+"-0")
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.requeueWithStatus(ctx, req.NamespacedName, applied, phaseProvisioning,
				"pod-0 not yet created", 5*time.Second)
		}
		return r.transient(ctx, req.NamespacedName, applied, "get pod-0", err)
	}
	if pod0.Status.Phase != corev1.PodRunning {
		return r.requeueWithStatus(ctx, req.NamespacedName, applied, phaseProvisioning,
			fmt.Sprintf("pod-0 phase=%s; waiting for Running", pod0.Status.Phase), 5*time.Second)
	}

	// 4. Init the cluster on pod 0 if not already initialised.
	if cr.Status.KeyMaterialRef == nil {
		if err := r.ensureInitialised(ctx, &cr, applied); err != nil {
			return r.transient(ctx, req.NamespacedName, applied, "init pod-0", err)
		}
	}

	// 5. Load key material we persisted (either just now or in a prior run).
	km, err := openbao.LoadKeyMaterialSecret(ctx, r.Client, cr.Namespace, cr.Name)
	if err != nil {
		return r.transient(ctx, req.NamespacedName, applied, "load key material", err)
	}

	// 6. Unseal pod 0 (idempotent — Unseal is a no-op when already unsealed).
	if err := r.unsealPod(ctx, &cr, pod0.Name, km.UnsealKeys); err != nil {
		return r.transient(ctx, req.NamespacedName, applied, "unseal pod-0", err)
	}

	// 7. For each follower replica: raft-join (if not initialised) + unseal.
	desiredReplicas := int32(defaultIfZero(cr.Spec.EngineConfig.HAReplicas, defaultShares-2))
	for i := int32(1); i < desiredReplicas; i++ {
		podName := fmt.Sprintf("%s-%d", render.StatefulSetName(&cr), i)
		pod, err := r.getPod(ctx, cr.Namespace, podName)
		if err != nil {
			if apierrors.IsNotFound(err) {
				// StatefulSet OrderedReady policy hasn't rolled this one yet —
				// requeue and try again next pass.
				return r.requeueWithStatus(ctx, req.NamespacedName, applied, phaseProvisioning,
					fmt.Sprintf("waiting for %s to be created", podName), 10*time.Second)
			}
			return r.transient(ctx, req.NamespacedName, applied, fmt.Sprintf("get %s", podName), err)
		}
		if pod.Status.Phase != corev1.PodRunning {
			return r.requeueWithStatus(ctx, req.NamespacedName, applied, phaseProvisioning,
				fmt.Sprintf("waiting for %s to reach Running (phase=%s)", podName, pod.Status.Phase), 5*time.Second)
		}
		if err := r.joinAndUnsealFollower(ctx, &cr, pod.Name, km); err != nil {
			if errors.Is(err, errFollowerJoining) {
				return r.requeueWithStatus(ctx, req.NamespacedName, applied, phaseProvisioning,
					fmt.Sprintf("raft-join sent to %s; waiting for seal config to propagate", podName), 5*time.Second)
			}
			return r.transient(ctx, req.NamespacedName, applied, fmt.Sprintf("join+unseal %s", podName), err)
		}
	}

	// 8. Verify leader election on pod 0.
	cli, err := openbao.NewClient(r.RESTConfig, cr.Namespace, pod0.Name, openbaoAPIPort)
	if err != nil {
		return r.transient(ctx, req.NamespacedName, applied, "build openbao client", err)
	}
	leader, err := cli.Leader(ctx)
	if err != nil {
		return r.transient(ctx, req.NamespacedName, applied, "query leader", err)
	}
	if leader.LeaderAddress == "" {
		return r.requeueWithStatus(ctx, req.NamespacedName, applied, phaseProvisioning,
			"waiting for Raft leader election", 5*time.Second)
	}

	// NOTE on audit: OpenBao refuses sys/audit enable via the API as a
	// hardening policy ("cannot enable audit device via API; use
	// declarative, config-based audit device management instead"). Audit
	// is declared in the HCL we render into the ConfigMap (see
	// render/configmap.go::renderHCL). The pod template carries an
	// hcl-checksum annotation so a ConfigMap edit triggers a rolling
	// restart; no runtime API call needed here.

	// 9. Ensure the dc-api scoped policy + token exist. dc-api should never
	// hold the root token — instead it gets a token bound to a "dc-api-admin"
	// policy with CRUD only on tenants/+/+/* paths (no sys/*, no auth/*).
	// Idempotent: skip if the Secret already exists.
	cli.SetToken(km.RootToken)
	if err := r.ensureDCAPIToken(ctx, &cr, cli); err != nil {
		return r.transient(ctx, req.NamespacedName, applied, "provision dc-api token", err)
	}

	// 10. All pods unsealed AND leader elected → Ready.
	address := fmt.Sprintf("%s.%s.svc.cluster.local", render.ServiceActiveName(&cr), cr.Namespace)
	if err := r.updateStatus(ctx, req.NamespacedName, func(s *keyvaultv1alpha1.KeyVaultBackendStatus) {
		s.Phase = phaseReady
		s.Message = fmt.Sprintf("Raft leader %s; %d replicas Running", leader.LeaderAddress, desiredReplicas)
		s.Resources = applied
		s.Endpoint = &keyvaultv1alpha1.BackendEndpoint{Address: address, Port: openbaoAPIPort}
		setReadyConditionOn(s, metav1.ConditionTrue, "Ready", "OpenBao Raft cluster ready")
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status to Ready: %w", err)
	}

	// Steady-state: re-check every minute to catch drift (pod restart, etc.).
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// dcAPIPolicyName is the OpenBao ACL policy bound to the dc-api scoped
// token. Single shared policy across all per-tenant Backends — the policy
// itself is path-templated with `tenants/+/+/...` so it matches whatever
// mounts dc-api creates on this Backend over time.
const dcAPIPolicyName = "dc-api-admin"

// dcAPIPolicyHCL is the ACL policy granted to dc-api's scoped token. It
// covers exactly the operations dc-api's handlers perform; no sys/*, no
// auth/* general access, no token-create, no sys/audit, no sys/mounts.
// The mount-creation step is the operator's job (root token); the per-vault
// Instance reconciler sets up the mount and the AppRole before dc-api ever
// touches them. dc-api's auth/approle/* grants are narrowly scoped to the
// per-vault role name (kv-<uuid>) and only cover the rotation surface
// (mint + list + destroy secret_id), NOT role mutation, deletion, or
// policy binding — those stay with the operator.
const dcAPIPolicyHCL = `# dc-api scoped policy. Path-templated: matches any tenant_uuid + vault_uuid.

# ── KV-v2 data plane (read / write / list / soft-delete / restore) ──
path "tenants/+/+/data/*" {
  capabilities = ["create", "read", "update", "delete"]
}
path "tenants/+/+/metadata/*" {
  capabilities = ["read", "list", "delete"]
}
path "tenants/+/+/metadata" {
  capabilities = ["list"]
}
path "tenants/+/+/delete/*" {
  capabilities = ["update"]
}
path "tenants/+/+/undelete/*" {
  capabilities = ["update"]
}

# ── AppRole secret_id rotation ──
# Mint a new secret_id + list existing accessors so old ones can be destroyed
# for an atomic rotate. Does NOT grant role mutation, role deletion, or
# policy attach — the role itself stays operator-owned.
#
# Note 1: the secret-id endpoint serves BOTH create (POST → update cap)
# and list-accessors (LIST → list cap) — same path, different verb.
# Note 2: OpenBao policy "+" matches a whole path segment; partial-segment
# wildcards (e.g. "kv-+") do NOT match. In practice the only AppRoles on
# this Backend are kv-<vault-uuid> (operator-created), so granting on the
# bare "+" is equivalent in effect. F-followup: tighten via templated
# ACLs once we have other AppRoles to distinguish from.
path "auth/approle/role/+/secret-id" {
  capabilities = ["update", "list"]
}
path "auth/approle/role/+/secret-id-accessor/destroy" {
  capabilities = ["update"]
}
`

// ensureDCAPIToken writes the dc-api-admin policy + mints a scoped token,
// stored in <backend>-dcapi-token Secret. Idempotent: if the Secret already
// exists, skip (token is long-lived).
func (r *KeyVaultBackendReconciler) ensureDCAPIToken(ctx context.Context, cr *keyvaultv1alpha1.KeyVaultBackend, cli *openbao.Client) error {
	log := logf.FromContext(ctx)

	// (a) Policy first. PUT /sys/policies/acl/<name> is idempotent — safe
	// to call every reconcile.
	if err := cli.WritePolicy(ctx, dcAPIPolicyName, dcAPIPolicyHCL); err != nil {
		return fmt.Errorf("write %s policy: %w", dcAPIPolicyName, err)
	}

	// (b) Token only if not already minted. We never overwrite the Secret
	// once written — the live token stays valid for the life of the Backend.
	exists, err := openbao.DCAPITokenSecretExists(ctx, r.Client, cr.Namespace, cr.Name)
	if err != nil {
		return fmt.Errorf("check dcapi-token secret: %w", err)
	}
	if exists {
		return nil
	}

	log.Info("minting dc-api-admin token", "backend", cr.Name)
	token, err := cli.CreateToken(ctx, openbao.CreateTokenRequest{
		Policies:  []string{dcAPIPolicyName},
		Renewable: true,
		// Periodic token (auto-renew window 720h = 30 days). dc-api uses
		// the token on every secret-CRUD call, which auto-renews via
		// OpenBao's token-lease machinery — so as long as dc-api is alive
		// at least once per 30 days, the token never expires. If dc-api
		// goes silent for >30 days the token finally expires; re-minting
		// is automatic on the next reconcile (Secret stays around so the
		// idempotency guard skips, but operators can delete it to force
		// a fresh mint).
		Period:          "720h",
		NoDefaultPolicy: true,
		DisplayName:     dcAPIPolicyName,
	})
	if err != nil {
		return fmt.Errorf("mint dc-api token: %w", err)
	}

	owner := metav1.OwnerReference{
		APIVersion:         cr.APIVersion,
		Kind:               cr.Kind,
		Name:               cr.Name,
		UID:                cr.UID,
		Controller:         ptr(true),
		BlockOwnerDeletion: ptr(true),
	}
	if owner.APIVersion == "" {
		owner.APIVersion = keyvaultv1alpha1.GroupVersion.String()
		owner.Kind = "KeyVaultBackend"
	}

	if err := openbao.CreateDCAPITokenSecret(ctx, r.Client, cr, owner, token, render.AllLabels(cr)); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("write dcapi-token secret: %w", err)
		}
		// Lost the race — another reconcile beat us to it. Fine.
	}
	return nil
}

// handleBackendDelete runs the Backend finalizer.
//
//   1. Refuse if any KeyVaultInstance still references this Backend
//      (re-checks on requeue; the user must delete dependent KVIs first).
//   2. Explicit DELETE of every owned object (we don't rely on owner-ref
//      cascade because we need deterministic ordering + want PVC cleanup
//      after pod drain).
//   3. Poll until the StatefulSet's pods are gone.
//   4. Delete the per-pod data PVCs (Longhorn whenDeleted=Retain means
//      they don't disappear when the StatefulSet does).
//   5. Delete the key material Secret last.
//   6. Remove finalizer so k8s can GC the CR.
func (r *KeyVaultBackendReconciler) handleBackendDelete(ctx context.Context, cr *keyvaultv1alpha1.KeyVaultBackend, log logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(cr, backendFinalizer) {
		return ctrl.Result{}, nil
	}

	// 1. Cascade-protect against dependent KVIs.
	var kvis keyvaultv1alpha1.KeyVaultInstanceList
	if err := r.List(ctx, &kvis); err != nil {
		return ctrl.Result{}, fmt.Errorf("list KVIs: %w", err)
	}
	dependents := 0
	var blockerNames []string
	for _, kvi := range kvis.Items {
		if kvi.Spec.BackendRef.Name == cr.Name && kvi.Spec.BackendRef.Namespace == cr.Namespace {
			dependents++
			blockerNames = append(blockerNames, kvi.Namespace+"/"+kvi.Name)
		}
	}
	if dependents > 0 {
		msg := fmt.Sprintf("blocked by %d KeyVaultInstance(s): %v — delete them first",
			dependents, blockerNames)
		log.Info("Backend delete blocked", "dependents", dependents)
		key := client.ObjectKey{Namespace: cr.Namespace, Name: cr.Name}
		_ = r.updateStatus(ctx, key, func(s *keyvaultv1alpha1.KeyVaultBackendStatus) {
			s.Phase = phaseTerminating
			s.Message = msg
			setReadyConditionOn(s, metav1.ConditionFalse, "Blocked", msg)
		})
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}

	// 2. Delete owned objects. List is intentional so we get deterministic
	// ordering (StatefulSet last among workloads, otherwise the pods
	// linger). Best-effort: NotFound is treated as success.
	toDelete := []client.Object{
		render.StatefulSet(cr),
		render.RegularService(cr),
		render.ActiveService(cr),
		render.StandbyService(cr),
		render.InternalService(cr),
		render.ConfigMap(cr),
		render.PodPatchRoleBinding(cr),
		render.PodPatchRole(cr),
		render.ServiceAccount(cr),
	}
	for _, obj := range toDelete {
		if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete %T %s: %w", obj, obj.GetName(), err)
		}
	}

	// 3. Wait for the StatefulSet's pods to actually disappear before
	// touching PVCs (they're still held by pod volumeMounts otherwise).
	var pods corev1.PodList
	if err := r.List(ctx, &pods,
		client.InNamespace(cr.Namespace),
		client.MatchingLabels{"app.kubernetes.io/instance": cr.Name},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list pods: %w", err)
	}
	if len(pods.Items) > 0 {
		log.Info("waiting for pods to terminate", "remaining", len(pods.Items))
		_ = r.updateStatus(ctx, client.ObjectKey{Namespace: cr.Namespace, Name: cr.Name},
			func(s *keyvaultv1alpha1.KeyVaultBackendStatus) {
				s.Phase = phaseTerminating
				s.Message = fmt.Sprintf("waiting for %d pod(s) to terminate", len(pods.Items))
			})
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	// 4. Delete data PVCs (Longhorn whenDeleted=Retain leaves them).
	var pvcs corev1.PersistentVolumeClaimList
	if err := r.List(ctx, &pvcs,
		client.InNamespace(cr.Namespace),
		client.MatchingLabels{"app.kubernetes.io/instance": cr.Name},
	); err != nil {
		return ctrl.Result{}, fmt.Errorf("list pvcs: %w", err)
	}
	// PVCs created by the StatefulSet volumeClaimTemplate don't carry the
	// instance label by default — fall back to name pattern data-<sts>-<i>.
	prefix := "data-" + render.StatefulSetName(cr) + "-"
	if len(pvcs.Items) == 0 {
		var allPVCs corev1.PersistentVolumeClaimList
		if err := r.List(ctx, &allPVCs, client.InNamespace(cr.Namespace)); err != nil {
			return ctrl.Result{}, fmt.Errorf("list namespace pvcs: %w", err)
		}
		for _, pvc := range allPVCs.Items {
			if strings.HasPrefix(pvc.Name, prefix) {
				pvcs.Items = append(pvcs.Items, pvc)
			}
		}
	}
	for i := range pvcs.Items {
		if err := r.Delete(ctx, &pvcs.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete pvc %s: %w", pvcs.Items[i].Name, err)
		}
	}

	// 5. Delete key material Secret. After this point recovery requires
	// re-init, which destroys the Raft data — but the data PVCs are
	// already gone by now, so this is consistent.
	if cr.Status.KeyMaterialRef != nil {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: cr.Namespace,
				Name:      cr.Status.KeyMaterialRef.Name,
			},
		}
		if err := r.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("delete key material secret: %w", err)
		}
	}

	// 6. Drop finalizer; k8s GC removes the CR.
	controllerutil.RemoveFinalizer(cr, backendFinalizer)
	if err := r.Update(ctx, cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// applyChildren renders + server-side-applies every owned object and
// returns the ResourceRefs to record in status.
func (r *KeyVaultBackendReconciler) applyChildren(ctx context.Context, cr *keyvaultv1alpha1.KeyVaultBackend) ([]keyvaultv1alpha1.ResourceRef, error) {
	objs := []client.Object{
		render.ServiceAccount(cr),
		render.PodPatchRole(cr),
		render.PodPatchRoleBinding(cr),
		render.ConfigMap(cr),
		render.InternalService(cr),
		render.RegularService(cr),
		render.ActiveService(cr),
		render.StandbyService(cr),
		render.StatefulSet(cr),
	}
	var refs []keyvaultv1alpha1.ResourceRef
	for _, obj := range objs {
		if err := controllerutil.SetControllerReference(cr, obj, r.Scheme); err != nil {
			return nil, fmt.Errorf("set owner ref on %T %s: %w", obj, obj.GetName(), err)
		}
		if err := r.Patch(ctx, obj, client.Apply, client.FieldOwner(fieldManager), client.ForceOwnership); err != nil {
			return nil, fmt.Errorf("apply %T %s/%s: %w", obj, obj.GetNamespace(), obj.GetName(), err)
		}
		refs = append(refs, keyvaultv1alpha1.ResourceRef{
			APIVersion: obj.GetObjectKind().GroupVersionKind().GroupVersion().String(),
			Kind:       obj.GetObjectKind().GroupVersionKind().Kind,
			Namespace:  obj.GetNamespace(),
			Name:       obj.GetName(),
		})
	}
	return refs, nil
}

// ensureInitialised runs Init on pod 0 if the cluster isn't yet
// initialised, then writes the resulting key material into an owned
// Secret and updates status.keyMaterialRef.
func (r *KeyVaultBackendReconciler) ensureInitialised(ctx context.Context, cr *keyvaultv1alpha1.KeyVaultBackend, applied []keyvaultv1alpha1.ResourceRef) error {
	log := logf.FromContext(ctx)

	pod0Name := render.StatefulSetName(cr) + "-0"
	cli, err := openbao.NewClient(r.RESTConfig, cr.Namespace, pod0Name, openbaoAPIPort)
	if err != nil {
		return fmt.Errorf("build openbao client: %w", err)
	}

	// Idempotency guard: maybe init ran earlier but we crashed before
	// writing the Secret. Use sys/seal-status (always returns 200) rather
	// than sys/health (returns 503 when sealed, which the kube API proxy
	// surfaces as a generic "server unable to handle request" error).
	ss, err := cli.SealStatus(ctx)
	if err != nil {
		return fmt.Errorf("check pod-0 seal-status: %w", err)
	}
	if ss.Initialized {
		// Cluster was initialised but we have no Secret. The keys are lost
		// — this is unrecoverable without operator intervention. Surface
		// loudly rather than re-init (which would destroy the existing
		// Raft cluster's data).
		return fmt.Errorf("pod-0 reports initialized=true but no key material Secret exists; manual recovery required")
	}

	log.Info("initialising OpenBao", "pod", pod0Name, "shares", defaultShares, "threshold", defaultThreshold)
	initResp, err := cli.Init(ctx, defaultShares, defaultThreshold)
	if err != nil {
		return fmt.Errorf("POST /v1/sys/init: %w", err)
	}

	// Persist before doing anything else with the keys.
	owner := metav1.OwnerReference{
		APIVersion:         cr.APIVersion,
		Kind:               cr.Kind,
		Name:               cr.Name,
		UID:                cr.UID,
		Controller:         ptr(true),
		BlockOwnerDeletion: ptr(true),
	}
	// If APIVersion/Kind are empty (typed fetch strips TypeMeta), recover them:
	if owner.APIVersion == "" {
		owner.APIVersion = keyvaultv1alpha1.GroupVersion.String()
		owner.Kind = "KeyVaultBackend"
	}

	labels := render.AllLabels(cr)
	km := openbao.KeyMaterial{
		RootToken:  initResp.RootToken,
		UnsealKeys: initResp.KeysBase64,
	}
	if err := openbao.CreateKeyMaterialSecret(ctx, r.Client, cr, owner, km, labels); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("write key material secret: %w", err)
		}
	}

	// Record on status so subsequent reconciles can skip init.
	key := client.ObjectKey{Namespace: cr.Namespace, Name: cr.Name}
	return r.updateStatus(ctx, key, func(s *keyvaultv1alpha1.KeyVaultBackendStatus) {
		s.KeyMaterialRef = &corev1.LocalObjectReference{Name: openbao.KeyMaterialSecretName(cr.Name)}
		s.Resources = applied
	})
}

// unsealPod submits the first `defaultThreshold` keys to the named pod.
// Idempotent — if the pod is already unsealed the first Unseal returns
// Sealed=false and we exit.
func (r *KeyVaultBackendReconciler) unsealPod(ctx context.Context, cr *keyvaultv1alpha1.KeyVaultBackend, podName string, keys []string) error {
	cli, err := openbao.NewClient(r.RESTConfig, cr.Namespace, podName, openbaoAPIPort)
	if err != nil {
		return err
	}
	status, err := cli.SealStatus(ctx)
	if err != nil {
		return fmt.Errorf("read seal status: %w", err)
	}
	if !status.Sealed {
		return nil
	}
	for i := 0; i < defaultThreshold && i < len(keys); i++ {
		st, err := cli.Unseal(ctx, keys[i])
		if err != nil {
			return fmt.Errorf("Unseal key #%d: %w", i, err)
		}
		if !st.Sealed {
			return nil
		}
	}
	final, err := cli.SealStatus(ctx)
	if err != nil {
		return fmt.Errorf("verify seal status: %w", err)
	}
	if final.Sealed {
		return fmt.Errorf("unsealed %d/%d keys but pod still sealed (progress=%d)", defaultThreshold, len(keys), final.Progress)
	}
	return nil
}

// joinAndUnsealFollower brings a follower pod into the Raft cluster.
// Step 1: if not yet initialised, raft-join via the leader's internal
// Service DNS — then RETURN errFollowerJoining so the caller requeues.
// raft-join is async; the seal config takes a few seconds to propagate
// from the leader's snapshot, and trying to Unseal before that races
// the cluster into "progress=0 after N keys" land.
// Step 2 (next reconcile, when Initialized=true): unseal with the
// threshold keys.
func (r *KeyVaultBackendReconciler) joinAndUnsealFollower(ctx context.Context, cr *keyvaultv1alpha1.KeyVaultBackend, podName string, km openbao.KeyMaterial) error {
	log := logf.FromContext(ctx)

	cli, err := openbao.NewClient(r.RESTConfig, cr.Namespace, podName, openbaoAPIPort)
	if err != nil {
		return err
	}
	status, err := cli.SealStatus(ctx)
	if err != nil {
		return fmt.Errorf("read seal status: %w", err)
	}

	// Followers come up uninitialised; raft-join propagates the leader's
	// init state so the keys we have actually decrypt the seal.
	if !status.Initialized {
		leaderAPI := fmt.Sprintf("http://%s-0.%s.%s.svc.cluster.local:%d",
			render.StatefulSetName(cr),
			render.ServiceInternalName(cr),
			cr.Namespace,
			openbaoAPIPort)
		log.Info("joining follower to Raft", "pod", podName, "leader_api_addr", leaderAPI)
		if err := cli.RaftJoin(ctx, leaderAPI); err != nil {
			return fmt.Errorf("raft join: %w", err)
		}
		// raft-join is async — return a sentinel so the reconciler
		// requeues with a short delay instead of trying to unseal a
		// pod whose seal config hasn't propagated yet.
		return errFollowerJoining
	}

	if !status.Sealed {
		return nil
	}
	return r.unsealPod(ctx, cr, podName, km.UnsealKeys)
}

// errFollowerJoining — sentinel returned by joinAndUnsealFollower when
// raft-join just fired and we need to give the seal config time to
// propagate before attempting Unseal. Treat as a requeue, not a fail.
var errFollowerJoining = fmt.Errorf("follower raft-join in progress")

func (r *KeyVaultBackendReconciler) getPod(ctx context.Context, namespace, name string) (*corev1.Pod, error) {
	var p corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// requeueWithStatus updates status to a transient Provisioning state and
// schedules a requeue after the given delay. Use for "waiting for X"
// situations that aren't errors.
func (r *KeyVaultBackendReconciler) requeueWithStatus(ctx context.Context, key client.ObjectKey, applied []keyvaultv1alpha1.ResourceRef, phase, message string, after time.Duration) (ctrl.Result, error) {
	err := r.updateStatus(ctx, key, func(s *keyvaultv1alpha1.KeyVaultBackendStatus) {
		s.Phase = phase
		s.Message = message
		if len(applied) > 0 {
			s.Resources = applied
		}
		setReadyConditionOn(s, metav1.ConditionFalse, phase, message)
	})
	return ctrl.Result{RequeueAfter: after}, err
}

func (r *KeyVaultBackendReconciler) fail(ctx context.Context, cr *keyvaultv1alpha1.KeyVaultBackend, err error) (ctrl.Result, error) {
	key := client.ObjectKey{Namespace: cr.Namespace, Name: cr.Name}
	if updErr := r.updateStatus(ctx, key, func(s *keyvaultv1alpha1.KeyVaultBackendStatus) {
		s.Phase = phaseFailed
		s.Message = err.Error()
		setReadyConditionOn(s, metav1.ConditionFalse, "Error", err.Error())
	}); updErr != nil {
		logf.FromContext(ctx).Error(updErr, "failed to update status after reconcile error")
	}
	return ctrl.Result{}, err
}

// transient handles errors expected to clear up on their own — k8s API
// hiccups, OpenBao not-ready, intermittent RBAC propagation. Sets
// Phase=Provisioning with the error message in status, requeues after a
// fixed delay (no exponential backoff — controller-runtime applies its
// own when we return err != nil, and we explicitly return nil to skip it).
// Logs each invocation so operators tailing the log see the retry storm
// even though the CR status stays in Provisioning.
//
// Use this for any error originating from the network / external systems.
// Reserve `fail` (Phase=Failed) for genuinely-terminal CR-spec errors that
// will NEVER succeed without the user editing the CR (none in practice
// today for KeyVaultBackend; the CRD's openAPIV3Schema catches malformed
// specs at admission time).
func (r *KeyVaultBackendReconciler) transient(
	ctx context.Context,
	key client.ObjectKey,
	applied []keyvaultv1alpha1.ResourceRef,
	step string,
	err error,
) (ctrl.Result, error) {
	logf.FromContext(ctx).Info("transient reconcile error; will requeue",
		"step", step, "error", err.Error(),
		"requeue_after", "15s")
	return r.requeueWithStatus(ctx, key, applied, phaseProvisioning,
		fmt.Sprintf("%s: %v", step, err), 15*time.Second)
}

// updateStatus fetches the freshest CR, applies the mutator, and Status-Updates
// it. Retries once on RV conflict (single retry is enough — we only race
// against our own reconciler, not parallel writers).
func (r *KeyVaultBackendReconciler) updateStatus(ctx context.Context, key client.ObjectKey, mutate func(*keyvaultv1alpha1.KeyVaultBackendStatus)) error {
	for attempt := 0; attempt < 2; attempt++ {
		var fresh keyvaultv1alpha1.KeyVaultBackend
		if err := r.Get(ctx, key, &fresh); err != nil {
			return err
		}
		mutate(&fresh.Status)
		err := r.Status().Update(ctx, &fresh)
		if err == nil {
			return nil
		}
		if !apierrors.IsConflict(err) {
			return err
		}
	}
	return fmt.Errorf("status update: too many conflicts")
}

func setReadyConditionOn(s *keyvaultv1alpha1.KeyVaultBackendStatus, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i := range s.Conditions {
		if s.Conditions[i].Type == conditionReady {
			s.Conditions[i].Status = status
			s.Conditions[i].Reason = reason
			s.Conditions[i].Message = message
			s.Conditions[i].LastTransitionTime = now
			return
		}
	}
	s.Conditions = append(s.Conditions, metav1.Condition{
		Type:               conditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

func defaultIfZero(n, def int) int {
	if n == 0 {
		return def
	}
	return n
}

func ptr[T any](v T) *T { return &v }

// SetupWithManager sets up the controller with the Manager.
func (r *KeyVaultBackendReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.RESTConfig == nil {
		r.RESTConfig = mgr.GetConfig()
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&keyvaultv1alpha1.KeyVaultBackend{}).
		Owns(&appsv1.StatefulSet{}, builder.WithPredicates()).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.Secret{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Named("keyvaultbackend").
		Complete(r)
}
