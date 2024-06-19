package controllers

import (
	"context"
	"errors"
	"fmt"
	aiv1alpha1 "github.com/zvigrinberg/morpheus-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/strings/slices"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"strings"
)

const (
	TypeDeployedMorpheus     = "MorpheusDeployed"
	TypeDeployedTritonServer = "TritonDeployed"
	TypeDeployedEtcd         = "EtcdDeployed"
	TypeDeployedMinio        = "MinioDeployed"
	TypeDeployedMilvusDB     = "MilvusDBDeployed"
)

var allTypes = []string{TypeDeployedMorpheus, TypeDeployedTritonServer, TypeDeployedEtcd, TypeDeployedMinio, TypeDeployedMilvusDB}

type StatusHandler struct {
}

func InitializeCrStatusImpl(ctx context.Context, morpheus *aiv1alpha1.Morpheus, r *MorpheusReconciler) error {

	log := log.FromContext(ctx)
	if morpheus.Status.Conditions == nil || len(morpheus.Status.Conditions) == 0 {
		for _, typeStatus := range allTypes {
			index := strings.LastIndex(typeStatus, "Deployed")
			meta.SetStatusCondition(&morpheus.Status.Conditions, metav1.Condition{
				Type:               typeStatus,
				Status:             metav1.ConditionUnknown,
				LastTransitionTime: metav1.Time{},
				Reason:             "Initializing",
				Message:            fmt.Sprintf("Waiting to %s to be Deployed", typeStatus[0:index]),
			})
		}
		if err := r.Status().Update(ctx, morpheus); err != nil {
			log.Error(err, "Failed to update Initialized Morpheus status")
			return err
		}
	}
	return nil
}
func UpdateCrStatusPerType(ctx context.Context, morpheus *aiv1alpha1.Morpheus, r *MorpheusReconciler, conditionType string, status metav1.ConditionStatus, reason string, message string) error {
	log := log.FromContext(ctx)
	if slices.Contains(allTypes, conditionType) {
		meta.SetStatusCondition(&morpheus.Status.Conditions, metav1.Condition{
			Type:               conditionType,
			Status:             status,
			LastTransitionTime: metav1.Time{},
			Reason:             reason,
			Message:            message,
		})
		if err := r.Status().Update(ctx, morpheus); err != nil {
			log.Error(err, fmt.Sprintf("Failed to update Morpheus status Type %s with status: %s, because of reason ->%s", conditionType, status, reason))
			return err
		}
	} else {
		return errors.New(fmt.Sprintf("ConditionType paramater must be one of => %v", allTypes))
	}
	return nil
}
