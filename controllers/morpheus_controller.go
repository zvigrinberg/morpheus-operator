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
	routev1 "github.com/openshift/api/route/v1"
	"github.com/thanhpk/randstr"
	aiv1alpha1 "github.com/zvigrinberg/morpheus-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"maps"
	"os"
	"reflect"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"strings"
)

// MorpheusReconciler reconciles a Morpheus object
type MorpheusReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const (
	jupyterPasswordHash      = "jupyter-password-hash"
	jupyterMorpheusCmEnvHash = "environment-hash"
)

//+kubebuilder:rbac:groups=ai.redhat.com,resources=morpheus,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=ai.redhat.com,resources=morpheus/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=ai.redhat.com,resources=morpheus/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=*,resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=*,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=*,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=*,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=*,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=security.openshift.io,resources=securitycontextconstraints,resourceNames=anyuid,verbs=use
// +kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch;create;update;patch;delete

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
	thereWasAnUpdate, err = deployMorpheusWithJupyter(ctx, morpheus, err, r, log)
	if err != nil {
		log.Error(err, "Failed to Deploy Morpheus with Jupyter Lab")
		return ctrl.Result{}, err
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

func deployMorpheusWithJupyter(ctx context.Context, morpheus *aiv1alpha1.Morpheus, err error, r *MorpheusReconciler, log logr.Logger) (bool, error) {
	thereWasAnUpdate := false
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
			return thereWasAnUpdate, err
		}
	} else if err != nil {
		log.Error(err, "Failed to get Service Account")
		return thereWasAnUpdate, err
	}
	autoBindSccToSa := &morpheus.Spec.AutoBindSccToSa
	// default to add anyuid scc (Security Context Constraint) to service account
	if autoBindSccToSa == nil {
		morpheus.Spec.AutoBindSccToSa = true
	}
	if morpheus.Spec.AutoBindSccToSa {
		err = createAnyUidSecurityContextConstraint(ctx, r, morpheus, log)
		if err != nil {
			return thereWasAnUpdate, err
		}
	}

	// Create Secret for Jupyter Notebook Lab UI access
	jupyterPassword := &corev1.Secret{}
	secretName := morpheus.Name + "-jupyter-token"
	const secretKey = "JUPYTER_TOKEN"
	secretWasChanged := false
	var md5HashOfSecret string
	err = r.Get(ctx, types.NamespacedName{Name: secretName, Namespace: morpheus.Namespace}, jupyterPassword)
	if err != nil && errors.IsNotFound(err) {
		randomTokenAsDefault := randstr.Hex(16)
		tokenValue := getFromSpecElseDefault(morpheus.Spec.Jupyter.LabPassword, randomTokenAsDefault)
		jupyterSecret := r.createSecret(morpheus, secretName, secretKey, getFromSpecElseDefault(morpheus.Spec.Jupyter.LabPassword, randomTokenAsDefault))
		md5HashOfSecret = GetMd5HashString(tokenValue)
		log.Info("Creating a new Secret", "Secret.Namespace", jupyterSecret.Namespace, "Secret.Name", jupyterSecret.Name)
		err = r.Create(ctx, jupyterSecret)
		if err != nil {
			log.Error(err, "Failed to create Jupyter Secret", "Secret.Namespace", jupyterSecret.Namespace, "Secret.Name", jupyterSecret.Name)
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionFalse, "Reconciling:Secret:Create:Error", err.Error())
			return thereWasAnUpdate, err
		}
	} else if err != nil {
		const errorMessage = "Failed to get Jupyter Secret"
		log.Error(err, errorMessage)
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionFalse, "Reconciling:Secret:Fetch:Error", errorMessage+"->"+err.Error())
		return thereWasAnUpdate, err
	} else {
		// check if jupyter password was changed.
		passwordValue := string(jupyterPassword.Data[secretKey])
		if strings.TrimSpace(morpheus.Spec.Jupyter.LabPassword) != "" &&
			strings.TrimSpace(morpheus.Spec.Jupyter.LabPassword) != strings.TrimSpace(string(passwordValue)) {
			jupyterPassword.Data[secretKey] = []byte(morpheus.Spec.Jupyter.LabPassword)
			md5HashOfSecret = GetMd5HashString(string(jupyterPassword.Data[secretKey]))
			err = r.Update(ctx, jupyterPassword)
			if err != nil {
				log.Error(err, "Failed to update Jupyter Secret", "Secret.Namespace", jupyterPassword.Namespace, "Secret.Name", jupyterPassword.Name)
				UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionFalse, "Reconciling:Secret:Update:Error", err.Error())
				return thereWasAnUpdate, err
			}
			thereWasAnUpdate = true
			secretWasChanged = true
		}
	}
	// Create ConfigMap for Environment Variables of Morpheus Jupyter Deployment

	jupyterMorpheusEnvVars := &corev1.ConfigMap{}
	cmName := morpheus.Name + "-env-vars"
	cmWasChanged := false
	var md5HashOfEnv string
	err = r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: morpheus.Namespace}, jupyterMorpheusEnvVars)
	if err != nil && errors.IsNotFound(err) {
		jupyterMorpheusEnvVariables := r.createConfigMap(morpheus, cmName)
		md5HashOfEnv = GetMd5HashString(getStringRepresentationOfMap(jupyterMorpheusEnvVars))
		log.Info("Creating a new ConfigMap", "ConfigMap.Namespace", jupyterMorpheusEnvVariables.Namespace, "ConfigMap.Name", jupyterMorpheusEnvVariables.Name)
		err = r.Create(ctx, jupyterMorpheusEnvVariables)
		if err != nil {
			log.Error(err, "Failed to create Jupyter Morpheus ConfigMap", "Secret.Namespace", jupyterMorpheusEnvVariables.Namespace, "Secret.Name", jupyterMorpheusEnvVariables.Name)
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionFalse, "Reconciling:Secret:Create:Error", err.Error())
			return thereWasAnUpdate, err
		}
	} else if err != nil {
		const errorMessage = "Failed to get Jupyter Morpheus Configmap"
		log.Error(err, errorMessage)
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionFalse, "Reconciling:Configmap:Fetch:Error", errorMessage+"->"+err.Error())
		return thereWasAnUpdate, err
	} else {
		// check if jupyter morpheus configmap of env vars was changed.
		comparableMap := convertKeysToCamelCaseInMap(jupyterMorpheusEnvVars.Data, morpheus.Spec.Jupyter.EnvironmentVars)
		if !reflect.DeepEqual(comparableMap, morpheus.Spec.Jupyter.EnvironmentVars) {

			var envVars map[string]string = make(map[string]string)
			for key, value := range morpheus.Spec.Jupyter.EnvironmentVars {
				envVars[camelCaseToScreamingSnakeCase(key)] = value
			}
			jupyterMorpheusEnvVars.Data = envVars
			md5HashOfEnv = GetMd5HashString(getStringRepresentationOfMap(jupyterMorpheusEnvVars))
			err = r.Update(ctx, jupyterMorpheusEnvVars)
			if err != nil {
				log.Error(err, "Failed to update Jupyter ConfigMap", "ConfigMap.Namespace", jupyterMorpheusEnvVars.Namespace, "ConfigMap.Name", jupyterMorpheusEnvVars.Name)
				UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionFalse, "Reconciling:ConfigMap:Update:Error", err.Error())
				return thereWasAnUpdate, err
			}
			thereWasAnUpdate = true
			cmWasChanged = true
		}
	}
	// Checks Whether Morpheus deployment exists.
	morpheusDeployment := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: morpheus.Name, Namespace: morpheus.Namespace}, morpheusDeployment)
	if err != nil && errors.IsNotFound(err) {
		// Define a new deployment
		deploymentMorpheus := r.createMorpheusDeployment(morpheus, md5HashOfSecret, secretName, cmName, jupyterMorpheusCmEnvHash)
		log.Info("Creating a new Deployment", "Deployment.Namespace", deploymentMorpheus.Namespace, "Deployment.Name", deploymentMorpheus.Name)
		err = r.Create(ctx, deploymentMorpheus)
		if err != nil {
			log.Error(err, "Failed to create new Morpheus-Jupyter Deployment", "Deployment.Namespace", deploymentMorpheus.Namespace, "Deployment.Name", deploymentMorpheus.Name)
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionFalse, "Reconciling:Creation:Error", err.Error())
			return thereWasAnUpdate, err
		}
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionTrue, "Reconciling:Create", "Morpheus Deployment successfully created and deployed!")
	} else if err != nil {
		const errorMessage = "Failed to get Morpheus-Jupyter Deployment"
		log.Error(err, errorMessage)
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionFalse, "Reconciling:Deployment:Fetch:Error", errorMessage+"->"+err.Error())
		return thereWasAnUpdate, err
	} else {
		serviceAccountDeployment := morpheusDeployment.Spec.Template.Spec.ServiceAccountName
		if serviceAccountDeployment != morpheus.Spec.ServiceAccountName || secretWasChanged || cmWasChanged {
			if secretWasChanged {
				morpheusDeployment.Spec.Template.Annotations[jupyterPasswordHash] = md5HashOfSecret
			} else {
				if cmWasChanged {
					morpheusDeployment.Spec.Template.Annotations[jupyterMorpheusCmEnvHash] = md5HashOfEnv
				} else {
					morpheusDeployment.Spec.Template.Spec.ServiceAccountName = morpheus.Spec.ServiceAccountName
				}
			}

			err = r.Update(ctx, morpheusDeployment)
			if err != nil {
				log.Error(err, "Failed to update Morpheus-jupyter-Deployment", "Deployment.Namespace", morpheusDeployment.Namespace, "Deployment.Name", morpheusDeployment.Name)
				UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionFalse, "Reconciling:Update:Error", err.Error())
				return thereWasAnUpdate, err
			}
			thereWasAnUpdate = true
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionTrue, ternary(secretWasChanged, "Reconciling:Secret:Update", "Reconciling:Update"), "Morpheus-Jupyter Deployment successfully Updated!")
		}
	}
	//Create A Morpheus-Jupyter Service
	morpheusJupyterSvc := &corev1.Service{}

	err = r.Get(ctx, types.NamespacedName{Name: morpheus.Name, Namespace: morpheus.Namespace}, morpheusJupyterSvc)

	if err != nil && errors.IsNotFound(err) {
		// Define a new Service
		var serviceJupyter *corev1.Service
		ports := []corev1.ServicePort{
			{
				Name:     "http",
				Protocol: corev1.ProtocolTCP,
				Port:     8888,
				TargetPort: intstr.IntOrString{
					Type:   intstr.Int,
					IntVal: 8888,
				},
			},
		}
		selector := map[string]string{
			"app":       "morpheus",
			"component": "jupyter",
		}
		serviceJupyter = r.createService(morpheus, ports, morpheus.Name, selector)
		log.Info("Creating a new Jupyter Service", "Service.Namespace", serviceJupyter.Namespace, "Service.Name", serviceJupyter.Name)
		err = r.Create(ctx, serviceJupyter)
		if err != nil {
			log.Error(err, "Failed to create new Jupyter Service", "Service.Namespace", serviceJupyter.Namespace, "Service.Name", serviceJupyter.Name)
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionFalse, "Reconciling:Service:Create:Error", err.Error())
			return false, err
		}
	} else if err != nil {
		log.Error(err, "Failed to get Jupyter Service")
		return false, err
	}
	jupyterRoute := &routev1.Route{}
	err = r.Get(ctx, types.NamespacedName{Name: morpheus.Name, Namespace: morpheus.Namespace}, jupyterRoute)
	if err != nil && errors.IsNotFound(err) {
		// Create an openshift route to expose jupyter notebook outside the cluster
		route := r.createRoute(morpheus)
		err = r.Create(ctx, route)
		if err != nil {
			log.Error(err, "Failed to Expose new  Route to Jupyter service", "Route.Namespace", route.Namespace, "Route.Name", route.Name)
			UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedMorpheus, metav1.ConditionFalse, "Reconciling:Route:Create:Error", err.Error())
			return false, err
		}
	} else if err != nil {
		log.Error(err, "Failed to get Route to Jupyter Service")
		return false, err
	}
	return thereWasAnUpdate, nil
}

func createAnyUidSecurityContextConstraint(ctx context.Context, r *MorpheusReconciler, morpheus *aiv1alpha1.Morpheus, log logr.Logger) error {
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
			return err
		}
	} else if err != nil {
		log.Error(err, "Failed to get Role")
		return err
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
			return err
		}
	} else if err != nil {
		log.Error(err, "Failed to get Role")
		return err
	}
	return nil
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
		} else {
			resourceCreated = true
		}
	} else if err != nil {
		const errorMessage = "Failed to get Triton Server Service"
		log.Error(err, errorMessage)
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedTritonServer, metav1.ConditionFalse, "Reconciling:Fetch:Service:Error", errorMessage+" ->"+err.Error())
		return thereWasAnUpdate, err
	}
	if resourceCreated {
		UpdateCrStatusPerType(ctx, morpheus, r, TypeDeployedTritonServer, metav1.ConditionTrue, "Reconciling:Create", "Triton Server successfully Deployed!")
	}
	return thereWasAnUpdate, nil

}

// deploymentForMemcached returns a memcached Deployment object
func (r *MorpheusReconciler) createMorpheusDeployment(m *aiv1alpha1.Morpheus, secretHashValue string, secretName string, configMapName string, hashOfCmContent string) *appsv1.Deployment {
	labels := labelsForComponent("morpheus", "v24.03.02", "jupyter")
	annotations := annotationForDeployment(jupyterPasswordHash, secretHashValue)
	annotations2 := annotationForDeployment(jupyterMorpheusCmEnvHash, hashOfCmContent)
	maps.Copy(annotations, annotations2)
	labels[jupyterPasswordHash] = secretHashValue
	var numOfReplicas int32 = 1
	var user int64 = 0

	envVars := []corev1.EnvVar{
		{
			Name:  "CONDA_DEFAULT_ENV",
			Value: "morpheus",
		},
		{
			Name:  "OPENAI_BASE_URL",
			Value: "https://integrate.api.nvidia.com/v1",
		},
	}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
			Labels:    commonLabelsForAllComponents(),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &numOfReplicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: corev1.PodSpec{
					NodeSelector: getInputNodeSelectorElseDefault(m.Spec.NodeSelector),
					Tolerations:  getInputTolerationsElseDefault(m.Spec.Tolerations),
					Containers: []corev1.Container{{
						Name:    "morpheus-jupyter",
						Image:   "quay.io/zgrinber/morpheus-jupyter:3",
						Command: []string{"bash"},
						Args:    []string{"-c", "(mamba env update -n ${CONDA_DEFAULT_ENV} --file /workspace/conda/environments/all_cuda-121_arch-x86_64.yaml &) ; jupyter-lab --ip=0.0.0.0 --no-browser --allow-root"},
						EnvFrom: []corev1.EnvFromSource{
							{
								SecretRef: &corev1.SecretEnvSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: secretName,
									},
								},
							},
							{
								ConfigMapRef: &corev1.ConfigMapEnvSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: configMapName,
									},
								},
							},
						},
						Env: envVars,
						SecurityContext: &corev1.SecurityContext{
							RunAsUser: &user,
						},
						Resources: corev1.ResourceRequirements{
							Limits:   corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
							Requests: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
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

func commonLabelsForAllComponents() map[string]string {
	createdBy, exists := os.LookupEnv("CREATED_BY")
	if !exists {
		createdBy = "morpheus-operator"
	}
	morpheusJupyterVersion, versionExists := os.LookupEnv("MORPHEUS_JUPYTER_VERSION")
	labels := map[string]string{
		"created-by": createdBy,
	}
	if versionExists {
		labels["morpheus-jupyter-version"] = morpheusJupyterVersion
	}
	return labels
}

func annotationForDeployment(key string, value string) map[string]string {
	mapOfAnnotations := map[string]string{key: value}
	return mapOfAnnotations
}

func labelsForComponent(name string, version string, component string) map[string]string {
	mapOfLabels := map[string]string{"app": name, "version": version}
	if component != "" {
		mapOfLabels["component"] = component
	}
	return mapOfLabels
}

func ignoreRedundantEvents() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			// Ignore updates to CR status in which case metadata.Generation does not change
			return e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration()
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// Evaluates to false if the object has been confirmed deleted.
			return !e.DeleteStateUnknown
		},
		GenericFunc: func(e event.GenericEvent) bool {
			// Filter out anyway GenericEvent events, which are irrelevant.
			return false
		},
		CreateFunc: func(e event.CreateEvent) bool {
			// Evaluates to false if the object type is not of kind `Morpheus`
			switch item := e.Object.(type) {
			case *aiv1alpha1.Morpheus:
				println("Handling Morpheus type instance ->" + item.GetName())
				return true
			default:
				return false
			}

		},
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *MorpheusReconciler) SetupWithManager(mgr ctrl.Manager) error {
	err := routev1.AddToScheme(mgr.GetScheme())
	if err != nil {
		println("Error intercepted in SetupWithManager=> error message => " + err.Error())
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&aiv1alpha1.Morpheus{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&routev1.Route{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ConfigMap{}).
		WithEventFilter(ignoreRedundantEvents()).
		Complete(r)
}

func (r *MorpheusReconciler) morpheusServiceAccount(morpheus *aiv1alpha1.Morpheus) *corev1.ServiceAccount {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      morpheus.Spec.ServiceAccountName,
			Namespace: morpheus.Namespace,
			Labels:    commonLabelsForAllComponents(),
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
			Labels:    commonLabelsForAllComponents(),
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
			Labels:    commonLabelsForAllComponents(),
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
			Labels:    commonLabelsForAllComponents(),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
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
			Labels:    commonLabelsForAllComponents(),
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
					NodeSelector: getInputNodeSelectorElseDefault(morpheus.Spec.NodeSelector),
					Tolerations:  getInputTolerationsElseDefault(morpheus.Spec.Tolerations),
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
						Resources: corev1.ResourceRequirements{
							Limits:   corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
							Requests: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")},
						},
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

func getInputNodeSelectorElseDefault(nodeSelector map[string]string) map[string]string {
	if &nodeSelector != nil && len(nodeSelector) > 0 {
		return nodeSelector
	} else {
		return map[string]string{"nvidia.com/gpu.deploy.driver": "true"}
	}
}

func getInputTolerationsElseDefault(tolerations []corev1.Toleration) []corev1.Toleration {

	if &tolerations != nil && len(tolerations) > 0 {
		return tolerations
	}
	return []corev1.Toleration{{
		Key:      "p4-gpu",
		Operator: "Exists",
		Effect:   "NoSchedule",
	}}

}

func (r *MorpheusReconciler) createService(morpheus *aiv1alpha1.Morpheus, ports []corev1.ServicePort, svcName string, selector map[string]string) *corev1.Service {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: morpheus.Namespace,
			Labels:    commonLabelsForAllComponents(),
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
			Labels:    commonLabelsForAllComponents(),
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
			Labels:    commonLabelsForAllComponents(),
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
			Labels:    commonLabelsForAllComponents(),
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

func (r *MorpheusReconciler) createSecret(morpheus *aiv1alpha1.Morpheus, secretName string, secretKey string, secretValue string) *corev1.Secret {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: morpheus.Namespace,
			Labels:    commonLabelsForAllComponents(),
		},
		Data: map[string][]byte{
			secretKey: []byte(secretValue),
		},
	}
	ctrl.SetControllerReference(morpheus, secret, r.Scheme)
	return secret
}

func (r *MorpheusReconciler) createRoute(morpheus *aiv1alpha1.Morpheus) *routev1.Route {
	weight := int32(100)

	routeSpec := routev1.RouteSpec{
		TLS: &routev1.TLSConfig{
			Termination:                   routev1.TLSTerminationEdge,
			InsecureEdgeTerminationPolicy: routev1.InsecureEdgeTerminationPolicyRedirect,
		},
		To: routev1.RouteTargetReference{
			Kind:   "Service",
			Name:   morpheus.Name,
			Weight: &weight,
		},
	}

	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      morpheus.Name,
			Namespace: morpheus.Namespace,
			Labels:    commonLabelsForAllComponents(),
		},
		Spec: routeSpec,
	}
	ctrl.SetControllerReference(morpheus, route, r.Scheme)
	return route
}

func (r *MorpheusReconciler) createConfigMap(morpheus *aiv1alpha1.Morpheus, name string) *corev1.ConfigMap {
	var envVars map[string]string = make(map[string]string)
	for key, value := range morpheus.Spec.Jupyter.EnvironmentVars {
		envVars[camelCaseToScreamingSnakeCase(key)] = value
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: morpheus.Namespace,
		},
		Data: envVars,
	}
	ctrl.SetControllerReference(morpheus, cm, r.Scheme)
	return cm
}
