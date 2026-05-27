package openbao

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// SecretKeyRootToken — base64 encoded by default Secret semantics; value
	// is the bao root_token literal.
	SecretKeyRootToken = "root_token"
	// SecretKeyUnsealKeys — newline-separated list of base64 unseal keys.
	SecretKeyUnsealKeys = "unseal_keys"
)

// KeyMaterial holds the post-init secrets for one OpenBao cluster.
type KeyMaterial struct {
	RootToken  string
	UnsealKeys []string
}

// EncodeUnsealKeys joins the keys with newlines for storage in the Secret.
func EncodeUnsealKeys(keys []string) []byte {
	return []byte(strings.Join(keys, "\n"))
}

// DecodeUnsealKeys splits a previously-EncodeUnsealKeys-formatted Secret value.
func DecodeUnsealKeys(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	raw := strings.Split(string(b), "\n")
	out := make([]string, 0, len(raw))
	for _, k := range raw {
		k = strings.TrimSpace(k)
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}

// KeyMaterialSecretName derives the Secret name from the Backend CR's name.
// "<cr-name>-keys" — same convention as the SA/Role naming.
func KeyMaterialSecretName(backendName string) string {
	return backendName + "-keys"
}

// CreateKeyMaterialSecret writes the post-init keys into a Secret in the
// same namespace as the Backend CR. owner is the Backend object so the
// Secret is garbage-collected when the Backend is deleted (which we'll wire
// once the finalizer step lands; for now ownerReferences make k8s GC the
// Secret on CR-delete).
//
// Returns an *apierrors.StatusError with reason AlreadyExists if the Secret
// already exists; callers should check IsAlreadyExists and treat as success
// (we never want to overwrite key material).
func CreateKeyMaterialSecret(ctx context.Context, c client.Client, backend metav1.Object, gvk metav1.OwnerReference, km KeyMaterial, labels map[string]string) error {
	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            KeyMaterialSecretName(backend.GetName()),
			Namespace:       backend.GetNamespace(),
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{gvk},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			SecretKeyRootToken:  []byte(km.RootToken),
			SecretKeyUnsealKeys: EncodeUnsealKeys(km.UnsealKeys),
		},
	}
	return c.Create(ctx, sec)
}

// LoadKeyMaterialSecret fetches the post-init keys for a Backend.
// Returns ErrKeyMaterialMissing wrapped with the cause if the Secret is
// not present.
func LoadKeyMaterialSecret(ctx context.Context, c client.Client, namespace, backendName string) (KeyMaterial, error) {
	var sec corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: KeyMaterialSecretName(backendName)}, &sec); err != nil {
		if apierrors.IsNotFound(err) {
			return KeyMaterial{}, fmt.Errorf("%w: secret %s/%s", ErrKeyMaterialMissing, namespace, KeyMaterialSecretName(backendName))
		}
		return KeyMaterial{}, err
	}
	return KeyMaterial{
		RootToken:  string(sec.Data[SecretKeyRootToken]),
		UnsealKeys: DecodeUnsealKeys(sec.Data[SecretKeyUnsealKeys]),
	}, nil
}

// ErrKeyMaterialMissing — sentinel returned when the post-init Secret is
// not yet on the cluster (typically because init has not run yet).
var ErrKeyMaterialMissing = fmt.Errorf("key material secret not found")

// ── dc-api scoped token Secret ───────────────────────────────────────────────
//
// Stored in a SEPARATE Secret from the root-token / unseal-keys material so
// that:
//   - RBAC on the two Secrets can diverge (operator reads both; dc-api reads
//     only the scoped-token Secret).
//   - Deleting the scoped-token Secret to force a re-mint never accidentally
//     wipes the root token + unseal keys (which would brick the Backend).

const (
	// SecretKeyDCAPIToken is the field name inside the dc-api-token Secret.
	SecretKeyDCAPIToken = "token"
)

// DCAPITokenSecretName derives the Secret name for the scoped dc-api token.
// "<cr-name>-dcapi-token" — keeps the kvb-<slug>-* prefix convention.
func DCAPITokenSecretName(backendName string) string {
	return backendName + "-dcapi-token"
}

// CreateDCAPITokenSecret writes the scoped token into a per-Backend Secret.
// Returns an *apierrors.StatusError with reason AlreadyExists if the Secret
// already exists; callers should check IsAlreadyExists and treat as success
// (the existing token is still valid; no need to mint a new one).
func CreateDCAPITokenSecret(ctx context.Context, c client.Client, backend metav1.Object, owner metav1.OwnerReference, token string, labels map[string]string) error {
	sec := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            DCAPITokenSecretName(backend.GetName()),
			Namespace:       backend.GetNamespace(),
			Labels:          labels,
			OwnerReferences: []metav1.OwnerReference{owner},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			SecretKeyDCAPIToken: []byte(token),
		},
	}
	return c.Create(ctx, sec)
}

// DCAPITokenSecretExists returns true when the per-Backend Secret is already
// present. Used as the idempotency guard on token-mint (we mint once and
// keep the token; subsequent reconciles skip the mint).
func DCAPITokenSecretExists(ctx context.Context, c client.Client, namespace, backendName string) (bool, error) {
	var sec corev1.Secret
	err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: DCAPITokenSecretName(backendName)}, &sec)
	if err == nil {
		return true, nil
	}
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return false, err
}
