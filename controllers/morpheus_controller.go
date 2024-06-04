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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
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
	morpheusSA := &corev1.ServiceAccount{}
	if strings.TrimSpace(morpheus.Spec.ServiceAccountName) == "" {
		morpheus.Spec.ServiceAccountName = "morpheus-sa"
	}
	// checks if service account for morpheus exists
	err = r.Get(ctx, types.NamespacedName{Name: morpheus.Spec.ServiceAccountName, Namespace: morpheus.Namespace}, morpheusSA)
	if err != nil && errors.IsNotFound(err) {
		// Define and create a new Service Account.
		var saMorpheus *corev1.ServiceAccount
		saMorpheus = r.morpheusServiceAccount(morpheus)
		log.Info("Creating a new Service Account", "ServiceAccount.Namespace", saMorpheus.Namespace, "ServiceAccount.Name", saMorpheus.Name)
		err = r.Create(ctx, saMorpheus)
		if err != nil {
			log.Error(err, "Failed to create new Service Account", "Deployment.Namespace", saMorpheus.Namespace, "Deployment.Name", saMorpheus.Name)
			return ctrl.Result{}, err
		}
	} else if err != nil {
		log.Error(err, "Failed to get Service Account")
		return ctrl.Result{}, err
	}
	autoBindSccToSa := &morpheus.Spec.AutoBindSccToSa
	// default to add anyuid scc (Security Context Constraint) to service account
	if autoBindSccToSa == nil {
		morpheus.Spec.AutoBindSccToSa = true
	}
	if morpheus.Spec.AutoBindSccToSa {
		// Checks Whether Morpheus Role exists.
		morpheusAnyUidRole := &rbacv1.Role{}
		err = r.Get(ctx, types.NamespacedName{Name: morpheus.Name, Namespace: morpheus.Namespace}, morpheusAnyUidRole)
		if err != nil && errors.IsNotFound(err) {
			// Define a new Role
			var roleMorpheus *rbacv1.Role
			roleMorpheus = r.createAnyUidRole(morpheus)
			log.Info("Creating a new Role", "Role.Namespace", roleMorpheus.Namespace, "Role.Name", roleMorpheus.Name)
			err = r.Create(ctx, roleMorpheus)
			if err != nil {
				log.Error(err, "Failed to create new anyuid Role", "Role.Namespace", roleMorpheus.Namespace, "Role.Name", roleMorpheus.Name)
				return ctrl.Result{}, err
			}
		} else if err != nil {
			log.Error(err, "Failed to get Role")
			return ctrl.Result{}, err
		}

		// Checks Whether Morpheus RoleBinding exists.
		morpheusAnyUidRoleBinding := &rbacv1.RoleBinding{}
		err = r.Get(ctx, types.NamespacedName{Name: morpheus.Name, Namespace: morpheus.Namespace}, morpheusAnyUidRoleBinding)
		if err != nil && errors.IsNotFound(err) {
			// Define a new RoleBinding to authorize service account to run deployments as any userid.
			var roleBindingMorpheus *rbacv1.RoleBinding
			roleBindingMorpheus = r.createAnyUidRoleBinding(morpheus)
			log.Info("Creating a new Role", "RoleBinding.Namespace", roleBindingMorpheus.Namespace, "RoleBinding.Name", roleBindingMorpheus.Name)
			err = r.Create(ctx, roleBindingMorpheus)
			if err != nil {
				log.Error(err, "Failed to create new anyuid RoleBinding", "RoleBinding.Namespace", roleBindingMorpheus.Namespace, "RoleBinding.Name", roleBindingMorpheus.Name)
				return ctrl.Result{}, err
			}
		} else if err != nil {
			log.Error(err, "Failed to get Role")
			return ctrl.Result{}, err
		}
	}
	// Checks Whether Morpheus deployment exists.
	morpheusDeployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: morpheus.Name, Namespace: morpheus.Namespace}, morpheusDeployment)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		deploymentMorpheus := r.createMorpheusDeployment(morpheus)
		log.Info("Creating a new Deployment", "Deployment.Namespace", deploymentMorpheus.Namespace, "Deployment.Name", deploymentMorpheus.Name)
		err = r.Create(ctx, deploymentMorpheus)
		if err != nil {
			log.Error(err, "Failed to create new Morpheus Deployment", "Deployment.Namespace", deploymentMorpheus.Namespace, "Deployment.Name", deploymentMorpheus.Name)
			return ctrl.Result{}, err
		}
	} else if err != nil {
		log.Error(err, "Failed to get Morpheus Deployment")
		return ctrl.Result{}, err
	}

	err = deployTritonServer(r, ctx, morpheus, log)
	err = deployMilvusDB(r, ctx, morpheus, log)

	return ctrl.Result{}, nil
}

func deployMilvusDB(r *MorpheusReconciler, ctx context.Context, morpheus *aiv1alpha1.Morpheus, log logr.Logger) error {

	err := deployMinio(r, ctx, morpheus, log)
	err := deployEtcd(r, ctx, morpheus, log)

	//Deploy Milvus Vector DB

	return nil
}

func deployEtcd(r *MorpheusReconciler, ctx context.Context, morpheus *aiv1alpha1.Morpheus, log logr.Logger) error {
	// Create a Persistent volume claim to store etcd data for milvus db.

	morpheusRepoPvc := &corev1.PersistentVolumeClaim{}
	const milvusEctdPvcName = "milvus-etcd-data"
	const milvusEctdName = "milvus-etcd"
	err := r.Get(ctx, types.NamespacedName{Name: milvusEctdPvcName, Namespace: morpheus.Namespace}, morpheusRepoPvc)

	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		var pvcEtcd *corev1.PersistentVolumeClaim
		pvcEtcd = r.createPvc(morpheus, milvusEctdPvcName, "2Gi")
		log.Info("Creating a new Pvc for Etcd Data", "Pvc.Namespace", pvcEtcd.Namespace, "Pvc.Name", pvcEtcd.Name)
		err = r.Create(ctx, pvcEtcd)
		if err != nil {
			log.Error(err, "Failed to create Pvc for Triton Server", "Pvc.Namespace", pvcEtcd.Namespace, "Pvc.Name", pvcEtcd.Name+"-repo")
			return err
		}
	} else if err != nil {
		log.Error(err, "Failed to get milvus etcd data PVC ")
		return err
	}

	// Create an Etcd Service
	etcdService := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: milvusEctdName, Namespace: morpheus.Namespace}, etcdService)

	if err != nil && errors.IsNotFound(err) {
		// Define a new Service
		var serviceEtcd *corev1.Service
		ports := []corev1.ServicePort{{
			Name:     "grpc",
			Protocol: corev1.ProtocolTCP,
			Port:     8001,
			TargetPort: intstr.IntOrString{
				StrVal: "8001",
			},
		},
		}
		selector := map[string]string{
			"app":       "milvus",
			"component": "etcd",
		}
		serviceEtcd = r.createService(morpheus, ports, milvusEctdName, selector)
		log.Info("Creating a new Triton Service", "Service.Namespace", serviceEtcd.Namespace, "Service.Name", serviceEtcd.Name)
		err = r.Create(ctx, serviceEtcd)
		if err != nil {
			log.Error(err, "Failed to create new Etcd Service", "Service.Namespace", serviceEtcd.Namespace, "Service.Name", serviceEtcd.Name)
			return err
		}
	} else if err != nil {
		log.Error(err, "Failed to get Etcd Service")
		return err
	}

	return nil
}

func deployMinio(r *MorpheusReconciler, ctx context.Context, morpheus *aiv1alpha1.Morpheus, log logr.Logger) error {

	return nil
}

func deployTritonServer(r *MorpheusReconciler, ctx context.Context, morpheus *aiv1alpha1.Morpheus, log logr.Logger) error {

	// Create a Persistent volume claim to store morpheus repo content, that will be mounted into triton server' container
	morpheusRepoPvc := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Name: morpheus.Name + "-repo", Namespace: morpheus.Namespace}, morpheusRepoPvc)

	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		var pvcMorpheus *corev1.PersistentVolumeClaim
		pvcMorpheus = r.createPvc(morpheus, "morpheus-repo", "20Gi")
		log.Info("Creating a new Pvc for Triton Server", "Pvc.Namespace", pvcMorpheus.Namespace, "Pvc.Name", pvcMorpheus.Name)
		err = r.Create(ctx, pvcMorpheus)
		if err != nil {
			log.Error(err, "Failed to create Pvc for Triton Server", "Pvc.Namespace", pvcMorpheus.Namespace, "Pvc.Name", pvcMorpheus.Name+"-repo")
			return err
		}
	} else if err != nil {
		log.Error(err, "Failed to get Morpheus repository PVC ")
		return err
	}

	// Create a Triton Inference Server Deployment
	tritonDeployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: "triton-server", Namespace: morpheus.Namespace}, tritonDeployment)

	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		var deploymentTriton *appsv1.Deployment
		deploymentTriton = r.createTritonDeployment(morpheus)
		log.Info("Creating a new Deployment", "Deployment.Namespace", deploymentTriton.Namespace, "Deployment.Name", deploymentTriton.Name)
		err = r.Create(ctx, deploymentTriton)
		if err != nil {
			log.Error(err, "Failed to create new Triton Server Deployment", "Deployment.Namespace", deploymentTriton.Namespace, "Deployment.Name", deploymentTriton.Name)
			return err
		}
	} else if err != nil {
		log.Error(err, "Failed to get Triton Server Deployment")
		return err
	}

	// Create a Triton Inference Server Service
	tritonService := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: "triton-server", Namespace: morpheus.Namespace}, tritonService)

	if err != nil && errors.IsNotFound(err) {
		// Define a new Service
		var serviceTriton *corev1.Service
		ports := []corev1.ServicePort{{
			Name:     "grpc",
			Protocol: corev1.ProtocolTCP,
			Port:     8001,
			TargetPort: intstr.IntOrString{
				StrVal: "8001",
			},
		},
			{
				Name:     "http",
				Protocol: corev1.ProtocolTCP,
				Port:     8000,
				TargetPort: intstr.IntOrString{
					StrVal: "8000",
				},
			},
			{
				Name:     "metrics",
				Protocol: corev1.ProtocolTCP,
				Port:     8002,
				TargetPort: intstr.IntOrString{
					StrVal: "8002",
				},
			},
		}
		selector := map[string]string{
			"app": "triton-server",
		}
		serviceTriton = r.createService(morpheus, ports, selector["app"], selector)
		log.Info("Creating a new Triton Service", "Service.Namespace", serviceTriton.Namespace, "Service.Name", serviceTriton.Name)
		err = r.Create(ctx, serviceTriton)
		if err != nil {
			log.Error(err, "Failed to create new Triton Server Service", "Service.Namespace", serviceTriton.Namespace, "Service.Name", serviceTriton.Name)
			return err
		}
	} else if err != nil {
		log.Error(err, "Failed to get Triton Server Service")
		return err
	}

	return nil

}

// deploymentForMemcached returns a memcached Deployment object
func (r *MorpheusReconciler) createMorpheusDeployment(m *aiv1alpha1.Morpheus) *appsv1.Deployment {
	labels := labelsForComponent("morpheus", "v24.03.02")
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
					ServiceAccountName: m.Spec.ServiceAccountName,
				},
			},
		},
	}

	// Set Morpheus instance as the owner and controller
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

func (r *MorpheusReconciler) morpheusServiceAccount(morpheus *aiv1alpha1.Morpheus) *corev1.ServiceAccount {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      morpheus.Spec.ServiceAccountName,
			Namespace: morpheus.Namespace,
		},
	}
	ctrl.SetControllerReference(morpheus, sa, r.Scheme)
	return sa
}

func (r *MorpheusReconciler) createAnyUidRole(morpheus *aiv1alpha1.Morpheus) *rbacv1.Role {
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      morpheus.Name,
			Namespace: morpheus.Namespace,
		},
		Rules: []rbacv1.PolicyRule{{
			Verbs:     []string{"use"},
			APIGroups: []string{"security.openshift.io"},
			Resources: []string{"anyuid"},
		},
		},
	}
	ctrl.SetControllerReference(morpheus, role, r.Scheme)
	return role
}

func (r *MorpheusReconciler) createAnyUidRoleBinding(morpheus *aiv1alpha1.Morpheus) *rbacv1.RoleBinding {
	roleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      morpheus.Name,
			Namespace: morpheus.Namespace,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      morpheus.Spec.ServiceAccountName,
			Namespace: morpheus.Namespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     morpheus.Name,
		},
	}
	ctrl.SetControllerReference(morpheus, roleBinding, r.Scheme)
	return roleBinding
}

func (r *MorpheusReconciler) createPvc(morpheus *aiv1alpha1.Morpheus, pvcName string, pvcSize string) *corev1.PersistentVolumeClaim {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: morpheus.Namespace,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(pvcSize),
				},
			},
		},
	}
	ctrl.SetControllerReference(morpheus, pvc, r.Scheme)
	return pvc
}

func (r *MorpheusReconciler) createTritonDeployment(morpheus *aiv1alpha1.Morpheus) *appsv1.Deployment {
	labels := labelsForComponent("triton-server", "v23.06")
	var numOfReplicas int32 = 1

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      morpheus.Name,
			Namespace: morpheus.Namespace,
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
						Image: "nvcr.io/nvidia/tritonserver:23.06-py3",
						Name:  "triton",
						Command: []string{"tritonserver", "--model-repository=/repo/Morpheus/models/triton-model-repo",
							"--exit-on-error=false", "--strict-readiness=false",
							"--disable-auto-complete-config", "--log-info=true"},
					}},
				},
			},
		},
	}

	// Set Morpheus instance as the owner and controller
	ctrl.SetControllerReference(morpheus, dep, r.Scheme)
	return dep
}

func (r *MorpheusReconciler) createService(morpheus *aiv1alpha1.Morpheus, ports []corev1.ServicePort, svcName string, selector map[string]string) *corev1.Service {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: morpheus.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Ports:    ports,
			Selector: selector,
		},
	}
	ctrl.SetControllerReference(morpheus, svc, r.Scheme)
	return svc
}
