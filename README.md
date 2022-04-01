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

Lead developer: @adrianludwin (aludwin@google.com)

## Using HNC

<a name="start"/>

### Getting started and learning more

To install HNC on your cluster, and the `kubectl-hns` plugin on your
workstation, follow the instructions on our [release
pages](https://github.com/kubernetes-sigs/hierarchical-namespaces/releases/).
Note that versions of HNC prior to HNC v0.9 are available from our [old
repo](https://github.com/kubernetes-sigs/multi-tenancy/releases/).

Once HNC is installed, you can try out the [HNC
quickstart](docs/user-guide/quickstart.md)
to get an idea of what HNC can do. Or, feel free to dive right into the [user
guide](docs/user-guide) instead.

### Roadmap and issues

Please file issues - the more the merrier! Bugs will be investigated ASAP, while
feature requests will be prioritized and assigned to a milestone or backlog.

All HNC issues are assigned to an HNC milestone. So far, the following
milestones are defined or planned:

* [v1.1](https://github.com/kubernetes-sigs/hierarchical-namespaces/milestone/3):
  Improved UX for managed labels; performance improvements.
* [v1.0](https://github.com/kubernetes-sigs/hierarchical-namespaces/milestone/2):
  HNC recommended for production use (released March 31 2022)
* [v0.9](https://github.com/kubernetes-sigs/hierarchical-namespaces/milestone/1):
  move HNC to its own repo; continued stability improvements.
* [v0.1-v0.8](https://github.com/kubernetes-sigs/multi-tenancy/milestones):
  see our old repo for details.

## Contact and governance

HNC is overseen by the Working Group on Multi-Tenancy (wg-multitenancy). Please
join us on Slack, mailing lists, and at our meeting at our [community
page](https://github.com/kubernetes/community/blob/master/wg-multitenancy/README.md).

If you use HNC, we recommend joining the
[kubernetes-hnc-announce](https://groups.google.com/g/kubernetes-hnc-announce)
mailing list, a low-volume list to receive updates such as new version of HNC
and proposed changes or new features.

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
