/*
Copyright 2023 The Kubernetes Authors.

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

package v1alpha

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MydeviceSelector allows one to match on a specific type of Device as part of the class
type MydeviceSelector struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// MydeviceClassParametersSpec is the spec for the DeviceClassParametersSpec CRD
type MydeviceClassParametersSpec struct {
	MydeviceSelector []MydeviceSelector `json:"mydeviceSelector,omitempty"`
}

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
// +kubebuilder:resource:scope=Cluster

// MydeviceClassParameters holds the set of parameters provided when creating a resource class for this driver
type MydeviceClassParameters struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec MydeviceClassParametersSpec `json:"spec,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MydeviceClassParametersList represents the "plural" of a MydeviceClassParameters CRD object
type MydeviceClassParametersList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MydeviceClassParameters `json:"items"`
}
