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

package controllers

import (
	"context"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"strings"

	aiv1alpha1 "github.com/zvigrinberg/morpheus-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// MorpheusReconciler reconciles a Morpheus object
type MorpheusReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=ai.redhat.com,resources=morpheuses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ai.redhat.com,resources=morpheuses/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ai.redhat.com,resources=morpheuses/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Morpheus object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.14.1/pkg/reconcile
func (r *MorpheusReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx = context.Background()
	log := r.Log.WithValues("morpheus", req.NamespacedName)
	// Fetch the Memcached instance
	morpheus := &aiv1alpha1.Morpheus{}
	err := r.Get(ctx, req.NamespacedName, morpheus)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("Morpheus resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get Memcached")
		return ctrl.Result{}, err
	}
	morpheusDeployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: morpheus.Name, Namespace: morpheus.Namespace}, morpheusDeployment)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		deploymentMorpheus := r.morpheusDeployment(morpheus)
		log.Info("Creating a new Deployment", "Deployment.Namespace", deploymentMorpheus.Namespace, "Deployment.Name", deploymentMorpheus.Name)
		err = r.Create(ctx, deploymentMorpheus)
		if err != nil {
			log.Error(err, "Failed to create new Deployment", "Deployment.Namespace", deploymentMorpheus.Namespace, "Deployment.Name", deploymentMorpheus.Name)
			return ctrl.Result{}, err
		}
		// Deployment created successfully - return and requeue
		return ctrl.Result{Requeue: true}, nil
	} else if err != nil {
		log.Error(err, "Failed to get Deployment")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// deploymentForMemcached returns a memcached Deployment object
func (r *MorpheusReconciler) morpheusDeployment(m *aiv1alpha1.Morpheus) *appsv1.Deployment {
	labels := labelsForComponent("morpheus", "v24.03.02")
	serviceAccountName := m.Spec.ServiceAccountName
	if strings.TrimSpace(serviceAccountName) == "" {
		serviceAccountName = "morpheus-sa"
	}
	var numOfReplicas int32 = 1
	var user int64 = 0

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &numOfReplicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Image:   "nvcr.io/nvidia/morpheus/morpheus:v24.03.02-runtime",
						Name:    "morpheus",
						Command: []string{"sleep", "infinity"},
						SecurityContext: &corev1.SecurityContext{
							RunAsUser: &user,
						},
					}},
					ServiceAccountName: serviceAccountName,
				},
			},
		},
	}

	// Set Memcached instance as the owner and controller
	ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}

func labelsForComponent(name string, version string) map[string]string {
	return map[string]string{"app": name, "version": version}
}

// SetupWithManager sets up the controller with the Manager.
func (r *MorpheusReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.Morpheus{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Complete(r)
}
