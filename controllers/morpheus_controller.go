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
	"fmt"
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/log"
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
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=ai.redhat.com,resources=morpheus,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ai.redhat.com,resources=morpheus/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ai.redhat.com,resources=morpheus/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=*,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=*,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=*,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,verbs=use

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
	thereWasAnUpdate := false
	ctx = context.Background()
	const minioAdminDefaultUser = "admin"
	const minioAdminDefaultPassword = "admin123!"

	ctx = context.WithValue(ctx, "minioDefaultUser", minioAdminDefaultUser)
	ctx = context.WithValue(ctx, "minioDefaultPassword", minioAdminDefaultPassword)
	log := log.FromContext(ctx)
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
	err = InitializeCrStatusImpl(ctx, morpheus, r)
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
		err2, result := createAnyUidSecurityContextConstraint(ctx, r, morpheus, log)
		if err2 != nil {
			return result, err2
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
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionFalse, "Reconciling:Creation:Error", err.Error())
			return ctrl.Result{}, err
		}
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionTrue, "Reconciling:Create", "Morpheus Deployment successfully created and deployed!")
	} else if err != nil {
		const errorMessage = "Failed to get Morpheus Deployment"
		log.Error(err, errorMessage)
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionFalse, "Reconciling:Deployment:Fetch:Error", errorMessage+"->"+err.Error())
		return ctrl.Result{}, err
	} else {
		serviceAccountDeployment := morpheusDeployment.Spec.Template.Spec.ServiceAccountName
		if serviceAccountDeployment != morpheus.Spec.ServiceAccountName {
			morpheusDeployment.Spec.Template.Spec.ServiceAccountName = morpheus.Spec.ServiceAccountName
			err = r.Update(ctx, morpheusDeployment)
			if err != nil {
				log.Error(err, "Failed to update Morpheus Deployment", "Deployment.Namespace", morpheusDeployment.Namespace, "Deployment.Name", morpheusDeployment.Name)
				UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionFalse, "Reconciling:Update:Error", err.Error())
				return ctrl.Result{}, err
			}
			thereWasAnUpdate = true
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionTrue, "Reconciling:Update", "Morpheus Deployment successfully Updated!")
		}
	}

	thereWasAnUpdate, err = deployTritonServer(r, ctx, morpheus, log)
	if err != nil {
		log.Error(err, "Failed to Deploy Triton Server")
		return ctrl.Result{}, err
	}
	thereWasAnUpdate, err = deployMilvusDB(r, ctx, morpheus, log)
	if err != nil {
		log.Error(err, "Failed to Deploy MilvusDB")
		return ctrl.Result{}, err
	}
	if thereWasAnUpdate {
		return ctrl.Result{Requeue: true}, nil
	} else {
		return ctrl.Result{}, nil
	}
}

func createAnyUidSecurityContextConstraint(ctx context.Context, r *MorpheusReconciler, morpheus *aiv1alpha1.Morpheus, log logr.Logger) (error, ctrl.Result) {
	// Checks Whether Morpheus Role exists.
	morpheusAnyUidRole := &rbacv1.Role{}
	err := r.Get(ctx, types.NamespacedName{Name: morpheus.Name, Namespace: morpheus.Namespace}, morpheusAnyUidRole)
	if err != nil && errors.IsNotFound(err) {
		// Define a new Role
		var roleMorpheus *rbacv1.Role
		roleMorpheus = r.createAnyUidRole(morpheus)
		log.Info("Creating a new Role", "Role.Namespace", roleMorpheus.Namespace, "Role.Name", roleMorpheus.Name)
		err = r.Create(ctx, roleMorpheus)
		if err != nil {
			log.Error(err, "Failed to create new anyuid Role", "Role.Namespace", roleMorpheus.Namespace, "Role.Name", roleMorpheus.Name)
			return nil, ctrl.Result{}
		}
	} else if err != nil {
		log.Error(err, "Failed to get Role")
		return nil, ctrl.Result{}
	}

	// Checks Whether Morpheus RoleBinding exists.
	morpheusAnyUidRoleBinding := &rbacv1.RoleBinding{}
	err = r.Get(ctx, types.NamespacedName{Name: morpheus.Name, Namespace: morpheus.Namespace}, morpheusAnyUidRoleBinding)
	if err != nil && errors.IsNotFound(err) {
		// Define a new RoleBinding to authorize service account to run deployments as any userid.
		var roleBindingMorpheus *rbacv1.RoleBinding
		roleBindingMorpheus = r.createAnyUidRoleBinding(morpheus)
		log.Info("Creating a new Role Binding", "RoleBinding.Namespace", roleBindingMorpheus.Namespace, "RoleBinding.Name", roleBindingMorpheus.Name)
		err = r.Create(ctx, roleBindingMorpheus)
		if err != nil {
			log.Error(err, "Failed to create new anyuid RoleBinding", "RoleBinding.Namespace", roleBindingMorpheus.Namespace, "RoleBinding.Name", roleBindingMorpheus.Name)
			return err, ctrl.Result{}
		}
	} else if err != nil {
		log.Error(err, "Failed to get Role")
		return err, ctrl.Result{}
	}
	return nil, ctrl.Result{}
}

func deployMilvusDB(r *MorpheusReconciler, ctx context.Context, morpheus *aiv1alpha1.Morpheus, log logr.Logger) (bool, error) {

	const milvusEctdPvcName = "milvus-etcd-data"
	const milvusEctdName = "milvus-etcd"
	const milvusMinioPvcName = "milvus-minio-data"
	const milvusMinioName = "milvus-minio"
	const milvusPvcDataName = "milvus-data"
	const minioPort = 9000
	const etcdPort = 2379
	//etFromSpecElseDefault(morpheus.Spec.Milvus.StoragePvcSize, defaultMilvusPvcSize)
	thereWasAnUpdate, err := deployMinio(r, ctx, morpheus, log, milvusMinioPvcName, milvusMinioName, minioPort)
	if err != nil {
		log.Error(err, "Failed to get deploy minio object storage store")
		return thereWasAnUpdate, err
	}
	thereWasAnUpdate, err = deployEtcd(r, ctx, morpheus, log, milvusEctdPvcName, milvusEctdName, etcdPort)
	if err != nil {
		log.Error(err, "Failed to get deploy etcd key-value store")
		return thereWasAnUpdate, err
	}
	//Deploy Milvus Vector DB
	resourceCreated := false
	milvusPvc := &corev1.PersistentVolumeClaim{}
	err = r.Get(ctx, types.NamespacedName{Name: milvusPvcDataName, Namespace: morpheus.Namespace}, milvusPvc)

	const defaultMilvusPvcSize = "2Gi"
	milvusPvcSize := getFromSpecElseDefault(morpheus.Spec.Milvus.StoragePvcSize, defaultMilvusPvcSize)
	if err != nil && errors.IsNotFound(err) {
		// Define a new PVC
		var pvcMilvus *corev1.PersistentVolumeClaim
		pvcMilvus = r.createPvc(morpheus, milvusPvcDataName, milvusPvcSize)
		log.Info("Creating a new Pvc for Milvus Data", "Pvc.Namespace", pvcMilvus.Namespace, "Pvc.Name", pvcMilvus.Name)
		err = r.Create(ctx, pvcMilvus)
		if err != nil {
			const errorMessageBase = "Failed to create Pvc for Milvus"
			log.Error(err, errorMessageBase, "Pvc.Namespace", pvcMilvus.Namespace, "Pvc.Name", pvcMilvus.Name)
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMilvusDB, metav1.ConditionFalse, "Reconciling:Create:Pvc:Error", errorMessageBase)
			return thereWasAnUpdate, err
		} else {
			resourceCreated = true
		}
	} else if err != nil {
		log.Error(err, "Failed to get milvus data PVC ")
		return thereWasAnUpdate, err
	} else {
		// pvc storage size was changed.
		if milvusPvcSize != milvusPvc.Spec.Resources.Requests.Storage().String() {
			milvusPvc.Spec.Resources.Requests["storage"] = resource.MustParse(milvusPvcSize)
			err = r.Update(ctx, milvusPvc)
			if err != nil {
				const baseMessageError = "Failed to update Milvus Pvc"
				log.Error(err, baseMessageError, "Deployment.Namespace", milvusPvc.Namespace, "Deployment.Name", milvusPvc.Name)
				UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMilvusDB, metav1.ConditionFalse, "Reconciling:Update:Pvc:Error", baseMessageError+"->"+err.Error())
				return thereWasAnUpdate, err
			}
			thereWasAnUpdate = true
		}
	}
	// Create an Milvus DB Deployment
	milvusDeployment := &appsv1.Deployment{}
	const milvusStandaloneDeploymentName = "milvus-standalone"
	err = r.Get(ctx, types.NamespacedName{Name: milvusStandaloneDeploymentName, Namespace: morpheus.Namespace}, milvusDeployment)

	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		var deploymentMilvus *appsv1.Deployment
		minioUrl := fmt.Sprintf("%s:%d", milvusMinioName, minioPort)
		etcdUrl := fmt.Sprintf("%s:%d", milvusEctdName, etcdPort)
		deploymentMilvus = r.createMilvusDbDeployment(ctx, morpheus, milvusPvcDataName, minioUrl, etcdUrl)
		log.Info("Creating a new Deployment", "Deployment.Namespace", deploymentMilvus.Namespace, "Deployment.Name", deploymentMilvus.Name)
		err = r.Create(ctx, deploymentMilvus)
		if err != nil {
			const baseErrorMessage = "Failed to create new milvus-standalone Deployment"
			log.Error(err, baseErrorMessage, "Deployment.Namespace", deploymentMilvus.Namespace, "Deployment.Name", deploymentMilvus.Name)
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMilvusDB, metav1.ConditionFalse, "Reconciling:Create:Deployment:Error", baseErrorMessage+" ->"+err.Error())
			return thereWasAnUpdate, err
		} else {
			resourceCreated = true
		}
	} else if err != nil {
		log.Error(err, "Failed to get Milvus Deployment")
		return thereWasAnUpdate, err
	} else {
		// Check if deployment data was changed.
		minioSpecUser := getFromSpecElseDefault(morpheus.Spec.Milvus.Minio.RootUser, ctx.Value("minioDefaultUser").(string))
		minioSpecPassword := getFromSpecElseDefault(morpheus.Spec.Milvus.Minio.RootPassword, ctx.Value("minioDefaultPassword").(string))
		thereWasEnvironmentUpdate := false
		for _, env := range milvusDeployment.Spec.Template.Spec.Containers[0].Env {
			if env.Name == "MINIO_ACCESS_KEY_ID" {
				if env.Value != minioSpecUser {
					env.Value = minioSpecUser
					thereWasEnvironmentUpdate = true
					thereWasAnUpdate = true
				} else if env.Name == "MINIO_SECRET_ACCESS_KEY" {
					if env.Value != minioSpecPassword {
						env.Value = minioSpecPassword
						thereWasEnvironmentUpdate = true
						thereWasAnUpdate = true
					}
				}
			}
		}
		if thereWasEnvironmentUpdate {
			err = r.Update(ctx, milvusDeployment)
			if err != nil {
				const baseMessageError = "Failed to update Milvus Deployment"
				log.Error(err, baseMessageError, "Deployment.Namespace", milvusDeployment.Namespace, "Deployment.Name", milvusDeployment.Name)
				UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMilvusDB, metav1.ConditionFalse, "Reconciling:Update:Deployment:Error", baseMessageError+"->"+err.Error())
				return thereWasAnUpdate, err
			}
			thereWasAnUpdate = true
		}
	}

	// Create A Milvus Service
	milvusService := &corev1.Service{}
	const milvusName = milvusStandaloneDeploymentName
	err = r.Get(ctx, types.NamespacedName{Name: milvusName, Namespace: morpheus.Namespace}, milvusService)

	if err != nil && errors.IsNotFound(err) {
		// Define a new Service
		var serviceMilvus *corev1.Service
		ports := []corev1.ServicePort{
			{
				Name:     "grpc",
				Protocol: corev1.ProtocolTCP,
				Port:     19530,
				TargetPort: intstr.IntOrString{
					Type:   intstr.Int,
					IntVal: 19530,
				},
			},
			{
				Name:     "api",
				Protocol: corev1.ProtocolTCP,
				Port:     9091,
				TargetPort: intstr.IntOrString{
					Type:   intstr.Int,
					IntVal: 9091,
				},
			},
		}
		selector := map[string]string{
			"app":       "milvus",
			"component": "milvus",
		}
		serviceMilvus = r.createService(morpheus, ports, milvusName, selector)
		log.Info("Creating a new Milvus Service", "Service.Namespace", serviceMilvus.Namespace, "Service.Name", serviceMilvus.Name)
		err = r.Create(ctx, serviceMilvus)
		if err != nil {
			log.Error(err, "Failed to create new Milvus Service", "Service.Namespace", serviceMilvus.Namespace, "Service.Name", serviceMilvus.Name)
			return false, err
		} else {
			resourceCreated = true
		}
	} else if err != nil {
		log.Error(err, "Failed to get Milvus Service")
		return false, err
	}
	if resourceCreated {
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMilvusDB, metav1.ConditionTrue, "Reconciling:Create", "MilvusDB Instance successfully Deployed!")
	}
	return thereWasAnUpdate, nil
}

func getFromSpecElseDefault(specValue string, defaultValue string) string {
	if &specValue != nil && strings.TrimSpace(specValue) != "" {
		return specValue
	}
	return defaultValue
}

func deployEtcd(r *MorpheusReconciler, ctx context.Context, morpheus *aiv1alpha1.Morpheus, log logr.Logger, milvusEctdPvcName string, milvusEctdName string, etcdPort int) (bool, error) {

	var thereWasAnUpdate bool = false
	resourceCreated := false
	// Create a Persistent volume claim to store etcd data for milvus db.
	etcdPvc := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Name: milvusEctdPvcName, Namespace: morpheus.Namespace}, etcdPvc)
	const defaultPvcSizeEtcd = "2Gi"
	etcdPvcSize := getFromSpecElseDefault(morpheus.Spec.Milvus.Etcd.StoragePvcSize, defaultPvcSizeEtcd)
	if err != nil && errors.IsNotFound(err) {
		// Define a new Pvc
		var pvcEtcd *corev1.PersistentVolumeClaim
		pvcEtcd = r.createPvc(morpheus, milvusEctdPvcName, etcdPvcSize)
		log.Info("Creating a new Pvc for Etcd Data", "Pvc.Namespace", pvcEtcd.Namespace, "Pvc.Name", pvcEtcd.Name)
		err = r.Create(ctx, pvcEtcd)
		if err != nil {
			const errorMessageBase = "Failed to create Pvc for Etcd"
			log.Error(err, errorMessageBase, "Pvc.Namespace", pvcEtcd.Namespace, "Pvc.Name", pvcEtcd.Name)
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedEtcd, metav1.ConditionFalse, "Reconciling:Create:Pvc:Error", errorMessageBase)
			return thereWasAnUpdate, err
		} else {
			resourceCreated = true
		}
	} else if err != nil {
		log.Error(err, "Failed to get milvus etcd data PVC ")
		return false, err
	} else {
		// pvc storage size was changed.
		if etcdPvcSize != etcdPvc.Spec.Resources.Requests.Storage().String() {
			etcdPvc.Spec.Resources.Requests["storage"] = resource.MustParse(etcdPvcSize)
			err = r.Update(ctx, etcdPvc)
			if err != nil {
				const baseMessageError = "Failed to update Etcd Pvc"
				log.Error(err, baseMessageError, "Deployment.Namespace", etcdPvc.Namespace, "Deployment.Name", etcdPvc.Name)
				UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedEtcd, metav1.ConditionFalse, "Reconciling:Update:Pvc:Error", baseMessageError+"->"+err.Error())
				return thereWasAnUpdate, err
			}
			thereWasAnUpdate = true
		}
	}
	// Create an Etcd Deployment
	etcdDeployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: milvusEctdName, Namespace: morpheus.Namespace}, etcdDeployment)

	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		var deploymentEtcd *appsv1.Deployment
		deploymentEtcd = r.createEtcdDeployment(morpheus, milvusEctdPvcName, milvusEctdName)
		log.Info("Creating a new Deployment", "Deployment.Namespace", deploymentEtcd.Namespace, "Deployment.Name", deploymentEtcd.Name)
		err = r.Create(ctx, deploymentEtcd)
		if err != nil {
			const baseErrorMessage = "Failed to create new milvus-etcd Deployment"
			log.Error(err, baseErrorMessage, "Deployment.Namespace", deploymentEtcd.Namespace, "Deployment.Name", deploymentEtcd.Name)
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedEtcd, metav1.ConditionFalse, "Reconciling:Create:Deployment:Error", baseErrorMessage+" ->"+err.Error())
			return thereWasAnUpdate, err
		} else {
			resourceCreated = true
		}
	} else if err != nil {
		log.Error(err, "Failed to get Etcd Deployment")
		return thereWasAnUpdate, err
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
			Port:     int32(etcdPort),
			TargetPort: intstr.IntOrString{
				Type:   intstr.Int,
				IntVal: int32(etcdPort),
			},
		},
		}
		selector := map[string]string{
			"app":       "milvus",
			"component": "etcd",
		}
		serviceEtcd = r.createService(morpheus, ports, milvusEctdName, selector)
		log.Info("Creating a new Etcd Service", "Service.Namespace", serviceEtcd.Namespace, "Service.Name", serviceEtcd.Name)

		err = r.Create(ctx, serviceEtcd)
		if err != nil {
			const baseErrorMessage = "Failed to create new Etcd Service"
			log.Error(err, baseErrorMessage, "Service.Namespace", serviceEtcd.Namespace, "Service.Name", serviceEtcd.Name)
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedEtcd, metav1.ConditionFalse, "Reconciling:Create:Service:Error", baseErrorMessage+" ->"+err.Error())
			return thereWasAnUpdate, err
		} else {
			resourceCreated = true
		}
	} else if err != nil {
		log.Error(err, "Failed to get Etcd Service")
		return thereWasAnUpdate, err
	}

	if resourceCreated {
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedEtcd, metav1.ConditionTrue, "Reconciling:Create", "Etcd Instance successfully Deployed!")
	}
	return thereWasAnUpdate, nil
}

func deployMinio(r *MorpheusReconciler, ctx context.Context, morpheus *aiv1alpha1.Morpheus, log logr.Logger, milvusMinioPvcName string, milvusMinioName string, minioPort int) (bool, error) {
	// Create a Persistent volume claim to store minio data for milvus db.
	var thereWasAnUpdate = false
	resourceCreated := false
	minioPvc := &corev1.PersistentVolumeClaim{}
	err := r.Get(ctx, types.NamespacedName{Name: milvusMinioPvcName, Namespace: morpheus.Namespace}, minioPvc)

	const defaultMinioPvcSize = "2Gi"
	minioPvcSize := getFromSpecElseDefault(morpheus.Spec.Milvus.Minio.StoragePvcSize, defaultMinioPvcSize)
	if err != nil && errors.IsNotFound(err) {
		// Define a new pvc
		var pvcMinio *corev1.PersistentVolumeClaim
		pvcMinio = r.createPvc(morpheus, milvusMinioPvcName, minioPvcSize)
		log.Info("Creating a new Pvc for Minio Data", "Pvc.Namespace", pvcMinio.Namespace, "Pvc.Name", pvcMinio.Name)
		err = r.Create(ctx, pvcMinio)
		if err != nil {
			log.Error(err, "Failed to create Pvc for Minio Instance", "Pvc.Namespace", pvcMinio.Namespace, "Pvc.Name", pvcMinio.Name)
			return thereWasAnUpdate, err
		} else {
			resourceCreated = true
		}
	} else if err != nil {
		log.Error(err, "Failed to get milvus minio data PVC ")
		return thereWasAnUpdate, err
	} else {
		// pvc storage size was changed.
		if minioPvcSize != minioPvc.Spec.Resources.Requests.Storage().String() {
			minioPvc.Spec.Resources.Requests["storage"] = resource.MustParse(minioPvcSize)
			err = r.Update(ctx, minioPvc)
			if err != nil {
				log.Error(err, "Failed to update Minio Pvc", "Deployment.Namespace", minioPvc.Namespace, "Deployment.Name", minioPvc.Name)
				return thereWasAnUpdate, err
			}
			thereWasAnUpdate = true
		}
	}

	// Create a minio Deployment
	minioDeployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: milvusMinioName, Namespace: morpheus.Namespace}, minioDeployment)

	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		var deploymentMinio *appsv1.Deployment
		deploymentMinio = r.createMinioDeployment(ctx, morpheus, milvusMinioPvcName, milvusMinioName)
		log.Info("Creating a new Deployment", "Deployment.Namespace", deploymentMinio.Namespace, "Deployment.Name", deploymentMinio.Name)
		err = r.Create(ctx, deploymentMinio)
		if err != nil {
			const baseErrorMessage = "Failed to create new milvus-minio Deployment"
			log.Error(err, baseErrorMessage, "Deployment.Namespace", deploymentMinio.Namespace, "Deployment.Name", deploymentMinio.Name)
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMinio, metav1.ConditionFalse, "Reconciling:Create:Deployment:Error", baseErrorMessage+" ->"+err.Error())
			return thereWasAnUpdate, err
		} else {
			resourceCreated = true
		}
	} else if err != nil {
		const errorMessage = "Failed to get Minio Deployment"
		log.Error(err, errorMessage)
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMinio, metav1.ConditionFalse, "Reconciling:Fetch:Deployment:Error", errorMessage+"->"+err.Error())
		return thereWasAnUpdate, err
	} else {
		// Check if deployment data was changed.
		minioSpecUser := getFromSpecElseDefault(morpheus.Spec.Milvus.Minio.RootUser, ctx.Value("minioDefaultUser").(string))
		minioSpecPassword := getFromSpecElseDefault(morpheus.Spec.Milvus.Minio.RootPassword, ctx.Value("minioDefaultPassword").(string))
		thereWasEnvironmentUpdate := false
		for _, env := range minioDeployment.Spec.Template.Spec.Containers[0].Env {
			if env.Name == "MINIO_ROOT_USER" {
				if env.Value != minioSpecUser {
					env.Value = minioSpecUser
					thereWasEnvironmentUpdate = true
				}
			} else if env.Name == "MINIO_ROOT_PASSWORD" {
				if env.Value != minioSpecPassword {
					env.Value = minioSpecPassword
					thereWasEnvironmentUpdate = true
				}
			}
		}
		if thereWasEnvironmentUpdate {
			err = r.Update(ctx, minioDeployment)
			if err != nil {
				const baseMessageError = "Failed to update Minio Deployment"
				log.Error(err, baseMessageError, "Deployment.Namespace", minioDeployment.Namespace, "Deployment.Name", minioDeployment.Name)
				UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMinio, metav1.ConditionFalse, "Reconciling:Update:Deployment:Error", baseMessageError+"->"+err.Error())
				return thereWasAnUpdate, err
			}
			thereWasAnUpdate = true
		}
	}

	// Create a minio Service
	minioService := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: milvusMinioName, Namespace: morpheus.Namespace}, minioService)

	if err != nil && errors.IsNotFound(err) {
		// Define a new Service
		var serviceMinio *corev1.Service
		ports := []corev1.ServicePort{
			{
				Name:     "api",
				Protocol: corev1.ProtocolTCP,
				Port:     int32(minioPort),
				TargetPort: intstr.IntOrString{
					Type:   intstr.Int,
					IntVal: int32(minioPort),
				},
			},
			{
				Name:     "console",
				Protocol: corev1.ProtocolTCP,
				Port:     9001,
				TargetPort: intstr.IntOrString{
					Type:   intstr.Int,
					IntVal: 9001,
				},
			},
		}
		selector := map[string]string{
			"app":       "milvus",
			"component": "minio",
		}
		serviceMinio = r.createService(morpheus, ports, milvusMinioName, selector)
		log.Info("Creating a new minio Service", "Service.Namespace", serviceMinio.Namespace, "Service.Name", serviceMinio.Name)
		err = r.Create(ctx, serviceMinio)
		if err != nil {
			const baseErrorMessage = "Failed to create new minio Service"
			log.Error(err, baseErrorMessage, "Service.Namespace", serviceMinio.Namespace, "Service.Name", serviceMinio.Name)
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMinio, metav1.ConditionFalse, "Reconciling:Create:Service:Error", baseErrorMessage+" ->"+err.Error())
			return thereWasAnUpdate, err
		}
	} else if err != nil {
		const errorMessage = "Failed to get Minio Service"
		log.Error(err, errorMessage)
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMinio, metav1.ConditionFalse, "Reconciling:Fetch:Service:Error", errorMessage+" ->"+err.Error())
		return thereWasAnUpdate, err
	} else {
		resourceCreated = true
	}
	if resourceCreated {
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMinio, metav1.ConditionTrue, "Reconciling:Create", "Minio Instance successfully Deployed!")
	}
	return thereWasAnUpdate, nil
}

func deployTritonServer(r *MorpheusReconciler, ctx context.Context, morpheus *aiv1alpha1.Morpheus, log logr.Logger) (bool, error) {

	// Create a Persistent volume claim to store morpheus repo content, that will be mounted into triton server' container
	thereWasAnUpdate := false
	resourceCreated := false
	morpheusRepoPvc := &corev1.PersistentVolumeClaim{}
	const morpheusRepoPvcName = "morpheus-repo"
	err := r.Get(ctx, types.NamespacedName{Name: morpheusRepoPvcName, Namespace: morpheus.Namespace}, morpheusRepoPvc)

	const defaultTritonServerMorpheusRepoPvcSize = "20Gi"
	tritonServerMorpheusRepoPvcSize := getFromSpecElseDefault(morpheus.Spec.TritonServer.MorpheusRepoStorageSize, defaultTritonServerMorpheusRepoPvcSize)

	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		var pvcMorpheus *corev1.PersistentVolumeClaim
		pvcMorpheus = r.createPvc(morpheus, morpheusRepoPvcName, tritonServerMorpheusRepoPvcSize)
		log.Info("Creating a new Pvc for Triton Server", "Pvc.Namespace", pvcMorpheus.Namespace, "Pvc.Name", pvcMorpheus.Name)
		err = r.Create(ctx, pvcMorpheus)
		if err != nil {
			const errorMessageBase = "Failed to create Pvc for Triton Server"
			log.Error(err, errorMessageBase, "Pvc.Namespace", pvcMorpheus.Namespace, "Pvc.Name", pvcMorpheus.Name)
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedTritonServer, metav1.ConditionFalse, "Reconciling:Create:Pvc:Error", errorMessageBase)
			return thereWasAnUpdate, err
		}
	} else if err != nil {
		log.Error(err, "Failed to get Morpheus repository PVC ")
		return thereWasAnUpdate, err
	} else {
		// pvc storage size was changed.
		if tritonServerMorpheusRepoPvcSize != morpheusRepoPvc.Spec.Resources.Requests.Storage().String() {
			morpheusRepoPvc.Spec.Resources.Requests["storage"] = resource.MustParse(tritonServerMorpheusRepoPvcSize)
			err = r.Update(ctx, morpheusRepoPvc)
			if err != nil {
				const baseMessageError = "Failed to update Triton Server' Morpheus repo Pvc"
				log.Error(err, baseMessageError, "Deployment.Namespace", morpheusRepoPvc.Namespace, "Deployment.Name", morpheusRepoPvc.Name)
				UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedTritonServer, metav1.ConditionFalse, "Reconciling:Update:Pvc:Error", baseMessageError+"->"+err.Error())
				return thereWasAnUpdate, err
			}
			thereWasAnUpdate = true
		} else {
			resourceCreated = true
		}
	}

	// Create a Triton Inference Server Deployment
	tritonDeployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: "triton-server", Namespace: morpheus.Namespace}, tritonDeployment)

	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		var deploymentTriton *appsv1.Deployment

		deploymentTriton = r.createTritonDeployment(morpheus, morpheusRepoPvcName)
		log.Info("Creating a new Deployment", "Deployment.Namespace", deploymentTriton.Namespace, "Deployment.Name", deploymentTriton.Name)
		err = r.Create(ctx, deploymentTriton)
		if err != nil {
			const baseErrorMessage = "Failed to create new Triton Server Deployment"
			log.Error(err, baseErrorMessage, "Deployment.Namespace", deploymentTriton.Namespace, "Deployment.Name", deploymentTriton.Name)
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedTritonServer, metav1.ConditionFalse, "Reconciling:Create:Deployment:Error", baseErrorMessage+" ->"+err.Error())
			return thereWasAnUpdate, err
		}
	} else if err != nil {
		const errorMessage = "Failed to get Triton Server Deployment"
		log.Error(err, errorMessage)
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedTritonServer, metav1.ConditionFalse, "Reconciling:Fetch:Deployment:Error", errorMessage+"->"+err.Error())
		return thereWasAnUpdate, err
	} else {
		resourceCreated = true
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
				Type:   intstr.Int,
				IntVal: 8001,
			},
		},
			{
				Name:     "http",
				Protocol: corev1.ProtocolTCP,
				Port:     8000,
				TargetPort: intstr.IntOrString{
					Type:   intstr.Int,
					IntVal: 8000,
				},
			},
			{
				Name:     "metrics",
				Protocol: corev1.ProtocolTCP,
				Port:     8002,
				TargetPort: intstr.IntOrString{
					Type:   intstr.Int,
					IntVal: 8002,
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
			const baseErrorMessage = "Failed to create new Triton Server Service"
			log.Error(err, baseErrorMessage, "Service.Namespace", serviceTriton.Namespace, "Service.Name", serviceTriton.Name)
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedTritonServer, metav1.ConditionFalse, "Reconciling:Create:Service:Error", baseErrorMessage+" ->"+err.Error())
			return thereWasAnUpdate, err
		}
	} else if err != nil {
		const errorMessage = "Failed to get Triton Server Service"
		log.Error(err, errorMessage)
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedTritonServer, metav1.ConditionFalse, "Reconciling:Fetch:Service:Error", errorMessage+" ->"+err.Error())
		return thereWasAnUpdate, err
	} else {
		resourceCreated = true
	}
	if resourceCreated {
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedTritonServer, metav1.ConditionTrue, "Reconciling:Create", "Triton Server successfully Deployed!")
	}
	return thereWasAnUpdate, nil

}

// deploymentForMemcached returns a memcached Deployment object
func (r *MorpheusReconciler) createMorpheusDeployment(m *aiv1alpha1.Morpheus) *appsv1.Deployment {
	labels := labelsForComponent("morpheus", "v24.03.02", "")
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
					SecurityContext: &corev1.PodSecurityContext{
						RunAsUser: &user,
					},
				},
			},
		},
	}

	// Set Morpheus instance as the owner and controller
	ctrl.SetControllerReference(m, dep, r.Scheme)
	return dep
}

func labelsForComponent(name string, version string, component string) map[string]string {
	mapOfLabels := map[string]string{"app": name, "version": version}
	if component != "" {
		mapOfLabels["component"] = component
	}
	return mapOfLabels
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
			Verbs:         []string{"use"},
			APIGroups:     []string{"security.openshift.io"},
			ResourceNames: []string{"anyuid"},
			Resources:     []string{"securitycontextconstraints"},
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

func (r *MorpheusReconciler) createTritonDeployment(morpheus *aiv1alpha1.Morpheus, morpheusRepoPvcName string) *appsv1.Deployment {
	labels := labelsForComponent("triton-server", "v23.06", "")
	var numOfReplicas int32 = 1
	var rootUserId int64 = 0

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "triton-server",
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
					InitContainers: []corev1.Container{{
						Name:  "fetch-models",
						Image: "nvcr.io/nvidia/tritonserver:23.06-py3",
						Command: []string{"bash", "-c", "if [ ! -d /repo/Morpheus ] ; then ((curl -s https://packagecloud.io/install/repositories/github/git-lfs/script.deb.sh | bash)" +
							" && apt-get install git-lfs && git clone https://github.com/nv-morpheus/Morpheus.git /repo/Morpheus &&" +
							"cd /repo/Morpheus && ./scripts/fetch_data.py fetch models) ; fi "},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      morpheusRepoPvcName,
							MountPath: "/repo",
						}},
						SecurityContext: &corev1.SecurityContext{
							RunAsUser: &rootUserId,
						},
					}},
					Containers: []corev1.Container{{
						Image: "nvcr.io/nvidia/tritonserver:23.06-py3",
						Name:  "triton",
						Command: []string{"tritonserver", "--model-repository=/repo/Morpheus/models/triton-model-repo",
							"--exit-on-error=false", "--strict-readiness=false",
							"--disable-auto-complete-config", "--log-info=true"},

						VolumeMounts: []corev1.VolumeMount{{
							Name:      morpheusRepoPvcName,
							MountPath: "/repo",
						}},
					}},
					ServiceAccountName: morpheus.Spec.ServiceAccountName,
					Volumes: []corev1.Volume{{
						Name: morpheusRepoPvcName,
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: morpheusRepoPvcName,
							},
						},
					},
					},
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

func (r *MorpheusReconciler) createEtcdDeployment(morpheus *aiv1alpha1.Morpheus, milvusEtcdData string, milvusEctdName string) *appsv1.Deployment {
	labels := labelsForComponent("milvus", "v3.5.5", "etcd")
	var numOfReplicas int32 = 1

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      milvusEctdName,
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
					Volumes: []corev1.Volume{{
						Name: "etcd-data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: milvusEtcdData,
							},
						},
					}},
					Containers: []corev1.Container{{
						Name:    "etcd",
						Image:   "quay.io/coreos/etcd:v3.5.5",
						Command: []string{"etcd"},
						Ports: []corev1.ContainerPort{{
							Name:          "service",
							ContainerPort: 2379,
							Protocol:      "TCP",
						}},
						Env: []corev1.EnvVar{{
							Name:  "ETCD_AUTO_COMPACTION_MODE",
							Value: "revision",
						},
							{
								Name:  "ETCD_AUTO_COMPACTION_RETENTION",
								Value: "1000",
							},
							{
								Name:  "ETCD_QUOTA_BACKEND_BYTES",
								Value: "4294967296",
							},
							{
								Name:  "ETCD_SNAPSHOT_COUNT",
								Value: "50000",
							},
							{
								Name:  "ETCD_LISTEN_CLIENT_URLS",
								Value: "http://0.0.0.0:2379",
							},
							{
								Name:  "ETCD_ADVERTISE_CLIENT_URLS",
								Value: "http://127.0.0.1:2379",
							},
							{
								Name:  "ETCD_DATA_DIR",
								Value: "/etcd",
							},
						},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "etcd-data",
							MountPath: "/etcd",
						}},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								Exec: &corev1.ExecAction{
									Command: []string{"etcdctl", "endpoint", "health"},
								},
							},
							InitialDelaySeconds: 2,
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								Exec: &corev1.ExecAction{
									Command: []string{"etcdctl", "endpoint", "health"},
								},
							},
							InitialDelaySeconds: 2,
						},
					}},
				},
			},
		},
	}

	// Set Morpheus instance as the owner and controller
	ctrl.SetControllerReference(morpheus, dep, r.Scheme)
	return dep
}
func (r *MorpheusReconciler) createMinioDeployment(ctx context.Context, morpheus *aiv1alpha1.Morpheus, MilvusPvc string, milvusMinioName string) *appsv1.Deployment {
	labels := labelsForComponent("milvus", "v3.5.5", "minio")
	var numOfReplicas int32 = 1

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      milvusMinioName,
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
					Volumes: []corev1.Volume{{
						Name: "minio-data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: MilvusPvc,
							},
						},
					}},
					Containers: []corev1.Container{{
						Name:    "minio",
						Image:   "minio/minio:RELEASE.2023-03-20T20-16-18Z",
						Command: []string{"minio", "server", "/minio_data", "--console-address", ":9001"},
						Ports: []corev1.ContainerPort{
							{
								Name:          "api",
								ContainerPort: 9000,
								Protocol:      "TCP",
							},
							{
								Name:          "console",
								ContainerPort: 9001,
								Protocol:      "TCP",
							},
						},
						Env: []corev1.EnvVar{
							{
								Name:  "MINIO_ROOT_USER",
								Value: getFromSpecElseDefault(morpheus.Spec.Milvus.Minio.RootUser, ctx.Value("minioDefaultUser").(string)),
							},
							{
								Name:  "MINIO_ROOT_PASSWORD",
								Value: getFromSpecElseDefault(morpheus.Spec.Milvus.Minio.RootPassword, ctx.Value("minioDefaultPassword").(string)),
							},
						},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "minio-data",
							MountPath: "/minio_data",
						}},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/minio/health/live",
									Port: intstr.IntOrString{
										IntVal: 9000,
									},
								},
							},
							InitialDelaySeconds: 2,
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/minio/health/live",
									Port: intstr.IntOrString{
										IntVal: 9000,
									},
								},
							},
							InitialDelaySeconds: 2,
						},
					}},
				},
			},
		},
	}

	// Set Morpheus instance as the owner and controller
	ctrl.SetControllerReference(morpheus, dep, r.Scheme)
	return dep
}
func (r *MorpheusReconciler) createMilvusDbDeployment(ctx context.Context, morpheus *aiv1alpha1.Morpheus, MilvusPvc string, minioServiceUrl string, etcdServiceUrl string) *appsv1.Deployment {
	labels := labelsForComponent("milvus", "v2.4.1", "milvus")
	var numOfReplicas int32 = 1

	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "milvus-standalone",
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
					Volumes: []corev1.Volume{{
						Name: "milvus-data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: MilvusPvc,
							},
						},
					}},
					Containers: []corev1.Container{{
						Name:    "milvus",
						Image:   "milvusdb/milvus:v2.4.1",
						Command: []string{"milvus", "run", "standalone"},
						Ports: []corev1.ContainerPort{
							{
								Name:          "grpc",
								ContainerPort: 19530,
								Protocol:      "TCP",
							},
							{
								Name:          "api",
								ContainerPort: 9091,
								Protocol:      "TCP",
							},
						},
						Env: []corev1.EnvVar{
							{
								Name:  "MINIO_ADDRESS",
								Value: minioServiceUrl,
							},

							{
								Name:  "ETCD_ENDPOINTS",
								Value: etcdServiceUrl,
							},

							{
								Name:  "MINIO_ACCESS_KEY_ID",
								Value: getFromSpecElseDefault(morpheus.Spec.Milvus.Minio.RootUser, ctx.Value("minioDefaultUser").(string)),
							},
							{
								Name:  "MINIO_SECRET_ACCESS_KEY",
								Value: getFromSpecElseDefault(morpheus.Spec.Milvus.Minio.RootPassword, ctx.Value("minioDefaultUser").(string)),
							},
						},
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "milvus-data",
							MountPath: "/var/lib/milvus",
						}},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/healthz",
									Port: intstr.IntOrString{
										IntVal: 9091,
									},
								},
							},
							InitialDelaySeconds: 2,
						},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/healthz",
									Port: intstr.IntOrString{
										IntVal: 9091,
									},
								},
							},
							InitialDelaySeconds: 2,
						},
					}},
				},
			},
		},
	}
	// Set Morpheus instance as the owner and controller
	ctrl.SetControllerReference(morpheus, dep, r.Scheme)
	return dep
}
