#!/usr/bin/env bash

# Copyright 2023 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail


# ensure operating from the root of this git tree 
EXAMPLE_DRIVER_HACK=$(dirname "$(readlink -f "$0")")
EXAMPLE_DRIVER=$(dirname "$EXAMPLE_DRIVER_HACK")
cd "$EXAMPLE_DRIVER"

#GO111MODULE="off"
# If this fails like "Failed making a parser: unable to add directory", "No files for pkg", then
# remove previously installed dra-example-driver from $GOPATH/src/

API_VERSION=v1alpha
bash vendor/k8s.io/code-generator/generate-groups.sh \
  "all" \
  github.com/kubernetes-sigs/dra-example-driver/pkg/crd/example \
  github.com/kubernetes-sigs/dra-example-driver/pkg/crd \
  example:"$API_VERSION" \
  --go-header-file hack/boilerplate.go.txt \
  --output-base "./pkg/crd/"

# wipe old generated code and copy new instead
for modname in clientset informers listers; do
    rm -rf pkg/crd/example/$modname
    mv pkg/crd/github.com/kubernetes-sigs/dra-example-driver/pkg/crd/example/$modname pkg/crd/example/
done
rm -f pkg/crd/example/"$API_VERSION"/zz_generated.deepcopy.go
mv pkg/crd/github.com/kubernetes-sigs/dra-example-driver/pkg/crd/example/"$API_VERSION"/zz_generated.deepcopy.go pkg/crd/example/"$API_VERSION"/

# cleanup empty dir after moving sole subdir
rm -r pkg/crd/github.com

pushd pkg/
for filename in $(grep -ri Parameterses | awk '{print $1}' | sed 's/:.*//'| sort -u); do
    echo "Fixing parameterses in $filename"
    sed -i 's/Parameterses/Parameters/g' $filename;
    sed -i 's/parameterses/parameters/g' $filename;
done;
popd

