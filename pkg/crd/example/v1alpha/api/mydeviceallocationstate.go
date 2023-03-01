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

package api

import (
	"context"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	myclientset "github.com/kubernetes-sigs/dra-example-driver/pkg/crd/example/clientset/versioned"
	mycrd "github.com/kubernetes-sigs/dra-example-driver/pkg/crd/example/v1alpha"
)

const (
	MydeviceAllocationStateStatusReady    = "Ready"
	MydeviceAllocationStateStatusNotReady = "NotReady"
)

type MydeviceAllocationStateConfig struct {
	Name      string
	Namespace string
	Owner     *metav1.OwnerReference
}

type AllocatableMydevice = mycrd.AllocatableMydevice
type AllocatedMydevice = mycrd.AllocatedMydevice
type AllocatedMydevices = mycrd.AllocatedMydevices
type RequestedMydevice = mycrd.RequestedMydevice
type RequestedMydevices = mycrd.RequestedMydevices
type MydeviceAllocationStateSpec = mycrd.MydeviceAllocationStateSpec
type MydeviceAllocationStateList = mycrd.MydeviceAllocationStateList

type MydeviceAllocationState struct {
	*mycrd.MydeviceAllocationState
	clientset myclientset.Interface
}

func NewMydeviceAllocationState(config *MydeviceAllocationStateConfig, clientset myclientset.Interface) *MydeviceAllocationState {
	object := &mycrd.MydeviceAllocationState{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.Name,
			Namespace: config.Namespace,
		},
	}

	if config.Owner != nil {
		object.OwnerReferences = []metav1.OwnerReference{*config.Owner}
	}

	mas := &MydeviceAllocationState{
		object,
		clientset,
	}

	return mas
}

func (g *MydeviceAllocationState) GetOrCreate() error {
	err := g.Get()
	if err == nil {
		return nil
	}
	if errors.IsNotFound(err) {
		return g.Create()
	}
	klog.Error("Could not get MydeviceAllocationState: %v", err)
	return err
}

func (g *MydeviceAllocationState) Create() error {
	mas := g.MydeviceAllocationState.DeepCopy()
	mas, err := g.clientset.DraV1alpha().MydeviceAllocationStates(g.Namespace).Create(context.TODO(), mas, metav1.CreateOptions{})
	if err != nil {
		return err
	}
	*g.MydeviceAllocationState = *mas
	return nil
}

func (g *MydeviceAllocationState) Delete() error {
	deletePolicy := metav1.DeletePropagationForeground
	deleteOptions := metav1.DeleteOptions{PropagationPolicy: &deletePolicy}
	err := g.clientset.DraV1alpha().MydeviceAllocationStates(g.Namespace).Delete(context.TODO(), g.MydeviceAllocationState.Name, deleteOptions)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

func (g *MydeviceAllocationState) Update(spec *mycrd.MydeviceAllocationStateSpec) error {
	mas := g.MydeviceAllocationState.DeepCopy()
	mas.Spec = *spec
	mas, err := g.clientset.DraV1alpha().MydeviceAllocationStates(g.Namespace).Update(context.TODO(), mas, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	*g.MydeviceAllocationState = *mas
	return nil
}

func (g *MydeviceAllocationState) UpdateStatus(status string) error {
	mas := g.MydeviceAllocationState.DeepCopy()
	mas.Status = status
	mas, err := g.clientset.DraV1alpha().MydeviceAllocationStates(g.Namespace).Update(context.TODO(), mas, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	*g.MydeviceAllocationState = *mas
	return nil
}

func (g *MydeviceAllocationState) Get() error {
	mas, err := g.clientset.DraV1alpha().MydeviceAllocationStates(g.Namespace).Get(context.TODO(), g.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	*g.MydeviceAllocationState = *mas
	return nil
}

func (g *MydeviceAllocationState) ListNames() ([]string, error) {
	masnames := []string{}

	mass, err := g.clientset.DraV1alpha().MydeviceAllocationStates(g.Namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return masnames, err
	}

	for _, mas := range mass.Items {
		masnames = append(masnames, mas.Name)
	}
	return masnames, nil
}

// Return list of Allocatable devices that are not yet allocated and are available
func (g *MydeviceAllocationState) Available() map[string]*mycrd.AllocatableMydevice {
	available := make(map[string]*mycrd.AllocatableMydevice)

	klog.V(5).Infof("MAS spec has %v allocatable devices, %v claimallocations", len(g.Spec.AllocatableMydevices), len(g.Spec.ResourceClaimAllocations))

	for _, device := range g.Spec.AllocatableMydevices {
		switch device.Type {
		case mycrd.MydeviceType0:
			// TODO: remove this check in case mydevice is freely shareable
			if g.DeviceIsAllocated(device.UID) {
				continue
			}

			available[device.UID] = &device
		default:
			klog.Warning("Unsupported device type: %v", string(device.Type))
		}
	}
	klog.V(3).Infof("Available %v devices: %v", len(available), available)

	return available
}

func (g *MydeviceAllocationState) DeviceIsAllocated(deviceUid string) bool {
	for _, claimAllocation := range g.Spec.ResourceClaimAllocations {
		for _, allocatedDevice := range claimAllocation {
			if allocatedDevice.UID == deviceUid {
				return true
			}
		}
	}
	return false
}

func (g *MydeviceAllocationState) MakeResourceClaimAllocation(claimUID string) {
	allocated := mycrd.AllocatedMydevices{}
	for _, device := range g.Spec.ResourceClaimRequests[claimUID].Mydevices {
		sourceDevice, _ := g.Spec.AllocatableMydevices[device.UID]
		// TODO: check if sourceDevice is found
		allocated = append(allocated, mycrd.AllocatedMydevice{
			CDIDevice: sourceDevice.CDIDevice,
			Type:      sourceDevice.Type,
			UID:       sourceDevice.UID,
		})
	}
	if g.Spec.ResourceClaimAllocations == nil {
		g.Spec.ResourceClaimAllocations = make(map[string]mycrd.AllocatedMydevices)
	}
	g.Spec.ResourceClaimAllocations[claimUID] = allocated
}
