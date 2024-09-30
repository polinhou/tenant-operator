/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	tenantv1 "github.com/polinhou/tenant-operator/api/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TenantReconciler reconciles a Tenant object
type TenantReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=tenant.polinhou.dev,resources=tenants,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=tenant.polinhou.dev,resources=tenants/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=tenant.polinhou.dev,resources=tenants/finalizers,verbs=update
//+kubebuilder:rbac:groups=core,resources=namespaces,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=resourcequotas,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Tenant object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.17.3/pkg/reconcile
func (r *TenantReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the Tenant instance
	tenant := &tenantv1.Tenant{}
	err := r.Get(ctx, req.NamespacedName, tenant)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("Tenant resource not found. Ignoring since object must be deleted")
			return r.cleanupNamespace(ctx, req.Name)
		}
		log.Error(err, "Failed to get Tenant")
		return ctrl.Result{}, err
	}

	// Create or update the namespace
	err = r.reconcileNamespace(ctx, tenant)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Only reconcile ResourceQuota if managed is true
	if tenant.Spec.Managed {
		err = r.reconcileResourceQuota(ctx, tenant)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{RequeueAfter: time.Second * 30}, nil
}

func (r *TenantReconciler) reconcileNamespace(ctx context.Context, tenant *tenantv1.Tenant) error {
	log := log.FromContext(ctx)
	namespace := &corev1.Namespace{}
	namespace.Name = tenant.Spec.Namespace

	_, err := ctrl.CreateOrUpdate(ctx, r.Client, namespace, func() error {
		if namespace.Labels == nil {
			namespace.Labels = make(map[string]string)
		}
		namespace.Labels["tenant.polinhou.dev/owned-by"] = tenant.Name
		namespace.Labels["tenant.polinhou.dev/managed"] = fmt.Sprintf("%v", tenant.Spec.Managed)
		return nil
	})

	if err != nil {
		log.Error(err, "Failed to create or update Namespace", "Namespace", namespace.Name)
		return err
	}

	log.Info("Reconciled Namespace", "Namespace", namespace.Name)
	return nil
}

func (r *TenantReconciler) reconcileResourceQuota(ctx context.Context, tenant *tenantv1.Tenant) error {
	log := log.FromContext(ctx)
	resourceQuota := &corev1.ResourceQuota{}
	resourceQuota.Name = fmt.Sprintf("%s-quota", tenant.Spec.Namespace)
	resourceQuota.Namespace = tenant.Spec.Namespace

	err := r.Get(ctx, client.ObjectKey{Namespace: tenant.Spec.Namespace, Name: resourceQuota.Name}, resourceQuota)
	if err != nil && !errors.IsNotFound(err) {
		log.Error(err, "Failed to get ResourceQuota")
		return err
	}

	if resourceQuota.Labels == nil {
		resourceQuota.Labels = make(map[string]string)
	}
	resourceQuota.Labels["tenant.polinhou.dev/owned-by"] = tenant.Name

	if errors.IsNotFound(err) {
		// ResourceQuota doesn't exist, create it
		resourceQuota.Spec = tenant.Spec.ResourceQuota
		if err := r.Create(ctx, resourceQuota); err != nil {
			log.Error(err, "Failed to create ResourceQuota")
			return err
		}
		log.Info("Created ResourceQuota", "ResourceQuota", resourceQuota.Name)
	} else {
		// ResourceQuota exists, check if it needs updating
		if !reflect.DeepEqual(resourceQuota.Spec.Hard, tenant.Spec.ResourceQuota.Hard) {
			log.Info("ResourceQuota specs do not match. Updating to match Tenant CR",
				"Current", resourceQuota.Spec.Hard,
				"Desired", tenant.Spec.ResourceQuota.Hard)
			resourceQuota.Spec = tenant.Spec.ResourceQuota
			if err := r.Update(ctx, resourceQuota); err != nil {
				log.Error(err, "Failed to update ResourceQuota")
				return err
			}
			log.Info("Updated ResourceQuota", "ResourceQuota", resourceQuota.Name)
		} else {
			log.Info("ResourceQuota is up to date", "ResourceQuota", resourceQuota.Name)
		}
	}

	return nil
}

func (r *TenantReconciler) cleanupNamespace(ctx context.Context, tenantName string) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the Tenant to get the namespace name
	tenant := &tenantv1.Tenant{}
	err := r.Get(ctx, client.ObjectKey{Name: tenantName}, tenant)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "Unable to fetch Tenant for cleanup")
			return ctrl.Result{}, err
		}
		// Tenant not found, which means it's already deleted
		// We need to find the associated namespace by other means
		// For example, you could use a label selector if you've labeled the namespace
		// Or you could use a naming convention
		// For this example, let's assume the namespace name is the same as the tenant name
		namespaceName := tenantName
		log.Info("Tenant not found, using tenant name as namespace name", "name", namespaceName)

		// Delete the associated namespace
		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespaceName,
			},
		}
		err = r.Delete(ctx, namespace)
		if err != nil && !apierrors.IsNotFound(err) {
			log.Error(err, "Unable to delete Namespace")
			return ctrl.Result{}, err
		}

		log.Info("Namespace deleted", "namespace", namespaceName)
		return ctrl.Result{}, nil
	}

	// If we get here, it means the Tenant still exists
	// This shouldn't happen in normal operation, but let's handle it just in case
	log.Info("Tenant still exists, not deleting namespace", "tenant", tenantName)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *TenantReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&tenantv1.Tenant{}).
		Owns(&corev1.Namespace{}).
		Owns(&corev1.ResourceQuota{}).
		Complete(r)
}
