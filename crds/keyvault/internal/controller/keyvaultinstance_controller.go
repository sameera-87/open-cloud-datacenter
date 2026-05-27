// KeyVaultInstanceReconciler manages user-facing Key Vaults:
//   1. Resolve the parent KeyVaultBackend; wait until it's Ready.
//   2. Find the Backend's current Raft leader pod.
//   3. Enable a kv-v2 mount at tenants/<tenant-uuid>/<resource-uuid>/.
//   4. Configure the kv-v2 soft-delete window.
//   5. Write an ACL policy scoped to the mount path.
//   6. Enable approle auth on the Backend (idempotent, once per Backend).
//   7. Create an AppRole bound to the policy.
//   8. Mint a secret_id (ONCE per KeyVaultInstance — never re-mint).
//   9. Persist {role_id, secret_id, mount_path, backend_address,
//      backend_port} in an owned credentials Secret in the project ns.
//   10. Set status.endpoint + Phase=Ready.
package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	keyvaultv1alpha1 "github.com/wso2/keyvault-operator/api/v1alpha1"
	"github.com/wso2/keyvault-operator/internal/controller/openbao"
	"github.com/wso2/keyvault-operator/internal/controller/render"
)

const (
	kviFieldManager = "kvi-keyvaultinstance"

	// Label keys used to build the mount path. Must match
	// dc-api/internal/providers/common.StandardLabels.
	labelTenantUUID   = "dc-api.wso2.com/tenant-uuid"
	labelResourceUUID = "dc-api.wso2.com/resource-uuid"

	// Default AppRole TTLs — the user re-authenticates every hour.
	approleTokenTTL    = "1h"
	approleTokenMaxTTL = "4h"

	// Default soft-delete window when spec doesn't override.
	defaultSoftDeleteDays = 30

	// Finalizer key. The reconciler adds it on first sight; on delete the
	// finalizer is held until the AppRole / policy / mount cleanup
	// succeeds on the Backend, then removed so k8s can GC the CR.
	kviFinalizer = "keyvault.opencloud.wso2.com/keyvault-cleanup"
)

// KeyVaultInstanceReconciler reconciles a KeyVaultInstance object.
type KeyVaultInstanceReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	RESTConfig *rest.Config
}

// +kubebuilder:rbac:groups=keyvault.opencloud.wso2.com,resources=keyvaultinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=keyvault.opencloud.wso2.com,resources=keyvaultinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=keyvault.opencloud.wso2.com,resources=keyvaultinstances/finalizers,verbs=update

func (r *KeyVaultInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var cr keyvaultv1alpha1.KeyVaultInstance
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get KeyVaultInstance: %w", err)
	}

	// Ensure finalizer is present before any provisioning so deletion
	// cleanup is guaranteed.
	if cr.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(&cr, kviFinalizer) {
			controllerutil.AddFinalizer(&cr, kviFinalizer)
			if err := r.Update(ctx, &cr); err != nil {
				return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
			}
			return ctrl.Result{Requeue: true}, nil
		}
	} else {
		return r.handleDelete(ctx, &cr, log)
	}

	// 1. Resolve Backend.
	var backend keyvaultv1alpha1.KeyVaultBackend
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: cr.Spec.BackendRef.Namespace,
		Name:      cr.Spec.BackendRef.Name,
	}, &backend); err != nil {
		if apierrors.IsNotFound(err) {
			return r.requeueKVI(ctx, &cr,
				fmt.Sprintf("Backend %s/%s not found; waiting for dc-api to create it",
					cr.Spec.BackendRef.Namespace, cr.Spec.BackendRef.Name),
				30*time.Second)
		}
		return r.transientKVI(ctx, &cr, "get Backend", err)
	}
	if backend.Status.Phase != phaseReady {
		return r.requeueKVI(ctx, &cr,
			fmt.Sprintf("Backend %s phase=%s; waiting for Ready", backend.Name, backend.Status.Phase),
			15*time.Second)
	}
	if backend.Status.KeyMaterialRef == nil {
		// Backend reports Ready but its key-material Secret reference is
		// unset. Either Backend just transitioned and hasn't written status
		// yet (race), or there's an actual operator bug. Treat as transient
		// so we requeue rather than locking the CR in Failed.
		return r.transientKVI(ctx, &cr,
			"Backend key material",
			fmt.Errorf("Backend %s reports Ready but has no key material Secret", backend.Name))
	}

	// 2. Load root token.
	km, err := openbao.LoadKeyMaterialSecret(ctx, r.Client, backend.Namespace, backend.Name)
	if err != nil {
		return r.transientKVI(ctx, &cr, "load Backend key material", err)
	}

	// 3. Find leader pod by polling each replica.
	leaderPod, err := r.findLeaderPod(ctx, &backend)
	if err != nil {
		return r.requeueKVI(ctx, &cr,
			fmt.Sprintf("waiting for Raft leader: %v", err), 10*time.Second)
	}

	cli, err := openbao.NewClient(r.RESTConfig, backend.Namespace, leaderPod, openbaoAPIPort)
	if err != nil {
		return r.transientKVI(ctx, &cr, "build openbao client", err)
	}
	cli.SetToken(km.RootToken)

	// 4. Build mount path + policy name from labels (dc-api stamps these).
	tuuid := cr.Labels[labelTenantUUID]
	ruuid := cr.Labels[labelResourceUUID]
	if tuuid == "" || ruuid == "" {
		// Genuinely terminal: dc-api must stamp these labels at CR creation.
		// If they're missing, the CR was hand-crafted incorrectly or
		// dc-api has a regression; user must delete + recreate the CR.
		return r.failKVI(ctx, &cr,
			fmt.Errorf("missing required labels %s/%s on KVI %s",
				labelTenantUUID, labelResourceUUID, cr.Name))
	}
	mountPath := fmt.Sprintf("tenants/%s/%s", tuuid, ruuid)
	policyName := "kv-" + ruuid
	roleName := "kv-" + ruuid

	// 5. Enable kv-v2 mount (idempotent).
	if err := cli.EnableKVv2Mount(ctx, mountPath); err != nil {
		return r.transientKVI(ctx, &cr, "enable mount", err)
	}

	// 6. Configure soft-delete window. Spec value (or default 30 days)
	// converted to Go-duration hours, which OpenBao accepts.
	softDeleteDays := cr.Spec.SoftDeleteDays
	if softDeleteDays == 0 {
		softDeleteDays = defaultSoftDeleteDays
	}
	if err := cli.ConfigureKVv2(ctx, mountPath, fmt.Sprintf("%dh", softDeleteDays*24)); err != nil {
		return r.transientKVI(ctx, &cr, "configure mount", err)
	}

	// 7. Write policy.
	policyHCL := fmt.Sprintf(`path "%s/data/*" { capabilities = ["create","read","update","delete"] }
path "%s/metadata/*" { capabilities = ["list","read","delete"] }
`, mountPath, mountPath)
	if err := cli.WritePolicy(ctx, policyName, policyHCL); err != nil {
		return r.transientKVI(ctx, &cr, "write policy", err)
	}

	// 8. Enable approle auth (idempotent — once per Backend).
	if err := cli.EnableApproleAuth(ctx); err != nil {
		return r.transientKVI(ctx, &cr, "enable approle", err)
	}

	// 9. Create approle role (idempotent — re-create doesn't rotate role_id).
	if err := cli.WriteAppRoleRole(ctx, roleName, openbao.AppRoleParams{
		TokenPolicies: []string{policyName},
		TokenTTL:      approleTokenTTL,
		TokenMaxTTL:   approleTokenMaxTTL,
	}); err != nil {
		return r.transientKVI(ctx, &cr, "write approle role", err)
	}

	// 10. Read role_id (stable).
	roleID, err := cli.ReadRoleID(ctx, roleName)
	if err != nil {
		return r.transientKVI(ctx, &cr, "read role_id", err)
	}

	// 11. Mint secret_id — ONLY if the credentials Secret doesn't already
	// exist. Re-generating would invalidate the user's stored credentials.
	credSecretName := credSecretName(&cr)
	var existing corev1.Secret
	credExists := false
	if err := r.Get(ctx, client.ObjectKey{Namespace: cr.Namespace, Name: credSecretName}, &existing); err == nil {
		credExists = true
	} else if !apierrors.IsNotFound(err) {
		return r.transientKVI(ctx, &cr, "check creds secret", err)
	}

	if !credExists {
		secretID, err := cli.GenerateSecretID(ctx, roleName)
		if err != nil {
			return r.transientKVI(ctx, &cr, "generate secret_id", err)
		}
		credSecret := buildCredSecret(&cr, &backend, mountPath, roleID, secretID, credSecretName)
		if err := controllerutil.SetControllerReference(&cr, credSecret, r.Scheme); err != nil {
			// Programmer error: Scheme isn't registered. Failed-state
			// surfaces the bug clearly.
			return r.failKVI(ctx, &cr, fmt.Errorf("set owner on creds secret: %w", err))
		}
		if err := r.Create(ctx, credSecret); err != nil {
			return r.transientKVI(ctx, &cr, "write creds secret", err)
		}
	}

	// 12. Set status Ready.
	if err := r.updateKVIStatus(ctx, req.NamespacedName, func(s *keyvaultv1alpha1.KeyVaultInstanceStatus) {
		s.Phase = phaseReady
		s.Message = fmt.Sprintf("KV-v2 mount at %s; AppRole %s", mountPath, roleName)
		s.MountPath = mountPath
		s.Endpoint = &keyvaultv1alpha1.KeyVaultInstanceEndpoint{
			Address:   backend.Status.Endpoint.Address,
			Port:      backend.Status.Endpoint.Port,
			SecretRef: &corev1.LocalObjectReference{Name: credSecretName},
		}
		s.Resources = []keyvaultv1alpha1.ResourceRef{
			{
				APIVersion: "v1",
				Kind:       "Secret",
				Namespace:  cr.Namespace,
				Name:       credSecretName,
			},
		}
		setKVIReadyCondition(s, metav1.ConditionTrue, "Ready", "AppRole provisioned; mount ready")
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("update KVI status: %w", err)
	}

	// Steady-state: re-reconcile every 5 min to surface drift (the AppRole
	// being deleted out-of-band, mount being disabled, etc.).
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// handleDelete runs the KVI finalizer: connect to the Backend's leader,
// remove this vault's AppRole + policy + mount, then remove the
// finalizer so k8s can GC the CR. The owned credentials Secret is
// cascade-deleted via its OwnerReference.
//
// Hard-delete only in this chunk. Soft-delete (delete creds Secret now,
// requeue for softDeleteDays, THEN unmount) is step 8.
//
// If the Backend has been deleted (or never existed), there's nothing
// to clean up upstream — proceed to finalizer removal so the user can
// recover from a broken Backend.
func (r *KeyVaultInstanceReconciler) handleDelete(ctx context.Context, cr *keyvaultv1alpha1.KeyVaultInstance, log logr.Logger) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(cr, kviFinalizer) {
		// Nothing to do — finalizer already removed in a prior pass.
		return ctrl.Result{}, nil
	}

	_ = r.updateKVIStatus(ctx, client.ObjectKey{Namespace: cr.Namespace, Name: cr.Name},
		func(s *keyvaultv1alpha1.KeyVaultInstanceStatus) {
			s.Phase = phaseTerminating
			s.Message = "deleting AppRole, policy, and mount from Backend"
			setKVIReadyCondition(s, metav1.ConditionFalse, "Terminating", "cleanup in progress")
		})

	// Best-effort cleanup against the Backend. If anything below fails
	// transiently, surface as an error so we requeue. If the Backend is
	// gone / unreachable, fall through to finalizer removal so we don't
	// trap the CR forever — the cluster operator already needs to
	// reconcile the lost Backend.
	if err := r.cleanupOnBackend(ctx, cr, log); err != nil {
		// Distinguish "Backend gone" (clean up our finalizer and exit)
		// from "transient error" (requeue).
		if apierrors.IsNotFound(err) {
			log.Info("Backend gone; skipping upstream cleanup", "backend", cr.Spec.BackendRef.Name)
		} else {
			return ctrl.Result{RequeueAfter: 15 * time.Second}, err
		}
	}

	controllerutil.RemoveFinalizer(cr, kviFinalizer)
	if err := r.Update(ctx, cr); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// cleanupOnBackend deletes the AppRole role, policy, and KV-v2 mount
// owned by this KVI from the Backend's running OpenBao. Each step is
// idempotent on the OpenBao side (404s treated as success).
//
// Returns the NotFound error from r.Get if the Backend is gone, so the
// caller can decide whether to remove the finalizer anyway.
func (r *KeyVaultInstanceReconciler) cleanupOnBackend(ctx context.Context, cr *keyvaultv1alpha1.KeyVaultInstance, log logr.Logger) error {
	var backend keyvaultv1alpha1.KeyVaultBackend
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: cr.Spec.BackendRef.Namespace,
		Name:      cr.Spec.BackendRef.Name,
	}, &backend); err != nil {
		return err
	}
	if backend.Status.KeyMaterialRef == nil {
		// Backend never initialised — nothing to clean upstream.
		return nil
	}

	km, err := openbao.LoadKeyMaterialSecret(ctx, r.Client, backend.Namespace, backend.Name)
	if err != nil {
		if errors.Is(err, openbao.ErrKeyMaterialMissing) {
			// Keys gone — Backend already torn down past unseal. Nothing
			// to clean upstream.
			return nil
		}
		return fmt.Errorf("load Backend key material: %w", err)
	}

	leaderPod, err := r.findLeaderPod(ctx, &backend)
	if err != nil {
		// Backend pods may be down — caller will requeue.
		return fmt.Errorf("find leader: %w", err)
	}

	cli, err := openbao.NewClient(r.RESTConfig, backend.Namespace, leaderPod, openbaoAPIPort)
	if err != nil {
		return fmt.Errorf("build openbao client: %w", err)
	}
	cli.SetToken(km.RootToken)

	ruuid := cr.Labels[labelResourceUUID]
	tuuid := cr.Labels[labelTenantUUID]
	if ruuid == "" || tuuid == "" {
		log.Info("KVI missing required labels; skipping upstream cleanup", "kvi", cr.Name)
		return nil
	}
	mountPath := fmt.Sprintf("tenants/%s/%s", tuuid, ruuid)
	roleName := "kv-" + ruuid
	policyName := "kv-" + ruuid

	log.Info("cleaning up KVI upstream", "mount", mountPath, "role", roleName)
	if err := cli.DeleteAppRoleRole(ctx, roleName); err != nil {
		return fmt.Errorf("delete approle role: %w", err)
	}
	if err := cli.DeletePolicy(ctx, policyName); err != nil {
		return fmt.Errorf("delete policy: %w", err)
	}
	if err := cli.DisableMount(ctx, mountPath); err != nil {
		return fmt.Errorf("disable mount: %w", err)
	}
	return nil
}

// findLeaderPod queries each Backend pod's /v1/sys/leader and returns
// the name of the pod whose response has IsSelf=true.
func (r *KeyVaultInstanceReconciler) findLeaderPod(ctx context.Context, backend *keyvaultv1alpha1.KeyVaultBackend) (string, error) {
	replicas := defaultIfZero(backend.Spec.EngineConfig.HAReplicas, 3)
	for i := 0; i < replicas; i++ {
		podName := fmt.Sprintf("%s-%d", render.StatefulSetName(backend), i)
		cli, err := openbao.NewClient(r.RESTConfig, backend.Namespace, podName, openbaoAPIPort)
		if err != nil {
			continue
		}
		leader, err := cli.Leader(ctx)
		if err != nil {
			// Pod may be transient-down or sealed; try the next one.
			continue
		}
		if leader.IsSelf {
			return podName, nil
		}
	}
	return "", fmt.Errorf("no Raft leader found among %d replicas", replicas)
}

// credSecretName derives the credentials Secret name from the KVI's
// resource UUID (8-char suffix). Mirrors the contract's
// "<service>-<8-char-uuid>-creds" convention.
func credSecretName(cr *keyvaultv1alpha1.KeyVaultInstance) string {
	ruuid := cr.Labels[labelResourceUUID]
	suffix := ruuid
	if dash := strings.IndexByte(suffix, '-'); dash != -1 {
		suffix = suffix[:dash]
	}
	if suffix == "" {
		// Fallback to the CR name — keeps the Secret unique even without
		// the conventional label.
		suffix = cr.Name
	}
	return "keyvault-" + suffix + "-creds"
}

// buildCredSecret renders the credentials Secret. Labels propagate from
// the CR — the contract requires every dc-api.wso2.com/* key to land on
// every child resource.
func buildCredSecret(cr *keyvaultv1alpha1.KeyVaultInstance, backend *keyvaultv1alpha1.KeyVaultBackend, mountPath, roleID, secretID, name string) *corev1.Secret {
	labels := map[string]string{}
	for k, v := range cr.Labels {
		if strings.HasPrefix(k, "dc-api.wso2.com/") {
			labels[k] = v
		}
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"role_id":         []byte(roleID),
			"secret_id":       []byte(secretID),
			"mount_path":      []byte(mountPath),
			"backend_address": []byte(backend.Status.Endpoint.Address),
			"backend_port":    []byte(fmt.Sprintf("%d", backend.Status.Endpoint.Port)),
		},
	}
}

// requeueKVI is the KVI analogue of requeueWithStatus.
func (r *KeyVaultInstanceReconciler) requeueKVI(ctx context.Context, cr *keyvaultv1alpha1.KeyVaultInstance, message string, after time.Duration) (ctrl.Result, error) {
	key := client.ObjectKey{Namespace: cr.Namespace, Name: cr.Name}
	err := r.updateKVIStatus(ctx, key, func(s *keyvaultv1alpha1.KeyVaultInstanceStatus) {
		s.Phase = phaseProvisioning
		s.Message = message
		setKVIReadyCondition(s, metav1.ConditionFalse, "Provisioning", message)
	})
	return ctrl.Result{RequeueAfter: after}, err
}

func (r *KeyVaultInstanceReconciler) failKVI(ctx context.Context, cr *keyvaultv1alpha1.KeyVaultInstance, err error) (ctrl.Result, error) {
	key := client.ObjectKey{Namespace: cr.Namespace, Name: cr.Name}
	if updErr := r.updateKVIStatus(ctx, key, func(s *keyvaultv1alpha1.KeyVaultInstanceStatus) {
		s.Phase = phaseFailed
		s.Message = err.Error()
		setKVIReadyCondition(s, metav1.ConditionFalse, "Error", err.Error())
	}); updErr != nil {
		logf.FromContext(ctx).Error(updErr, "failed to update KVI status after reconcile error")
	}
	return ctrl.Result{}, err
}

// transientKVI is the KVI analogue of KeyVaultBackendReconciler.transient.
// Sets Phase=Provisioning with the formatted error message in status,
// schedules a 15s requeue, returns nil error so controller-runtime skips
// its exponential backoff. Logs each invocation so operators tailing the
// log see retry storms even though the CR status stays in Provisioning.
//
// Use for errors expected to clear up on their own (k8s API hiccups,
// OpenBao not-ready, RBAC propagation, kv-v2 lazy-init races). Reserve
// failKVI (Phase=Failed) for genuinely-terminal CR-spec errors.
func (r *KeyVaultInstanceReconciler) transientKVI(ctx context.Context, cr *keyvaultv1alpha1.KeyVaultInstance, step string, err error) (ctrl.Result, error) {
	logf.FromContext(ctx).Info("transient KVI reconcile error; will requeue",
		"step", step, "error", err.Error(),
		"requeue_after", "15s")
	return r.requeueKVI(ctx, cr, fmt.Sprintf("%s: %v", step, err), 15*time.Second)
}

// updateKVIStatus — same fetch-mutate-update-retry pattern as the
// Backend reconciler. Single-attempt conflict retry is enough because we
// only race against ourselves.
func (r *KeyVaultInstanceReconciler) updateKVIStatus(ctx context.Context, key client.ObjectKey, mutate func(*keyvaultv1alpha1.KeyVaultInstanceStatus)) error {
	for attempt := 0; attempt < 2; attempt++ {
		var fresh keyvaultv1alpha1.KeyVaultInstance
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
	return errors.New("KVI status update: too many conflicts")
}

func setKVIReadyCondition(s *keyvaultv1alpha1.KeyVaultInstanceStatus, status metav1.ConditionStatus, reason, message string) {
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

// SetupWithManager sets up the controller with the Manager.
func (r *KeyVaultInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.RESTConfig == nil {
		r.RESTConfig = mgr.GetConfig()
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&keyvaultv1alpha1.KeyVaultInstance{}).
		Owns(&corev1.Secret{}).
		Named("keyvaultinstance").
		Complete(r)
}
