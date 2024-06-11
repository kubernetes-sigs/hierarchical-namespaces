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

ROOTDIR="$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )/../../"
YQ=${ROOTDIR}/bin/yq
MANIFESTDIR=${ROOTDIR}/manifests
CAHRTDIR=${ROOTDIR}/charts/hnc
TEMPLATESDIR=${CAHRTDIR}/templates

# Remove CRDs templates from templates directory
# CRDs templates have already been generated in the crds directory
crd_files=$(ls ${CAHRTDIR}/crds/* | xargs -n 1 basename)
for file in ${crd_files}; do
    rm -f ${TEMPLATESDIR}/${file}
done


# [HRQ] Additional ValidatingWebhookConfiguration setting for HRQ
# It will be added to default.yaml when HRQ is enabled by default near the future release.
HRQWEBHOOK=$(
cat <<'EOF'
  {{- if .Values.hrq.enabled }}
  - admissionReviewVersions:
    - v1
    - v1beta1
    clientConfig:
      service:
        name: '{{ include "hnc.fullname" . }}-webhook-service'
        namespace: '{{ include "hnc.namespace" . }}'
        path: /validate-hnc-x-k8s-io-v1alpha2-hrq
    failurePolicy: Fail
    name: hrq.hnc.x-k8s.io
    rules:
    - apiGroups:
      - hnc.x-k8s.io
      apiVersions:
      - v1alpha2
      operations:
      - CREATE
      - UPDATE
      resources:
      - hierarchicalresourcequotas
    sideEffects: None
  - admissionReviewVersions:
    - v1
    - v1beta1
    clientConfig:
      service:
        name: '{{ include "hnc.fullname" . }}-webhook-service'
        namespace: '{{ include "hnc.namespace" . }}'
        path: /validate-hnc-x-k8s-io-v1alpha2-resourcequotasstatus
    failurePolicy: Ignore
    name: resourcesquotasstatus.hnc.x-k8s.io
    rules:
    - apiGroups:
      - ""
      apiVersions:
      - v1
      operations:
      - UPDATE
      resources:
      - resourcequotas/status
    sideEffects: None
  {{- end }}
EOF
)

# Update templates
for output_file in ${TEMPLATESDIR}/*.yaml; do
  # Add prefix placeholder to Resource Name
  if [ "$($YQ '.metadata | has("name")' $output_file)" = "true" ]; then
    
    resourcename=$($YQ -N '.metadata.name' $output_file | sed s/hnc-//g) $YQ -N -i '.metadata.name |= "{{ include \"hnc.fullname\" . }}-" + strenv(resourcename)' $output_file

    # Add prefix placeholder to Service Name of WebhookConfiguration
    if [ "$($YQ '.kind | (. == "*WebhookConfiguration")' $output_file)" = "true" ]; then
      $YQ -N -i '.webhooks[].clientConfig.service.name |= "{{ include \"hnc.fullname\" . }}-webhook-service"' $output_file
    fi
  fi

  # Replace Namespace Name with placeholder
  if [ "$($YQ '.metadata | has("namespace")' $output_file)" = "true" ]; then
    $YQ -N -i '.metadata.namespace |= "{{ include \"hnc.namespace\" . }}"' $output_file
  fi
  if [ "$($YQ '.kind | (. == "*WebhookConfiguration")' $output_file)" = "true" ]; then
    $YQ -N -i '.webhooks[].clientConfig.service.namespace |= "{{ include \"hnc.namespace\" . }}"' $output_file
  fi


  # Update Deployment templates
  if [ "$($YQ '.kind | (. == "Deployment")' $output_file)" = "true" ]; then
    # Replace image name with placeholder
    $YQ -N -i '(.spec.template.spec.containers[] | select(.name == "manager") | .image) |= "{{ .Values.image.repository }}:{{ .Values.image.tag | default \"hnc-manager:latest\" }}"' $output_file

    # Add imagePullPolicy placeholder
    $YQ -N -i '(.spec.template.spec.containers[] | select(.name == "manager") | .imagePullPolicy) |= "{{ .Values.image.imagePullPolicy }}"' $output_file

    # Remove --excluded-namespace arg
    # It will be replaced with placeholder later
    $YQ -N -i 'del(.spec.template.spec.containers[] | select(.name=="manager").args[] | select(.=="--excluded-namespace=*"))' $output_file

    # Replace resources with placeholder
    $YQ -N -i 'del(.spec.template.spec.containers[] | select(.name=="manager") | .resources)' $output_file
    $YQ -N -i '(.spec.template.spec.containers[] | select(.name=="manager") | .resources) |= "{{- toYaml . | nindent 12}}"' $output_file

    # Add nodeSelector placeholder
    $YQ -N -i '.spec.template.spec.nodeSelector |= "{{- toYaml . | nindent 8}}"' $output_file

    # Add affinity placeholder
    $YQ -N -i '.spec.template.spec.affinity |= "{{- toYaml . | nindent 8}}"' $output_file

    # Add tolerations placeholder
    $YQ -N -i '.spec.template.spec.tolerations |= "{{- toYaml . | nindent 8}}"' $output_file

    # Add secretName placeholder
    $YQ -N -i '(.spec.template.spec.volumes[] | select(.name == "cert") | .secret.secretName) |= "{{ include \"hnc.fullname\" . }}-webhook-server-cert"' $output_file

    # Additional update for controller-manager Deployment
    if [ "$($YQ '.metadata.name | (. == "*-controller-manager")' $output_file)" = "true" ]; then

      # [HA] Add conditional blocks for --no-webhooks arg
      sed -i -e '/args:/a \            {{- if .Values.ha.enabled }}\n \           - --no-webhooks\n \           {{- end }}' $output_file

      # Add scope block for resources
      sed -i -e 's/resources:/\{{- with .Values.manager.resources }}\n \         resources:/' $output_file
      sed -i -e '/resources:/a \          {{- end }}' $output_file

      # Add scope block for nodeSelector
      sed -i -e 's/nodeSelector:/\{{- with .Values.manager.nodeSelector }}\n \     nodeSelector:/' $output_file
      sed -i -e '/nodeSelector:/a \      {{- end }}' $output_file

      # Add scope block for affinity
      sed -i -e 's/affinity:/\{{- with .Values.manager.affinity }}\n \     affinity:/' $output_file
      sed -i -e '/affinity:/a \      {{- end }}' $output_file

      # Add scope block for tolerations
      sed -i -e 's/tolerations:/\{{- with .Values.manager.tolerations }}\n \     tolerations:/' $output_file
      sed -i -e '/tolerations:/a \      {{- end }}' $output_file

    # [HA] Additional update for controller-manager-ha Deployment
    elif [ "$($YQ '.metadata.name | (. == "*-controller-manager-ha")' $output_file)" = "true" ]; then

      # [HA] Add scope block for resources
      sed -i -e 's/resources:/\{{- with .Values.ha.manager.resources }}\n \         resources:/' $output_file
      sed -i -e '/resources:/a \          {{- end }}' $output_file

      # [HA] Add scope block for nodeSelector
      sed -i -e 's/nodeSelector:/\{{- with .Values.ha.manager.nodeSelector }}\n \     nodeSelector:/' $output_file
      sed -i -e '/nodeSelector:/a \      {{- end }}' $output_file

      # [HA] Add scope block for affinity
      sed -i -e 's/affinity:/\{{- with .Values.ha.manager.affinity }}\n \     affinity:/' $output_file
      sed -i -e '/affinity:/a \      {{- end }}' $output_file

      # [HA] Add scope block for tolerations
      sed -i -e 's/tolerations:/\{{- with .Values.ha.manager.tolerations }}\n \     tolerations:/' $output_file
      sed -i -e '/tolerations:/a \      {{- end }}' $output_file

      # [HA] Add conditional blocks for controller-manager-ha Deployment
      sed -i -e '1s/^/{{- if .Values.ha.enabled }}\n/g' $output_file
      sed -i -e '$s/$/\n{{- end }}/g' $output_file

    fi

    # Add placeholder for --excluded-namespace arg
    sed -i -e '/args:/a \            {{- range $hncExcludeNamespace := .Values.hncExcludeNamespaces }}\n \           - --excluded-namespace={{ $hncExcludeNamespace }}\n \           {{- end }}' $output_file

    # Add placeholder for --included-namespace-regex arg
    sed -i -e '/args:/a \            {{- if $hncIncludeNamespacesRegex }}\n \           - --included-namespace-regex={{ $hncIncludeNamespacesRegex }}\n \           {{- end }}' $output_file

    # [HRQ] Add conditional blocks for --enable-hrq arg
    sed -i -e '/args:/a \            {{- if .Values.hrq.enabled }}\n \           - --enable-hrq\n \           {{- end }}' $output_file

    # Add conditional blocks for imagePullPolicy
    sed -i -e 's/imagePullPolicy:/\{{- with .Values.imagePullPolicy }}\n \         imagePullPolicy:/' $output_file
    sed -i -e '/imagePullPolicy:/a \          {{- end }}' $output_file

    # Remove extra '' from templates
    sed -i -e s/\'//g $output_file 

  # Update RoleBinding template
  elif [ "$($YQ '.kind | (. == "RoleBinding")' $output_file)" = "true" ]; then
    if [ "$($YQ '.metadata.name | (. == "*-leader-election-rolebinding")' $output_file)" = "true" ]; then
      $YQ -N -i '.roleRef.name |= "{{ include \"hnc.fullname\" . }}-leader-election-role"' $output_file
      $YQ -N -i '(.subjects[] | select(.kind == "ServiceAccount") | .namespace) |= "{{ include \"hnc.namespace\" . }}"' $output_file
    fi

  # Update ClusterRoleBinding template
  elif [ "$($YQ '.kind | (. == "ClusterRoleBinding")' $output_file)" = "true" ]; then
    if [ "$($YQ '.metadata.name | (. == "*-manager-rolebinding")' $output_file)" = "true" ]; then
      $YQ -N -i '.roleRef.name |= "{{ include \"hnc.fullname\" . }}-manager-role"' $output_file
      $YQ -N -i '(.subjects[] | select(.kind == "ServiceAccount") | .namespace) |= "{{ include \"hnc.namespace\" . }}"' $output_file
    fi

  # [HA]Update Service templates for HA
  # Add conditional blocks for selector
  elif [ "$($YQ '.kind | (. == "Service")' $output_file)" = "true" ]; then
    if [ "$($YQ '.metadata.name | (. == "*-webhook-service")' $output_file)" = "true" ]; then
      $YQ -N -i 'del(.spec.selector)' $output_file
      sed -i -e '$s/$/\n \ selector: \n  \  {{- if .Values.ha.enabled }}\n \   control-plane: controller-manager-ha\n \   {{- else }}\n \   control-plane: controller-manager\n \   {{- end }}/g' $output_file
    fi

  # [HRQ] Update ValidatingWebhookConfiguration template for HRQ
  # It will be added to default.yaml when HRQ is enabled by default near the future release.
  elif [ "$($YQ '.kind | (. == "ValidatingWebhookConfiguration")' $output_file)" = "true" ]; then
    echo "$HRQWEBHOOK" >> "$output_file"
  fi

done
