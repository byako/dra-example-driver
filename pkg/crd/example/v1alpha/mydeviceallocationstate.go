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

// Types of Devices that can be allocated
const (
	MydeviceType0     = "type0" // make sure add more
	UnknownDeviceType = "unknown"
)

// AllocatableMydevice represents an allocatable device on a node
type AllocatableMydevice struct {
	CDIDevice string       `json:"cdiDevice"`
	Type      MydeviceType `json:"type"`
	UID       string       `json:"uid"` // PCI_DBDF-PCI_DEVICE_ID
}

// AllocatedMydevice represents an allocated device on a node
type AllocatedMydevice struct {
	CDIDevice string       `json:"cdiDevice"`
	Type      MydeviceType `json:"type"`
	UID       string       `json:"uid"`
}

// AllocatedMydevices represents a list of allocated devices on a node
// +kubebuilder:validation:MaxItems=8
type AllocatedMydevices []AllocatedMydevice

// +kubebuilder:validation:Enum=type0
type MydeviceType string

// RequestedMydevice represents a Mydevice being requested for allocation
type RequestedMydevice struct {
	UID string `json:"uid,omitempty"`
}

// RequestedMydevices represents a set of request spec and devices requested for allocation
type RequestedMydevices struct {
	Spec MydeviceClaimParametersSpec `json:"spec"`
	// +kubebuilder:validation:MaxItems=8
	Mydevices []RequestedMydevice `json:"mydevices"`
}

// MydeviceAllocationStateSpec is the spec for the MydeviceAllocationState CRD
type MydeviceAllocationStateSpec struct {
	AllocatableMydevices     map[string]AllocatableMydevice `json:"allocatableMydevice,omitempty"`
	ResourceClaimAllocations map[string]AllocatedMydevices  `json:"resourceClaimAllocations,omitempty"`
	ResourceClaimRequests    map[string]RequestedMydevices  `json:"resourceClaimRequests,omitempty"`
}

// +genclient
// +genclient:noStatus
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:openapi-gen=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:resource:singular=mas

// MydeviceAllocationState holds the state required for allocation on a node
type MydeviceAllocationState struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MydeviceAllocationStateSpec `json:"spec,omitempty"`
	Status string                      `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// MydeviceAllocationStateList represents the "plural" of a MydeviceAllocationState CRD object
type MydeviceAllocationStateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []MydeviceAllocationState `json:"items"`
}
