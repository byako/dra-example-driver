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

package main

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	resourcev1alpha1 "k8s.io/api/resource/v1alpha1"
	"k8s.io/dynamic-resource-allocation/controller"
	"k8s.io/klog/v2"

	myclientset "github.com/kubernetes-sigs/dra-example-driver/pkg/crd/example/clientset/versioned"
	mycrd "github.com/kubernetes-sigs/dra-example-driver/pkg/crd/example/v1alpha/api"
	driverVersion "github.com/kubernetes-sigs/dra-example-driver/pkg/version"
)

const (
	apiGroupVersion = mycrd.ApiGroupName + "/" + mycrd.ApiVersion
	minMemory       = 8
)

type driver struct {
	lock                 *PerNodeMutex
	namespace            string
	clientset            myclientset.Interface
	PendingClaimRequests *PerNodeClaimRequests
}

type onSuccessCallback func()

var _ controller.Driver = (*driver)(nil)

func newDriver(config *config_t) *driver {
	klog.V(5).Infof("Creating new driver")

	driverVersion.PrintDriverVersion()

	return &driver{
		lock:                 NewPerNodeMutex(),
		namespace:            config.namespace,
		clientset:            config.clientset.example,
		PendingClaimRequests: NewPerNodeClaimRequests(),
	}
}

func (d driver) GetClassParameters(ctx context.Context, class *resourcev1alpha1.ResourceClass) (interface{}, error) {
	klog.V(5).InfoS("GetClassParameters called", "resource class", class.Name)

	if class.ParametersRef == nil {
		return mycrd.DefaultDeviceClassParametersSpec(), nil
	}

	if class.ParametersRef.APIGroup != apiGroupVersion {
		return nil, fmt.Errorf(
			"incorrect resource-class API group and version: %v, expected: %v",
			class.ParametersRef.APIGroup,
			apiGroupVersion)
	}

	dc, err := d.clientset.DraV1alpha().MydeviceClassParameters().Get(ctx, class.ParametersRef.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get DeviceClassParameters '%v': %v", class.ParametersRef.Name, err)
	}

	return &dc.Spec, nil
}

func (d driver) GetClaimParameters(ctx context.Context, claim *resourcev1alpha1.ResourceClaim, class *resourcev1alpha1.ResourceClass, classParameters interface{}) (interface{}, error) {
	klog.V(5).InfoS("GetClaimParameters called", "resource claim", claim.Namespace+"/"+claim.Name)
	if claim.Spec.ParametersRef == nil {
		return mycrd.DefaultMydeviceClaimParametersSpec(), nil
	}

	if claim.Spec.ParametersRef.APIGroup != apiGroupVersion {
		return nil, fmt.Errorf(
			"incorrect claim spec parameter API group and version: %v, expected: %v",
			claim.Spec.ParametersRef.APIGroup,
			apiGroupVersion)
	}

	if mycrd.MydeviceClaimParametersKind != claim.Spec.ParametersRef.Kind {
		klog.Error("Unsupported ResourceClaimParametersRef Kind: %v", claim.Spec.ParametersRef.Kind)
		return nil, fmt.Errorf("Unsupported ResourceClaim.ParametersRef Kind: %v", claim.Spec.ParametersRef.Kind)

	}

	gcp, err := d.clientset.DraV1alpha().MydeviceClaimParameters(claim.Namespace).Get(ctx, claim.Spec.ParametersRef.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get MydeviceClaimParameters '%v' in namespace '%v': %v", claim.Spec.ParametersRef.Name, claim.Namespace, err)
	}

	err = validateMydeviceClaimParameters(&gcp.Spec)
	if err != nil {
		return nil, fmt.Errorf("could not validate MydeviceClaimParameters '%v' in namespace '%v': %v", claim.Spec.ParametersRef.Name, claim.Namespace, err)
	}

	return &gcp.Spec, nil
}

// Sanitize resource request parameters.
func validateMydeviceClaimParameters(claimParams *mycrd.MydeviceClaimParametersSpec) error {
	klog.V(5).Infof("validateMydeviceClaimParameters called")

	// Count is mandatory, its value is checked in CRD / OpenAPI
	// Type is mandatory, its value is checked in CRD / OpenAPI

	return nil
}

func (d driver) Allocate(
	ctx context.Context,
	claim *resourcev1alpha1.ResourceClaim,
	claimParameters interface{},
	class *resourcev1alpha1.ResourceClass,
	classParameters interface{},
	selectedNode string) (*resourcev1alpha1.AllocationResult, error) {
	klog.V(5).InfoS("Allocate called", "resource claim", claim.Namespace+"/"+claim.Name, "selectedNode", selectedNode)

	// immediate allocation with no pendingResourceClaims
	if selectedNode == "" {
		return d.allocateImmediateClaim(claim, claimParameters, class, classParameters)
	}

	return d.allocatePendingClaim(claim, claimParameters, selectedNode)
}

func (d driver) allocateImmediateClaim(
	claim *resourcev1alpha1.ResourceClaim,
	claimParameters interface{},
	class *resourcev1alpha1.ResourceClass,
	classParameters interface{},
) (*resourcev1alpha1.AllocationResult, error) {
	klog.V(5).Infof("Allocating immediately")

	crdconfig := &mycrd.MydeviceAllocationStateConfig{
		Namespace: d.namespace,
	}

	mas := mycrd.NewMydeviceAllocationState(crdconfig, d.clientset)
	masnames, err := mas.ListNames()
	if err != nil {
		return nil, fmt.Errorf("error retrieving list of MydeviceAllocationState objects: %v", err)
	}

	// create claimAllocation
	ca := controller.ClaimAllocation{
		Claim:           claim,
		ClaimParameters: claimParameters,
		Class:           class,
		ClassParameters: classParameters,
	}
	cas := []*controller.ClaimAllocation{&ca}

	for _, nodename := range masnames {
		d.lock.Get(nodename).Lock()

		crdconfig.Name = nodename

		klog.V(5).Infof("Fetching MAS item: %v", nodename)
		mas := mycrd.NewMydeviceAllocationState(crdconfig, d.clientset)

		err := mas.Get()
		if err != nil {
			d.lock.Get(nodename).Unlock()
			klog.Errorf("error retrieving MAS CRD for node %v: %v", nodename, err)
			continue
		}

		allocated := d.selectPotentialDevices(mas, cas)
		klog.V(5).Infof("Allocated: %v", allocated)

		claimUID := string(claim.UID)
		claimParamsSpec := claimParameters.(*mycrd.MydeviceClaimParametersSpec)

		if claimParamsSpec.Count != len(allocated[claimUID].Mydevices) {
			d.lock.Get(nodename).Unlock()
			klog.V(3).Infof("Requested amount does not match allocated, skipping node %v", nodename)
			continue // next node
		}

		klog.V(5).Infof("Allocated as much as requested, processing devices")

		if mas.Spec.ResourceClaimRequests == nil {
			mas.Spec.ResourceClaimRequests = make(map[string]mycrd.RequestedMydevices)
		}
		mas.Spec.ResourceClaimRequests[claimUID] = allocated[claimUID]

		mas.MakeResourceClaimAllocation(claimUID)

		err = mas.Update(&mas.Spec)
		if err != nil {
			d.lock.Get(nodename).Unlock()
			klog.Error("Could not update MydeviceAllocationState %v. Error: %+v", mas.Name, err)
			return nil, fmt.Errorf("error updating MydeviceAllocationState CRD: %v", err)
		}

		d.lock.Get(nodename).Unlock()

		// first successfull allocation should suffice
		return buildAllocationResult(nodename, true), nil
	}

	klog.V(3).InfoS("Could not immediately allocate", "resource claim", claim.Namespace+"/"+claim.Name)
	return nil, fmt.Errorf("no suitable node found")
}

func (d driver) allocatePendingClaim(
	claim *resourcev1alpha1.ResourceClaim,
	claimParameters interface{},
	nodename string) (*resourcev1alpha1.AllocationResult, error) {
	if _, ok := claimParameters.(*mycrd.MydeviceClaimParametersSpec); !ok {
		return nil, fmt.Errorf("Unknown ResourceClaim.ParametersRef.Kind: %v", claim.Spec.ParametersRef.Kind)
	}

	d.lock.Get(nodename).Lock()
	defer d.lock.Get(nodename).Unlock()

	crdconfig := &mycrd.MydeviceAllocationStateConfig{
		Name:      nodename,
		Namespace: d.namespace,
	}

	claimUID := string(claim.UID)

	mas := mycrd.NewMydeviceAllocationState(crdconfig, d.clientset)
	err := mas.Get()
	if err != nil {
		return nil, fmt.Errorf("Error retrieving MAS CRD for node %v: %v", nodename, err)
	}

	if mas.Status != mycrd.MydeviceAllocationStateStatusReady {
		return nil, fmt.Errorf("MydeviceAllocationStateStatus: %v", mas.Status)
	}

	if mas.Spec.ResourceClaimRequests == nil {
		mas.Spec.ResourceClaimRequests = make(map[string]mycrd.RequestedMydevices)
	} else if _, exists := mas.Spec.ResourceClaimAllocations[claimUID]; exists {
		klog.V(5).Infof("MAS already has ResourceClaimAllocation %v, building allocation result", claimUID)
		return buildAllocationResult(nodename, true), nil
	}

	if claim.Spec.AllocationMode != resourcev1alpha1.AllocationModeImmediate && !d.PendingClaimRequests.Exists(claimUID, nodename) {
		return nil, fmt.Errorf("No allocation requests generated for claim '%v' on node '%v' yet", claimUID, nodename)
	}

	var onSuccess onSuccessCallback

	// validate that there is still resource for it
	enoughResource := d.enoughResourcesForPendingClaim(mas, claimUID, nodename)
	if enoughResource {
		klog.V(5).Infof("Enough resources. Setting MAS ClaimRequest %v", claimUID)

		mas.Spec.ResourceClaimRequests[claimUID] = d.PendingClaimRequests.Get(claimUID, nodename)
		mas.MakeResourceClaimAllocation(claimUID)
		onSuccess = func() {
			d.PendingClaimRequests.Remove(claimUID)
		}
	} else {
		klog.V(5).Infof("Insufficient resource for claim %v on allocation", claimUID)
		return nil, fmt.Errorf("Unable to allocate devices on node '%v': Insufficient resources", nodename)
	}

	err = mas.Update(&mas.Spec)
	if err != nil {
		return nil, fmt.Errorf("Error updating MydeviceAllocationState CRD: %v", err)
	}

	onSuccess()

	return buildAllocationResult(nodename, true), nil
}

func (d driver) Deallocate(ctx context.Context, claim *resourcev1alpha1.ResourceClaim) error {
	klog.V(5).InfoS("Deallocate called", "resource claim", claim.Namespace+"/"+claim.Name)

	selectedNode := getSelectedNode(claim)
	if selectedNode == "" {
		return nil
	}

	d.lock.Get(selectedNode).Lock()
	defer d.lock.Get(selectedNode).Unlock()

	crdconfig := &mycrd.MydeviceAllocationStateConfig{
		Name:      selectedNode,
		Namespace: d.namespace,
	}

	mas := mycrd.NewMydeviceAllocationState(crdconfig, d.clientset)
	err := mas.Get()
	if err != nil {
		return fmt.Errorf("error retrieving MAS CRD for node %v: %v", selectedNode, err)
	}

	claimUID := string(claim.UID)
	devices, exists := mas.Spec.ResourceClaimRequests[claimUID]
	if !exists {
		klog.Warning("Resource claim %v does not exist internally in resource driver")
		return nil
	}
	switch devices.Spec.Type {
	case mycrd.MydeviceType0:
		d.PendingClaimRequests.Remove(claimUID)
	default:
		klog.Errorf("Unknown requested devices type: %v", devices.Spec.Type)
		err = fmt.Errorf("unknown requested device type: %v", devices.Spec.Type)
	}
	if err != nil {
		return fmt.Errorf("unable to deallocate devices '%v': %v", devices, err)
	}

	if mas.Spec.ResourceClaimRequests != nil {
		delete(mas.Spec.ResourceClaimRequests, claimUID)
	}
	if mas.Spec.ResourceClaimAllocations != nil {
		delete(mas.Spec.ResourceClaimAllocations, claimUID)
	}

	err = mas.Update(&mas.Spec)
	if err != nil {
		return fmt.Errorf("error updating MydeviceAllocationState CRD: %v", err)
	}
	return nil
}

// Unsuitable nodes call chain
// mark nodes that do not suit request into .UnsuitableNodes and populate d.PendingClaimAllocations
func (d driver) UnsuitableNodes(ctx context.Context, pod *corev1.Pod, cas []*controller.ClaimAllocation, potentialNodes []string) error {
	klog.V(5).InfoS("UnsuitableNodes called", "cas length", len(cas))

	for _, node := range potentialNodes {
		klog.V(5).InfoS("UnsuitableNodes processing", "node", node)
		err := d.unsuitableNode(cas, node)
		if err != nil {
			return fmt.Errorf("error checking if node '%v' is unsuitable: %v", node, err)
		}
	}

	// remove duplicates from UnsuitableNodes
	for _, claimallocation := range cas {
		claimallocation.UnsuitableNodes = unique(claimallocation.UnsuitableNodes)
	}
	return nil
}

func (d driver) unsuitableNode(allcas []*controller.ClaimAllocation, potentialNode string) error {
	d.lock.Get(potentialNode).Lock()
	defer d.lock.Get(potentialNode).Unlock()

	crdconfig := &mycrd.MydeviceAllocationStateConfig{
		Name:      potentialNode,
		Namespace: d.namespace,
	}

	mas := mycrd.NewMydeviceAllocationState(crdconfig, d.clientset)
	klog.V(5).InfoS("Getting MydeviceAllocationState", "node", potentialNode, "namespace", d.namespace)
	err := mas.Get()
	if err != nil || mas.Status != mycrd.MydeviceAllocationStateStatusReady {
		klog.V(3).Infof("Could not get allocation state %v or it is not ready", potentialNode)
		for _, ca := range allcas {
			klog.V(5).Infof("Adding node %v to unsuitable nodes for CA %v", potentialNode, ca)
			ca.UnsuitableNodes = append(ca.UnsuitableNodes, potentialNode)
		}
		return nil
	}

	klog.V(5).Infof("MAS status OK")

	if mas.Spec.ResourceClaimRequests == nil {
		klog.V(5).Infof("Creating blank map for claim requests")
		mas.Spec.ResourceClaimRequests = make(map[string]mycrd.RequestedMydevices)
	}

	filteredCAs := []*controller.ClaimAllocation{}

	for _, ca := range allcas {
		if _, ok := ca.ClaimParameters.(*mycrd.MydeviceClaimParametersSpec); ok {
			filteredCAs = append(filteredCAs, ca)
		} else {
			klog.V(3).Infof("Unsupported claim parameters type %T", ca.ClaimParameters)
		}
	}

	err = d.unsuitableMydeviceNode(mas, filteredCAs, allcas)
	if err != nil {
		return fmt.Errorf("error processing '%v': %v", mycrd.MydeviceClaimParametersKind, err)
	}

	return nil
}

func (d *driver) unsuitableMydeviceNode(
	mas *mycrd.MydeviceAllocationState,
	mcas []*controller.ClaimAllocation,
	allcas []*controller.ClaimAllocation) error {
	klog.V(5).Infof("unsuitableMydeviceNode called")

	// remove pending claim requests that are in CRD already
	// Add pending claim requests to CRD
	d.PendingClaimRequests.CleanupNode(mas)
	allocated := d.selectPotentialDevices(mas, mcas)
	klog.V(5).Infof("Allocated: %v", allocated)
	for _, ca := range mcas {
		claimUID := string(ca.Claim.UID)
		claimParamsSpec := ca.ClaimParameters.(*mycrd.MydeviceClaimParametersSpec)

		if claimParamsSpec.Count != len(allocated[claimUID].Mydevices) {
			klog.V(3).Infof("Requested number of devices does not match allocated, skipping node")
			for _, ca := range allcas {
				ca.UnsuitableNodes = append(ca.UnsuitableNodes, mas.Name)
			}
			return nil
		}

		klog.V(5).Infof("Allocated as many devices as requested, processing devices")

		d.PendingClaimRequests.Set(claimUID, mas.Name, allocated[claimUID])
		mas.Spec.ResourceClaimRequests[claimUID] = allocated[claimUID]
	}
	klog.V(5).Info("Leaving unsuitableMydeviceNode")
	return nil
}

// Allocate Mydevices out of available for all claim allocations or fail
func (d *driver) selectPotentialDevices(
	mas *mycrd.MydeviceAllocationState,
	mcas []*controller.ClaimAllocation) map[string]mycrd.RequestedMydevices {
	klog.V(5).Infof("selectPotentialDevices called")

	available := mas.Available()
	newlyAllocated := make(map[string]mycrd.RequestedMydevices)

	for _, ca := range mcas {
		claimUID := string(ca.Claim.UID)
		claimParamsSpec := ca.ClaimParameters.(*mycrd.MydeviceClaimParametersSpec)

		// recalculating is cheaper than rescheduling, always recalculate or validate
		if _, exists := mas.Spec.ResourceClaimRequests[claimUID]; exists {
			klog.V(5).Infof("Found existing MAS ClaimRequest, validating")

			reusePending := true
			for _, allocatedDevice := range mas.Spec.ResourceClaimRequests[claimUID].Mydevices {
				if _, exists := available[allocatedDevice.UID]; exists {
					// TODO: if mydevice is shareable - do not remove from available
					delete(available, allocatedDevice.UID)
				} else {
					reusePending = false
					break
				}
			}

			if reusePending {
				klog.V(5).Infof("Reusing pending ClaimRequest allocation %v", claimUID)
				newlyAllocated[claimUID] = mycrd.RequestedMydevices{
					Spec:      *claimParamsSpec,
					Mydevices: mas.Spec.ResourceClaimRequests[claimUID].Mydevices,
				}
				continue
			}
		}

		var devices []mycrd.RequestedMydevice
		for i := 0; i < claimParamsSpec.Count; i++ {
			for _, device := range available {
				if device.Type == mycrd.MydeviceType0 {
					device := mycrd.RequestedMydevice{
						UID: device.UID,
					}
					devices = append(devices, device)
					// TODO: if mydevice is shareable - do not remove from available
					delete(available, device.UID)
					break
				}
			}
		}

		newlyAllocated[claimUID] = mycrd.RequestedMydevices{
			Spec:      *claimParamsSpec,
			Mydevices: devices,
		}
	}

	return newlyAllocated
}

// ensure claim still fits into available devices
func (d *driver) enoughResourcesForPendingClaim(
	mas *mycrd.MydeviceAllocationState,
	pendingClaimUID string,
	selectedNode string) bool {
	klog.V(5).Infof("enoughResourcesForPendingClaim called for claim %v", pendingClaimUID)

	pendingClaim := d.PendingClaimRequests.Get(pendingClaimUID, selectedNode)

	for _, device := range pendingClaim.Mydevices {
		if _, exists := mas.Available()[device.UID]; !exists {
			klog.Errorf("Device %v from pending claim %v is not available", device.UID, pendingClaimUID)
			return false
		}
	}

	return true
}

func buildAllocationResult(selectedNode string, shared bool) *resourcev1alpha1.AllocationResult {
	nodeSelector := &corev1.NodeSelector{
		NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{
				MatchFields: []corev1.NodeSelectorRequirement{
					{
						Key:      "metadata.name",
						Operator: "In",
						Values:   []string{selectedNode},
					},
				},
			},
		},
	}
	allocation := &resourcev1alpha1.AllocationResult{
		AvailableOnNodes: nodeSelector,
		// ResourceHandle:
	}
	return allocation
}

func getSelectedNode(claim *resourcev1alpha1.ResourceClaim) string {
	if claim.Status.Allocation == nil {
		return ""
	}

	if claim.Status.Allocation.AvailableOnNodes == nil {
		return ""
	}

	return claim.Status.Allocation.AvailableOnNodes.NodeSelectorTerms[0].MatchFields[0].Values[0]
}

func unique(s []string) []string {
	set := make(map[string]bool)
	var filtered []string
	for _, str := range s {
		if _, exists := set[str]; !exists {
			set[str] = true
			filtered = append(filtered, str)
		}
	}

	return filtered
}
