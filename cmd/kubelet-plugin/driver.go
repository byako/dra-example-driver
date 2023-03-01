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

	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	drapbv1 "k8s.io/kubelet/pkg/apis/dra/v1alpha1"

	mycrd "github.com/kubernetes-sigs/dra-example-driver/pkg/crd/example/v1alpha/api"
)

type driver struct {
	mas   *mycrd.MydeviceAllocationState
	state *nodeState
}

func NewDriver(config *config_t) (*driver, error) {
	mas := mycrd.NewMydeviceAllocationState(config.crdconfig, config.clientset.example)

	klog.V(3).Info("Creating new MydeviceAllocationState")
	err := mas.GetOrCreate()
	if err != nil {
		return nil, err
	}

	klog.V(3).Info("Creating new DeviceState")
	state, err := newNodeState(mas)
	if err != nil {
		return nil, err
	}

	klog.V(3).Info("Updating MydeviceAllocationState")
	err = mas.Update(state.getUpdatedSpec(&mas.Spec))
	if err != nil {
		return nil, err
	}

	klog.V(3).Info("Updating MydeviceAllocationState status")
	err = mas.UpdateStatus(mycrd.MydeviceAllocationStateStatusReady)
	if err != nil {
		return nil, err
	}

	d := &driver{
		mas:   mas,
		state: state,
	}
	klog.V(3).Info("Finished creating new driver")

	return d, nil
}

func (d *driver) NodePrepareResource(ctx context.Context, req *drapbv1.NodePrepareResourceRequest) (*drapbv1.NodePrepareResourceResponse, error) {
	klog.V(5).Infof("NodePrepareResource is called: request: %+v", req)

	var err error
	var cdinames []string
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err = d.mas.Get()
		if err != nil {
			return err
		}
		klog.V(5).Info("MAS get OK")

		err = d.state.syncAllocatedDevicesFromMASSpec(&d.mas.Spec)
		if err != nil {
			return err
		}

		// CDI devices names
		cdinames = d.state.getAllocatedAsCDIDevices(req.ClaimUid)
		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("error preparing resource: %v", err)
	}

	klog.V(3).Infof("Prepared devices for claim '%v': %s", req.ClaimUid, cdinames)
	return &drapbv1.NodePrepareResourceResponse{CdiDevices: cdinames}, nil
}

func (d *driver) NodeUnprepareResource(ctx context.Context, req *drapbv1.NodeUnprepareResourceRequest) (*drapbv1.NodeUnprepareResourceResponse, error) {
	klog.V(3).Infof("NodeUnprepareResource is called: request: %+v", req)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err := d.mas.Get()
		if err != nil {
			return fmt.Errorf("error freeing devices for claim '%v': %v", req.ClaimUid, err)
		}

		err = d.state.free(req.ClaimUid)
		if err != nil {
			return fmt.Errorf("error freeing devices for claim '%v': %v", req.ClaimUid, err)
		}

		err = d.mas.Update(d.state.getUpdatedSpec(&d.mas.Spec))
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("error unpreparing resource: %v", err)
	}

	klog.V(3).Infof("Freed devices for claim '%v'", req.ClaimUid)
	return &drapbv1.NodeUnprepareResourceResponse{}, nil
}

func (d *driver) provision(toProvision map[string][]*DeviceInfo) (DevicesInfo, error) {
	provisioned := DevicesInfo{}
	return provisioned, nil
}
