package structure

import (
	"context"
	"github.com/zvigrinberg/morpheus-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type MorpheusReconcilerParent interface {
	client.Client
	Reconcile(ctx context.Context, req controllerruntime.Request) (controllerruntime.Result, error)
	createMorpheusDeployment(m *v1alpha1.Morpheus) *appsv1.Deployment
	SetupWithManager(mgr controllerruntime.Manager) error
	morpheusServiceAccount(morpheus *v1alpha1.Morpheus) *corev1.ServiceAccount
	createAnyUidRole(morpheus *v1alpha1.Morpheus) *rbacv1.Role
	createAnyUidRoleBinding(morpheus *v1alpha1.Morpheus) *rbacv1.RoleBinding
	createPvc(morpheus *v1alpha1.Morpheus, pvcName string, pvcSize string) *corev1.PersistentVolumeClaim
	createTritonDeployment(morpheus *v1alpha1.Morpheus, morpheusRepoPvcName string) *appsv1.Deployment
	createService(morpheus *v1alpha1.Morpheus, ports []corev1.ServicePort, svcName string, selector map[string]string) *corev1.Service
	createEtcdDeployment(morpheus *v1alpha1.Morpheus, milvusEtcdData string, milvusEctdName string) *appsv1.Deployment
	createMinioDeployment(ctx context.Context, morpheus *v1alpha1.Morpheus, MilvusPvc string, milvusMinioName string) *appsv1.Deployment
	createMilvusDbDeployment(ctx context.Context, morpheus *v1alpha1.Morpheus, MilvusPvc string, minioServiceUrl string, etcdServiceUrl string) *appsv1.Deployment
}
