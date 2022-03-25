# This makefile is used to test the prow scripts locally

# This is a temp directory where we check out the repo.
#
# Note that this makefile does *not* attempt to clean up the temp dirs created
# above. I found some cases where I couldn't remove the files even after the
# container, images and volumes had all been nuked. So please remember to clean
# your /tmp dir from time to time, kids.
PROW_TMP := $(shell mktemp -d)

# The KRTE image to use (see prow-run-e2e.sh for information about why we use krte).
#
# Please keep this in sync with
# https://github.com/kubernetes/test-infra/tree/master/config/jobs/kubernetes-sigs/hierarchical-namespaces/hnc-e2e.yaml,
# which is kept up-to-date automatically by K8s build automation.
#
# Also, when you do this, please also upgrade the version of Kind we download
# in prow-run-e2e.sh in this directory.
KRTE_IMG ?= gcr.io/k8s-staging-test-infra/krte:v20220322-c5eee43783-master

# The variant of HNC to test; this can be default, ha, or omitted (defaults to
# "default"). See prow-run-e2e.sh for details.
HNC_VARIANT ?= ""

# The branch to check out of the HNC repo once it's been cloned. Note that
# changes to prow-run-e2e.sh do not need to be checked in (the prow-test
# target, below, copies it from this directory) but anything else does.
HNC_BRANCH ?= "master"

# The org hosting the repo. By default, pulls from the official repo, but changing this allows you to push your local
# changes to your own Github repo and then pull from that.
HNC_ORG ?= "kubernetes-sigs"

# The entire repo is cloned and checked out at the master branch, except for
# prow-run-e2e.sh which is copied from this directory so you can test local
# changes.
#
# The '-it' arg is needed to allow you to hit ctrl-c while the tests are still
# running.
prow-test: prow-clean
	@echo ~~~ Preparing to test prow-run-e2e.sh ~~~
	@echo
	@echo Cloning the repo
	git clone https://github.com/${HNC_ORG}/hierarchical-namespaces ${PROW_TMP}
	cd ${PROW_TMP} && git checkout ${HNC_BRANCH}
	@echo
	@echo Copying run-e2e-tests in this directory to the synced directory
	cp -p ./prow-run-e2e.sh ${PROW_TMP}/hack
	@echo
	@echo ~~~ Testing prow-run-e2e.sh in KRTE ~~~
	@echo
	docker run \
		-e DOCKER_IN_DOCKER_ENABLED=true \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v ${PROW_TMP}:/local-test-start-dir \
		-w /local-test-start-dir \
		--network="host" \
		-it \
		${KRTE_IMG} \
		/local-test-start-dir/hack/prow-run-e2e.sh ${HNC_VARIANT}

# After calling 'make prow-test', the Kind cluster might still be present on
# the *host* even though the Docker container has exited. This name is defined
# in prow-run-e2e.sh.
prow-clean:
	-kind delete cluster --name hnc-e2e
