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
	"sync"

	mycrd "github.com/kubernetes-sigs/dra-example-driver/pkg/crd/example/v1alpha/api"
	"k8s.io/klog/v2"
)

/*
	PerNodeClaimRequests is a map {
		claim-uid : map {
			nodename : mycrd.RequestedMydevices{
							Spec    MydeviceClaimParametersSpec `json:"spec"`
							Devices []RequestedMydevice         `json:"devices"`
						}
			}
		}
	}
*/
type PerNodeClaimRequests struct {
	sync.RWMutex
	requests map[string]map[string]mycrd.RequestedMydevices
}

func NewPerNodeClaimRequests() *PerNodeClaimRequests {
	return &PerNodeClaimRequests{
		requests: make(map[string]map[string]mycrd.RequestedMydevices),
	}
}

func (p *PerNodeClaimRequests) Exists(claimUID, node string) bool {
	p.RLock()
	defer p.RUnlock()

	if _, exists := p.requests[claimUID]; !exists {
		return false
	}

	if _, exists := p.requests[claimUID][node]; !exists {
		return false
	}

	return true
}

func (p *PerNodeClaimRequests) Get(claimUID, node string) mycrd.RequestedMydevices {
	p.RLock()
	defer p.RUnlock()

	if !p.Exists(claimUID, node) {
		return mycrd.RequestedMydevices{}
	}
	return p.requests[claimUID][node]
}

func (p *PerNodeClaimRequests) CleanupNode(mas *mycrd.MydeviceAllocationState) {
	p.RLock()
	for claimUID := range p.requests {
		if request, exists := p.requests[claimUID][mas.Name]; exists {
			klog.V(5).Infof("Cleaning up resource requests for node %v", mas.Name)
			// cleanup processed claim requests
			if _, exists := mas.Spec.ResourceClaimRequests[claimUID]; exists {
				delete(p.requests, claimUID)
			} else {
				mas.Spec.ResourceClaimRequests[claimUID] = request
			}
		}
	}
	p.RUnlock()
}

func (p *PerNodeClaimRequests) Set(claimUID, node string, devices mycrd.RequestedMydevices) {
	p.Lock()
	defer p.Unlock()

	_, exists := p.requests[claimUID]
	if !exists {
		p.requests[claimUID] = make(map[string]mycrd.RequestedMydevices)
	}

	p.requests[claimUID][node] = devices
}

func (p *PerNodeClaimRequests) Remove(claimUID string) {
	p.Lock()
	defer p.Unlock()

	delete(p.requests, claimUID)
}
