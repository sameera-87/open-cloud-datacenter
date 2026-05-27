package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	keyvaultv1alpha1 "github.com/wso2/keyvault-operator/api/v1alpha1"
)

var _ = Describe("KeyVaultInstance Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}
		kvi := &keyvaultv1alpha1.KeyVaultInstance{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind KeyVaultInstance")
			err := k8sClient.Get(ctx, typeNamespacedName, kvi)
			if err != nil && errors.IsNotFound(err) {
				resource := &keyvaultv1alpha1.KeyVaultInstance{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: keyvaultv1alpha1.KeyVaultInstanceSpec{
						BackendRef: keyvaultv1alpha1.BackendReference{
							Name:      "kvb-stub",
							Namespace: "dc-tenant-stub",
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &keyvaultv1alpha1.KeyVaultInstance{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance KeyVaultInstance")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &KeyVaultInstanceReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
