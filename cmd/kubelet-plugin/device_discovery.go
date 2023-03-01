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
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	mycrd "github.com/kubernetes-sigs/dra-example-driver/pkg/crd/example/v1alpha/api"
	"k8s.io/klog/v2"
)

const (
	sysfsDrmDir  = "/sys/class/drm/"
	pciAddressRE = `[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`
	cardRE       = `^card[0-9]+$`
	renderdRE    = `^renderD[0-9]+$`
)

/* detect devices from sysfs drm directory (card id and renderD id) */
func enumerateAllPossibleDevices() map[string]*DeviceInfo {

	cardRegexp := regexp.MustCompile(cardRE)
	renderdRegexp := regexp.MustCompile(renderdRE)
	drmFiles, err := os.ReadDir(sysfsDrmDir)

	if err != nil {
		if err == os.ErrNotExist {
			klog.V(5).Infof("No DRM Mydevice devices found on this host. %v does not exist.", sysfsDrmDir)
		}
		klog.V(5).Infof("Resorting to deviceless / fake devices with environment variables only.")
		return fakeDevices()
	}

	klog.V(5).Infof("Found %d files in %v dir", len(drmFiles), sysfsDrmDir)

	devices := make(map[string]*DeviceInfo)

	for _, drmFile := range drmFiles {
		// check if file is pci device
		if !cardRegexp.MatchString(drmFile.Name()) {
			klog.V(5).Infof("Ignoring file %v", drmFile.Name())
			continue
		}
		klog.V(5).Infof("Found DRM card device: " + drmFile.Name())

		symlinkFile := filepath.Join(sysfsDrmDir, drmFile.Name())
		pciDevDrmCard, err := os.Readlink(symlinkFile)
		if err != nil {
			klog.V(5).Infof("Could not read device DRM symlink '%v', resorting to fake devices", symlinkFile)
			return fakeDevices()
		}

		drmDevDir := path.Join(sysfsDrmDir, pciDevDrmCard, "../")
		drmDevFiles, err := os.ReadDir(drmDevDir)
		if err != nil {
			klog.V(5).Infof("Could not read device DRM dir '%v', resorting to fake devices", drmDevDir)
			return fakeDevices()
		}

		cardDev := ""
		renderdDev := ""

		// typically cardX, renderDYYY, controlD64
		for _, drmDevFile := range drmDevFiles {
			if cardRegexp.Match([]byte(drmDevFile.Name())) {
				cardDev = drmDevFile.Name()
			} else if renderdRegexp.Match([]byte(drmDevFile.Name())) {
				renderdDev = drmDevFile.Name()
			}
		}

		// renderD can be absent, but cardX must be present
		if cardDev == "" {
			klog.Errorf("Could not find DRM card device, skipping this device")
			continue
		}

		klog.V(5).Infof("Found DRM device files (card / renderD): %v / %v", cardDev, renderdDev)

		// read device and vendor identities
		device_id_file := path.Join(drmDevDir, "../", "device")
		device_id_bytes, err := os.ReadFile(device_id_file)
		if err != nil {
			klog.Errorf("Failed reading device file (%s): %+v", device_id_file, err)
			continue
		}

		device_id := strings.TrimSpace(string(device_id_bytes))

		vendor_id_file := path.Join(drmDevDir, "../", "vendor")
		vendor_id_bytes, err := os.ReadFile(vendor_id_file)
		if err != nil {
			klog.Errorf("Failed reading vendor file (%s): %+v", vendor_id_file, err)
			continue
		}

		vendor_id := strings.TrimSpace(string(vendor_id_bytes))

		pciDBDF := filepath.Base(path.Join(drmDevDir, "../"))
		klog.V(5).Infof("Discovered device is on PCI address %v", pciDBDF)

		uid := fmt.Sprintf("%v-%v-%v", pciDBDF, vendor_id, device_id)
		klog.V(5).Infof("New Mydevice UID: %v", uid)

		newDeviceInfo := &DeviceInfo{
			uid:        uid,
			cdiname:    uid,
			card:       cardDev,
			renderd:    renderdDev,
			deviceType: mycrd.MydeviceType0,
		}
		klog.V(5).Infof("cdiname: %v", newDeviceInfo.cdiname)

		devices[newDeviceInfo.uid] = newDeviceInfo

	}
	return devices
}

// Generate five fake devices
func fakeDevices() map[string]*DeviceInfo {
	devices := make(map[string]*DeviceInfo)

	for idx := 0; idx < 5; idx++ {

		uid := fmt.Sprintf("fakeDevice%02d", idx)
		klog.V(5).Infof("New Mydevice UID: %v", uid)
		newDeviceInfo := &DeviceInfo{
			uid:        uid,
			cdiname:    uid,
			deviceType: mycrd.MydeviceType0,
			card:       "",
			renderd:    "",
		}
		devices[newDeviceInfo.uid] = newDeviceInfo
	}

	return devices
}
