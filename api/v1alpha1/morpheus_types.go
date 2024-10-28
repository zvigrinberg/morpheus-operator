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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type MinioSpec struct {
	// Root User to authenticate with Minio Instance
	RootUser string `json:"rootUser,omitempty"`
	// Root Password to authenticate with Minio Instance
	RootPassword   string `json:"rootPassword,omitempty"`
	StoragePvcSize string `json:"storagePvcSize,omitempty"`
}

type EtcdSpec struct {
	StoragePvcSize string `json:"storagePvcSize,omitempty"`
}

type TritonSpec struct {
	MorpheusRepoStorageSize string `json:"morpheusRepoStorageSize,omitempty"`
}

type MilvusSpec struct {
	// Minio Instance Spec
	Minio MinioSpec `json:"minio,omitempty"`
	// Etcd Instance Spec
	Etcd EtcdSpec `json:"etcd,omitempty"`
	// Milvus DB Storage Size
	StoragePvcSize string `json:"storagePvcSize,omitempty"`
}

type JupyterSpec struct {
	// Password for Jupyter Lab UI Entrance
	LabPassword string `json:"labPassword,omitempty"`
	// Map of arbitrary key value pairs that will become environment variables in morpheus jupyter deployment
	EnvironmentVars map[string]string `json:"environmentVars,omitempty"`
}

// MorpheusSpec defines the desired state of Morpheus
type MorpheusSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	ServiceAccountName string `json:"serviceAccountName,omitempty"`
	// automatically add 'anyuid' SecurityContextConstraint permission to service account of Morpheus.
	AutoBindSccToSa bool `json:"autoBindSccToSa,omitempty"`
	// Milvus DB Spec
	Milvus MilvusSpec `json:"milvus,omitempty"`
	// Triton Server Spec
	TritonServer TritonSpec `json:"tritonServer,omitempty"`
	//Jupyter Notebook Lab Spec
	Jupyter JupyterSpec `json:"jupyter,omitempty"`

	NodeSelector map[string]string   `json:"nodeSelector,omitempty"`
	Tolerations  []corev1.Toleration `json:"tolerations,omitempty"`
}

// MorpheusStatus defines the observed state of Morpheus
type MorpheusStatus struct {
	// +operator-sdk:csv:customresourcedefinitions:type=status
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Morpheus is the Schema for the morpheuses API
type Morpheus struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MorpheusSpec   `json:"spec,omitempty"`
	Status MorpheusStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MorpheusList contains a list of Morpheus
type MorpheusList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Morpheus `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Morpheus{}, &MorpheusList{})
}
