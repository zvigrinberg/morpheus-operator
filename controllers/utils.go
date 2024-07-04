package controllers

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	aiv1alpha1 "github.com/zvigrinberg/morpheus-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/strings/slices"
	"regexp"
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
		} else {
			log.Info(fmt.Sprintf("Successfully updated Morpheus status Type %s with status: %s, because of reason ->%s", conditionType, status, reason))
		}
	} else {
		return errors.New(fmt.Sprintf("ConditionType paramater must be one of => %v", allTypes))
	}
	return nil
}

func GetMd5HashString(data string) string {
	hash := md5.Sum([]byte(data))
	return string(hex.EncodeToString(hash[:]))
}

func camelCaseToScreamingSnakeCase(key string) string {
	var matchCaps = regexp.MustCompile("([a-z0-9])([A-Z])")
	screamingSnakeCase := matchCaps.ReplaceAllString(key, "${1}_${2}")
	return strings.ToUpper(screamingSnakeCase)
}

func ScreamingSnakeCaseToCamelCase(key string) string {
	keyLowered := strings.ToLower(key)
	var result []string = make([]string, len(keyLowered))
	nextLetterIsUpperCase := false
	for i, rune := range keyLowered {
		char := fmt.Sprintf("%c", rune)
		if char == "_" {
			nextLetterIsUpperCase = true
		} else {
			if nextLetterIsUpperCase {
				result[i] = strings.ToUpper(char)
				nextLetterIsUpperCase = false
			} else {
				result[i] = char
			}

		}
	}
	return strings.Join(result, "")
}

func getStringRepresentationOfMap(jupyterMorpheusEnvVars *corev1.ConfigMap) string {
	var sb strings.Builder
	for key, value := range jupyterMorpheusEnvVars.Data {
		sb.WriteString(fmt.Sprintf("%s=%s;", key, value))
	}
	return sb.String()
}

func encodeStringBase64(secretValue string) []byte {
	return []byte(base64.StdEncoding.EncodeToString([]byte(secretValue)))
}

func decodeBase64String(data []byte) ([]byte, error) {
	return base64.StdEncoding.DecodeString(string(data))
}

func ternary(condition bool, valueIfConditionIsTrue string, valueIfConditionIsFalse string) string {
	if condition {
		return valueIfConditionIsTrue
	} else {
		return valueIfConditionIsFalse
	}
}

// Create new map from configmap data dict/map, containing the all the (key,value) pairs with modified key converted to camel case,
// if it's doesn't exists as screaming camel case in custom resource env vars map/dict
func convertKeysToCamelCaseInMap(data map[string]string, crEnvVars map[string]string) map[string]string {
	mappedData := make(map[string]string, len(data))
	for key, value := range data {
		_, keyExists := crEnvVars[key]
		finalKey := key
		if !keyExists {
			finalKey = ScreamingSnakeCaseToCamelCase(key)
		}
		mappedData[finalKey] = value
	}

	return mappedData
}
