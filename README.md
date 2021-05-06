# The Hierarchical Namespace Controller (HNC)

```bash
$ kubectl hns create my-service -n my-team
$ kubectl hns tree my-team
my-team
└── my-service
```

Hierarchical namespaces make it easier to share your cluster by making
namespaces more powerful. For example, you can create additional namespaces
under your team's namespace, even if you don't have cluster-level permission to
create namespaces, and easily apply policies like RBAC and Network Policies
across all namespaces in your team (e.g. a set of related microservices).

Learn more in the [HNC User Guide](docs/user-guide) or get started with the
instructions below!

Credits:
* Lead developer: @adrianludwin (aludwin@google.com)
* Current contributors: @yiqigao217, @rjbez17, @GinnyJI
* Other contributors include @sophieliu15, @lafh, @shivi28, @danielSbastos and @entro-pi - thanks all!

## Using HNC

<a name="start"/>

### Getting started and learning more

To install HNC on your cluster, and the `kubectl-hns` plugin on your
workstation, follow the instructions on our release pages.
* [Latest version (v0.8.0)](https://github.com/kubernetes-sigs/multi-tenancy/releases/tag/hnc-v0.8.0)
  * Note that this release is in a different repo since we are currently
    migrating to this new repo. HNC v0.9.0 will be released from this repo.
* Versions of HNC prior to HNC v0.9 are available from our [old
  repo](https://github.com/kubernetes-sigs/multi-tenancy/releases/)

HNC is also supported by the following vendors:

* GCP: as a part of Hierarchy Controller. [Install via Config
  Sync](https://cloud.google.com/kubernetes-engine/docs/add-on/config-sync/how-to/installing-hierarchy-controller)
  on GKE or [via
  ACM](https://cloud.google.com/anthos-config-management/docs/how-to/installing-hierarchy-controller)
  on Anthos.

Once HNC is installed, you can try out the [HNC
quickstart](docs/user-guide/quickstart.md)
to get an idea of what HNC can do. Or, feel free to dive right into the [user
guide](docs/user-guide) instead.

### Roadmap and issues

Please file issues - the more the merrier! Bugs will be investigated ASAP, while
feature requests will be prioritized and assigned to a milestone or backlog.

HNC is not yet GA, so please be cautious about using it on clusters with config
objects you can't afford to lose (e.g. that aren't stored in a Git repository).

All HNC issues are assigned to an HNC milestone. So far, the following
milestones are defined or planned:

* v1.0 - Likely late Q2 or early Q3 2021: HNC recommended for production use.
* [v0.9](https://github.com/kubernetes-sigs/hierarchical-namespaces/milestone/1): move
  HNC to its own repo; continued stability improvements.
* [v0.1-v0.8](https://github.com/kubernetes-sigs/multi-tenancy/milestones):
  see our old repo for details.

## Contact and governance

HNC is overseen by the Working Group on Multi-Tenancy (wg-multitenancy). Please
join us on Slack, mailing lists, and at our meeting at our [community
page](https://github.com/kubernetes/community/blob/master/wg-multitenancy/README.md).

This project is goverened by
[wg-multitenancy](https://github.com/kubernetes-sigs/multi-tenancy), and was
originally located in that repo. It moved to this location after approval by
sig-auth in [KEP #1687](https://github.com/kubernetes/enhancements/issues/1687).

## Contributing to HNC

The best way you can help contribute to bringing hierarchical namespaces to the
Kubernetes ecosystem is to try out HNC and report the problems you have with
either HNC itself or its documentation. Or, if it's working well for you, let us
know on the \#wg-multitenancy channel on Slack, or join a wg-multitenancy
meeting. We'd love to hear from you!

But if you're looking for a deeper level of involvement, please check out our
[contributors guide](docs/contributing.md)!

## CI infrastructure and release

HNC uses Prow to run tests, which is configured
[here](https://github.com/kubernetes/test-infra/tree/master/config/jobs/kubernetes-sigs/wg-multi-tenancy).
The presubmits run `hack/ci-test.sh` in this repo, and the postsubmits and
periodics run `hack/prow-run-e2e.sh`.  Results are displayed on
[testgrid](https://k8s-testgrid.appspot.com/wg-multi-tenancy-hnc) and are
configured
[here](https://github.com/kubernetes/test-infra/tree/master/config/testgrids/kubernetes/wg-multi-tenancy).
For more information about Prow jobs (e.g. a reference for the configs), see
[here](https://github.com/kubernetes/test-infra/blob/master/prow/jobs.md).

These config files should be inspected periodically (e.g. about once a release)
to make sure they're fully up-to-date.

To release HNC, follow [this guide](docs/releasing.md).
