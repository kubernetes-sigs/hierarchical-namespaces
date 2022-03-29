#!/bin/bash
#
# This script is used in both the postsubmit and periodic tests (see ci-test.sh
# in this directory for the presubmits, and the README file for information
# about where this is configured in Prow).
#
# This script is designed to be run in the krte image
# (https://github.com/kubernetes/test-infra/tree/master/images/krte). That image
# includes everything needed to run Docker-in-Docker, and also cleans up after
# itself. Note that Prow seems to use containerd (and if it doesn't yet, it will
# soon) so there's no Docker daemon running on nodes unless you use this image!
#
# The precise version of krte being used is specified in two places:
#
# * The Prow configs in test-infra (see README for links).
# * The 'prow-test' target in the Makefile in this directory.
#
# The test-infra directory is automatically updated to always use the most
# recent version of krte we're using. Please keep the Makefile in this direcotry
# in sync with the version checked into the Prow config. Also, when you do that,
# please upgrade to the latest version of Kind in this file too! Look down a few
# lines to see/update the Kind version.

set -euf -o pipefail

# Configure some vars differently depending on whether we want the HA version of
# HNC or not.
deploy_target="deploy"
repair_manifest="default.yaml"
variant="${1:-default}"
case $variant in

  default)
    echo "Using default variant"
    ;;

  ha)
    echo "Using HA variant"
    deploy_target="deploy-ha"
    repair_manifest="ha.yaml"
    ;;

  *)
    echo "Unknown variant \"${variant}\""
    exit 1
    ;;
esac

start_time="$(date -u +%s)"
echo
echo "Starting script at $(date +%Y-%m-%d\ %H:%M:%S)"

# Install and start Kind. It seems like krte cleans this up when the test is
# done.
echo
echo Installing and starting Kind...
go install sigs.k8s.io/kind@v0.12.0
kind create cluster --name hnc-e2e

echo
echo "Building and deploying HNC via \'make ${deploy_target}\'"
export HNC_REGISTRY=
KIND=hnc-e2e make ${deploy_target}

echo
echo "Building kubectl-hns"
KIND=hnc-e2e make kubectl

# The webhooks take about 30 load
echo
end_time="$(date -u +%s)"
elapsed="$(($end_time-$start_time))"
echo "Test setup took $elapsed seconds."
echo "Waiting 30s for HNC to be alive..."
sleep 10
echo "... waited 10s..."
sleep 10
echo "... waited 20s..."
sleep 10
echo "... done."

export HNC_REPAIR="${PWD}/manifests/${repair_manifest}"
echo
echo "Starting the tests at $(date +%Y-%m-%d\ %H:%M:%S) with HNC_REPAIR=${HNC_REPAIR}"
make test-e2e

echo
echo "Finished at $(date +%Y-%m-%d\ %H:%M:%S)"
end_time="$(date -u +%s)"
elapsed="$(($end_time-$start_time))"
echo "Script took $elapsed seconds"
