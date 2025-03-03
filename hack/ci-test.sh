#!/usr/bin/env bash

#  Licensed under the Apache License, Version 2.0 (the "License");
#  you may not use this file except in compliance with the License.
#  You may obtain a copy of the License at
#
#      http://www.apache.org/licenses/LICENSE-2.0
#
#  Unless required by applicable law or agreed to in writing, software
#  distributed under the License is distributed on an "AS IS" BASIS,
#  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
#  See the License for the specific language governing permissions and
#  limitations under the License.

set -e

go version

# This file is run by Prow during all presubmits

# Not included or existing by default in Prow
#  - This is part of an ongoing discussion present on the issue:
#    https://github.com/kubernetes/test-infra/issues/9469;
#  - Other projects that use Prow as the CI, e.g. kubernetes-sigs/controller-runtime,
#    https://github.com/kubernetes-sigs/controller-runtime/blob/master/hack/ci-check-everything.sh,
#    also have this custom configuration;
#  - In Prow, the GOPATH is set to /home/prow/go, whereas in
#    the Docker container is /go, which is the default one.
export PATH=$(go env GOPATH)/bin:$PATH
mkdir -p $(go env GOPATH)/bin

echo "Installing kubebuilder tools"
K8S_VERSION=1.26.1
curl -sSLo envtest-bins.tar.gz "https://storage.googleapis.com/kubebuilder-tools/kubebuilder-tools-${K8S_VERSION}-$(go env GOOS)-$(go env GOARCH).tar.gz"
mkdir /usr/local/kubebuilder
tar -C /usr/local/kubebuilder --strip-components=1 -zvxf envtest-bins.tar.gz

hack_dir=$(dirname ${BASH_SOURCE})
cd "$hack_dir/.."

echo "Running 'make check-fmt check-generate"
make check-fmt check-generate

echo "Running 'make test'"
make test
