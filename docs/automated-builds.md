# Automated Builds

HNC uses [Google Cloud Build](https://cloud.google.com/build) for building some artifacts.
Currently only docker builds are automated for every push to the main branch.

## Trigger Setup

Cloud Build Triggers can be configured via the GCP Console. In order to setup triggers there
are a few prerequisites. First, the Cloud Build App needs to be install within the GitHub org.
Fortunately, kubernetes-sigs already does this.

Secondly, you will need to manually authorize
Cloud Build to access your repository. Within the Cloud Build UI, simply add a repository and
follow the prompts for authorizing the repo. This will be tied to the specific GitHub user
who authorizes. Currently, @rjbez17 is configured for all HNC triggers.This authorization allows
Cloud Build to watch for changes to the HNC repository and allow us to trigger on changes to branchs or tags.

Finally, you can now setup triggers for various files.

## Current Cloud Build Files

| File                    | Trigger(s)            | Description                                                   |
|:------------------------|:----------------------|:--------------------------------------------------------------|
| ./cloudbuild.yaml       | None                  | Used to manually call cloud build to create release artifacts |
| ./cloudbuild-build.yaml | Pushes to main branch | Used to create dev builds based on current HEAD               |

## Account Information

HNC Currently uses the following:

GCP Project Name: k8s-staging-multitenancy
GCP Project Number: 817922591645
Owner: Multitenancy-WG
Access provided by k8s-infra [here](https://github.com/kubernetes/k8s.io/blob/main/groups/sig-auth/groups.yaml#L48)
Created by k8s-infra [here](https://github.com/kubernetes/k8s.io/blob/main/infra/gcp/infra.yaml#L311)
