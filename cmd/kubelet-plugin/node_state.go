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
	"fmt"
	"path/filepath"
	"sync"

	cdiapi "github.com/container-orchestrated-devices/container-device-interface/pkg/cdi"
	specs "github.com/container-orchestrated-devices/container-device-interface/specs-go"
	"github.com/kubernetes-sigs/dra-example-driver/pkg/crd/example/v1alpha"
	mycrd "github.com/kubernetes-sigs/dra-example-driver/pkg/crd/example/v1alpha/api"
	"k8s.io/klog/v2"
)

type DeviceInfo struct {
	uid        string // Unique identifier, for instance PCI_DBDF-PCI_DEVICE_ID
	cdiname    string // name field from cdi spec, uid if handled by this resource-driver
	deviceType string // in case several different device types are supported
	card       string // card DRM device file name, can be empty if devices are faked
	renderd    string // renderd DRM device file name, can be empty
}

func (g *DeviceInfo) DeepCopy() *DeviceInfo {
	return &DeviceInfo{
		uid:        g.uid,
		cdiname:    g.cdiname,
		deviceType: g.deviceType,
		card:       g.card,
		renderd:    g.renderd,
	}
}

type DevicesInfo map[string]*DeviceInfo

func (g *DevicesInfo) DeepCopy() DevicesInfo {
	devicesInfoCopy := DevicesInfo{}
	for duid, device := range *g {
		devicesInfoCopy[duid] = device.DeepCopy()
	}
	return devicesInfoCopy
}

type ClaimAllocations map[string][]*DeviceInfo

type nodeState struct {
	sync.Mutex
	cdi         cdiapi.Registry
	allocatable map[string]*DeviceInfo
	allocations ClaimAllocations
}

func (g DeviceInfo) CDIDevice() string {
	return fmt.Sprintf("%s=%s", cdiKind, g.cdiname)
}

func newNodeState(mas *mycrd.MydeviceAllocationState) (*nodeState, error) {
	klog.V(3).Infof("Enumerating all devices")
	detecteddevices := enumerateAllPossibleDevices()

	klog.V(5).Infof("Detected %d devices", len(detecteddevices))

	for ddev := range detecteddevices {
		klog.V(3).Infof("new device: %+v", ddev)
	}

	klog.V(5).Infof("Getting CDI registry")
	cdi := cdiapi.GetRegistry(
		cdiapi.WithSpecDirs(cdiRoot),
	)

	klog.V(5).Infof("Got CDI registry, refreshing it")
	err := cdi.Refresh()
	if err != nil {
		return nil, fmt.Errorf("unable to refresh the CDI registry: %v", err)
	}

	// syncDetectedDevicesWithCdiRegistry overrides uid in detecteddevices from existing cdi spec
	err = syncDetectedDevicesWithCdiRegistry(cdi, detecteddevices)
	if err != nil {
		return nil, fmt.Errorf("unable to sync detected devices to CDI registry: %v", err)
	}
	err = cdi.Refresh()
	if err != nil {
		return nil, fmt.Errorf("unable to refresh the CDI registry after populating it: %v", err)
	}

	for duid, ddev := range detecteddevices {
		klog.V(3).Infof("Allocatable after CDI refresh device: %v : %+v", duid, ddev)
	}

	klog.V(5).Infof("Creating NodeState")
	// TODO: allocatable should include cdi-described
	state := &nodeState{
		cdi:         cdi,
		allocatable: detecteddevices,
		allocations: make(ClaimAllocations),
	}

	klog.V(5).Infof("Syncing allocatable devices")
	err = state.syncAllocatedDevicesFromMASSpec(&mas.Spec)
	if err != nil {
		return nil, fmt.Errorf("unable to sync allocated devices from CRD: %v", err)
	}
	klog.V(5).Infof("Synced state with CDI and CRD: %+v", state)
	for duid, ddev := range state.allocatable {
		klog.V(5).Infof("Allocatable device: %v : %+v", duid, ddev)
	}

	return state, nil
}

// Add detected devices into cdi registry if they are not yet there.
// Update existing registry devices with detected.
// Remove absent registry devices
func syncDetectedDevicesWithCdiRegistry(registry cdiapi.Registry, detectedDevices DevicesInfo) error {

	vendorSpecs := registry.SpecDB().GetVendorSpecs(cdiVendor)
	devicesToAdd := detectedDevices.DeepCopy()

	if len(vendorSpecs) != 0 {
		// loop through spec devices
		// - remove from CDI those not detected
		// - delete from detected so they are not added as duplicates
		// - write spec
		// add rest of detected devices to first vendor spec
		for specidx, vendorSpec := range vendorSpecs {
			klog.V(5).Infof("checking vendorspec %v", specidx)

			specChanged := false // if devices were updated or deleted
			filteredDevices := []specs.Device{}

			for specDeviceIdx, specDevice := range vendorSpec.Devices {
				klog.V(5).Infof("checking device %v: %v", specDeviceIdx, specDevice)

				if _, found := devicesToAdd[specDevice.Name]; found {
					filteredDevices = append(filteredDevices, specDevice)
					delete(devicesToAdd, specDevice.Name)
				} else {
					// skip CDI devices that were not detected
					klog.V(5).Infof("Removing device %v from CDI registry", specDevice.Name)
					specChanged = true
					continue
				}
			}
			// update spec if it was changed
			if specChanged {
				vendorSpec.Spec.Devices = filteredDevices
				specName := filepath.Base(vendorSpec.GetPath())
				klog.V(5).Infof("Overwriting spec %v", specName)
				err := registry.SpecDB().WriteSpec(vendorSpec.Spec, specName)
				if err != nil {
					klog.Errorf("Failed writing CDI spec %v: %v", vendorSpec.GetPath(), err)
					return fmt.Errorf("Failed writing CDI spec %v: %v", vendorSpec.GetPath(), err)
				}
			}
		}

		if len(devicesToAdd) > 0 {
			// add devices that were not found in registry to the first existing vendor spec
			apispec := vendorSpecs[0]
			klog.V(5).Infof("Adding %d devices to CDI spec", len(devicesToAdd))
			addDevicesToCDISpec(devicesToAdd, apispec.Spec)
			specName := filepath.Base(apispec.GetPath())
			klog.V(5).Infof("Overwriting spec %v", specName)
			err := registry.SpecDB().WriteSpec(apispec.Spec, specName)
			if err != nil {
				klog.Errorf("Failed writing CDI spec %v: %v", apispec.GetPath(), err)
				return err
			}
		}
	} else {
		klog.V(5).Info("Creating new CDI spec for detected devices")
		if err := addNewDevicesToNewRegistry(devicesToAdd); err != nil {
			klog.V(5).Infof("Failed adding devices to new CDI registry: %v", err)
			return err
		}
	}

	return nil
}

func addDevicesToCDISpec(devices DevicesInfo, spec *specs.Spec) {
	for _, device := range devices {
		deviceNodes := []*specs.DeviceNode{}
		if device.card != "" {
			deviceNodes = append(deviceNodes, &specs.DeviceNode{Path: fmt.Sprintf("/dev/dri/%s", device.card), Type: "c"})
		}
		if device.renderd != "" {
			deviceNodes = append(deviceNodes, &specs.DeviceNode{Path: fmt.Sprintf("/dev/dri/%s", device.renderd), Type: "c"})
		}
		newDevice := specs.Device{
			Name: device.cdiname,
			ContainerEdits: specs.ContainerEdits{
				DeviceNodes: deviceNodes,
				Env:         []string{"ENV_A=value1", "ENV_B=value2"},
			},
		}

		spec.Devices = append(spec.Devices, newDevice)
	}
}

// Write devices into new vendor-specific CDI spec, should only be called if such spec does not exist
func addNewDevicesToNewRegistry(devices DevicesInfo) error {
	klog.V(5).Infof("Adding %v devices to new spec", len(devices))
	registry := cdiapi.GetRegistry()
	spec := &specs.Spec{
		Version: cdiVersion,
		Kind:    cdiKind,
	}

	addDevicesToCDISpec(devices, spec)
	klog.V(5).Infof("spec devices length: %v", len(spec.Devices))

	specname, err := cdiapi.GenerateNameForSpec(spec)
	if err != nil {
		return fmt.Errorf("Failed to generate name for cdi device spec: %+v", err)
	}

	return registry.SpecDB().WriteSpec(spec, specname)
}

func (s *nodeState) free(claimUid string) error {
	s.Lock()
	defer s.Unlock()

	if s.allocations[claimUid] == nil {
		return nil
	}

	for _, device := range s.allocations[claimUid] {
		var err error
		switch device.deviceType {
		case mycrd.MydeviceType0:
			klog.V(5).Info("Freeing Type0, nothing to do")
		default:
			klog.Errorf("Unsupported device type: %v", device.deviceType)
			err = fmt.Errorf("unsupported device type: %v", device.deviceType)
		}
		if err != nil {
			return fmt.Errorf("free failed: %v", err)
		}
	}

	delete(s.allocations, claimUid)
	return nil
}

func (s *nodeState) getUpdatedSpec(inspec *mycrd.MydeviceAllocationStateSpec) *mycrd.MydeviceAllocationStateSpec {
	s.Lock()
	defer s.Unlock()

	outspec := inspec.DeepCopy()
	s.syncAllocatableDevicesToMASSpec(outspec)
	s.syncAllocatedDevicesToMASSpec(outspec)
	return outspec
}

func (s *nodeState) getAllocatedAsCDIDevices(claimUid string) []string {
	var devs []string
	klog.V(5).Infof("getAllocatedAsCDIDevices is called")
	for _, device := range s.allocations[claimUid] {
		cdidev := s.cdi.DeviceDB().GetDevice(device.CDIDevice())
		if cdidev == nil {
			klog.Errorf("Device %v from claim %v not found in cdi DB", device.uid, claimUid)
			return []string{}
		}
		klog.V(5).Infof("Found cdi device %v", cdidev.GetQualifiedName())
		devs = append(devs, cdidev.GetQualifiedName())
	}
	return devs
}

func (s *nodeState) syncAllocatableDevicesToMASSpec(spec *mycrd.MydeviceAllocationStateSpec) {
	devices := make(map[string]mycrd.AllocatableMydevice)
	for _, device := range s.allocatable {
		devices[device.uid] = mycrd.AllocatableMydevice{
			CDIDevice: device.cdiname,
			Type:      v1alpha.MydeviceType(device.deviceType),
			UID:       device.uid,
		}
	}

	spec.AllocatableMydevices = devices
}

func (s *nodeState) syncAllocatedDevicesFromMASSpec(spec *mycrd.MydeviceAllocationStateSpec) error {
	klog.V(5).Infof("Syncing %d resource claim allocations from MAS to internal state", len(spec.ResourceClaimAllocations))
	if s.allocations == nil {
		s.allocations = make(ClaimAllocations)
	}

	for claimUid, devices := range spec.ResourceClaimAllocations {
		klog.V(5).Infof("claim %v has %v devices", claimUid, len(devices))
		s.allocations[claimUid] = []*DeviceInfo{}
		for _, d := range devices {
			klog.V(5).Infof("Device: %+v", d)
			switch d.Type {
			case mycrd.MydeviceType0:
				klog.V(5).Info("Matched MydeviceType0 type in sync")
				if _, exists := s.allocatable[d.UID]; !exists {
					klog.Errorf("Allocated device %v no longer available for claim %v", d.UID, claimUid)
					// TODO: handle this better: wipe resource claim allocation if claimAllocation does not exist anymore
					return fmt.Errorf("Could not find allocated device %v for claimAllocation %v", d.UID, claimUid)
				}
				newdevice := s.allocatable[d.UID].DeepCopy()
				s.allocations[claimUid] = append(s.allocations[claimUid], newdevice)
			default:
				klog.Errorf("Unsupported device type: %v", d.Type)
			}
		}
	}

	return nil
}

func (s *nodeState) syncAllocatedDevicesToMASSpec(masspec *mycrd.MydeviceAllocationStateSpec) {
	outrcas := make(map[string]mycrd.AllocatedMydevices)
	for claimUid, devices := range s.allocations {
		allocatedDevices := mycrd.AllocatedMydevices{}
		for _, device := range devices {
			switch device.deviceType {
			case mycrd.MydeviceType0:
				outdevice := mycrd.AllocatedMydevice{
					UID:       device.uid,
					CDIDevice: device.CDIDevice(),
					Type:      v1alpha.MydeviceType(device.deviceType),
				}
				allocatedDevices = append(allocatedDevices, outdevice)
			default:
				klog.Errorf("Unsupported device type: %v", device.deviceType)
			}

		}
		outrcas[claimUid] = allocatedDevices
	}
	masspec.ResourceClaimAllocations = outrcas
}

func (s *nodeState) announceNewDevices(newDevices DevicesInfo) error {
	klog.V(5).Infof("Refreshing CDI registry")
	err := s.cdi.Refresh()
	if err != nil {
		return fmt.Errorf("Unable to refresh the CDI registry: %v", err)
	}

	klog.V(5).Infof("Adding %v new devices to CDI", len(newDevices))
	err = syncDetectedDevicesWithCdiRegistry(s.cdi, newDevices)
	if err != nil {
		klog.Errorf("Failed announcing new devices: %v", err)
		return fmt.Errorf("Failed announcing new devices: %v", err)
	}

	// Adding new devices to s.allocatable is enough, getUpdatedSpec will be called in NodePrepareResource
	for duid, device := range newDevices {
		s.allocatable[duid] = device
	}

	return nil
}

func (s *nodeState) unannounceDevices(deviceUid string) error {
	klog.V(5).Infof("unannounceDevices called for parentUid: %v", deviceUid)
	// MAS spec will beb updated with s.allocatable in NodeUnprepareResource call to getUpdatedSpec
	for _, availDev := range s.allocatable {
		if availDev.uid == deviceUid {
			delete(s.allocatable, availDev.uid)
		}
	}

	// remove from CDI registry
	klog.V(5).Infof("Refreshing CDI registry")
	err := s.cdi.Refresh()
	if err != nil {
		return fmt.Errorf("Unable to refresh the CDI registry: %v", err)
	}

	for _, spec := range s.cdi.SpecDB().GetVendorSpecs(cdiVendor) {
		klog.V(5).Infof("Checking for devices in CDI spec: %+v", spec)

		filteredDevices := []specs.Device{}
		for _, device := range spec.Spec.Devices {
			if device.Name == deviceUid {
				klog.V(5).Infof("Found matching device: %v", device.Name)
				continue
			}
			filteredDevices = append(filteredDevices, device)
		}
		if len(filteredDevices) < len(spec.Spec.Devices) {
			spec.Spec.Devices = filteredDevices
			klog.V(5).Info("Overwriting spec")
			specName := filepath.Base(spec.GetPath())
			err = s.cdi.SpecDB().WriteSpec(spec.Spec, specName)
			if err != nil {
				klog.Errorf("Failed writing CDI spec %v: %v", spec.GetPath(), err)
			}
		}
	}

	return nil
}
