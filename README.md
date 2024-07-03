# morpheus-operator
 This is An k8/Openshift Operator to install Nvidia Morpheus on cluster, in order to bootstrap working
 on Morpheus SDK in order to run sample/example/custom user pipelines.

## Description
This operator runs on an openshift cluster/k8 cluster, and It Deploys Nvidia Morpheus Deployment along all of its dependencies

1. Morpheus Deployment and configure its environment automatically.
2. Triton Server Deployment 
3. Download some models and install them automatically on Triton Server  
4. Milvus Vector DB and its Underlying Etcd And Minio Instances As Standalone deployments and services.


Note: In order to deploy the operator on a cluster, your user should have a cluster-admin `ClusterRole` bound to it, or at least
all the permissions depicted in [this RBAC Role](./config/rbac/role.yaml)


## Quick-Start
You’ll need an openshift/Kubernetes cluster to run against. You can use [KIND](https://sigs.k8s.io/kind) to get a local cluster for testing, or run against a remote cluster.
**Note:** Your controller will automatically use the current context in your kubeconfig file (i.e. whatever cluster `oc cluster-info` shows).

### Running on the cluster
1. Install Instances of Custom Resources:

```sh
kubectl apply -f config/samples/
```

2. Build and push your image to the location specified by `IMG`:

```sh
make docker-build docker-push IMG=<some-registry>/morpheus-operator:tag
```

3. Deploy the Operator to the cluster with the image specified by `IMG`:

```sh
make deploy IMG=<some-registry>/morpheus-operator:tag
```

### Uninstall CRDs
To delete the CRDs from the cluster:

```sh
make uninstall
```

### Undeploy controller
UnDeploy the Operator from the cluster:

```sh
make undeploy
```

### Run The Operator Locally

1. Run the operator locally on your machine
```shell
 make install run
```

2. Create some new project in openshift ( or namespace if running on k8)
```shell
oc new-project morpheus-test
```

3. On another terminal, apply to the cluster a sample `Morpheus` CR ( Custom resource)
```shell
oc apply -f config/samples/ai_v1alpha1_morpheus.yaml 
```

4. Check that all deployments and pod created
```shell
oc get deployments,pods
```
5. Press on `Ctrl+Z` or `Ctrl+C` to quit the running operator, and then remove CRD from cluster
```shell
make uninstall
```

## Build And Install Operator Using OLM ( Operator Lifecycle Manager)

1. Define the following environment variables according to the version to build, and according to user/namespace/org in image registry
```shell
# change to the current operator version 
export MORPHEUS_VERSION=0.0.3
export VERSION=${MORPHEUS_VERSION}
# Set change to your username/org/namespace in your container registry.
export USER_NAMESPACE=zgrinber
export MORPHEUS_IMAGE_BASE=quay.io/${USER_NAMESPACE}/morpheus-operator
export IMAGE_BUNDLE_BASE=quay.io/${USER_NAMESPACE}/morpheus-operator-bundle
export MORPHEUS_BUNDLE_VERSION=v0.0.3
```

2. Login to your container registry using your credentials, for example:
```shell
podman login quay.io
```
3. Build Operator Container Image and push it to registry
```shell
IMG=${MORPHEUS_IMAGE_BASE}:${MORPHEUS_VERSION} make docker-build docker-push
```
4. Build Operator Bundle image and push it to registry
```shell
IMG=${MORPHEUS_IMAGE_BASE}:${MORPHEUS_VERSION} BUNDLE_IMG=${IMAGE_BUNDLE_BASE}:${MORPHEUS_BUNDLE_VERSION} make bundle bundle-build bundle-push
```
5. Now Run the bundle and deploy the Operator to the cluster all at once
```shell
operator-sdk run bundle ${IMAGE_BUNDLE_BASE}:${MORPHEUS_BUNDLE_VERSION}
```

**Note: This operator depends on Nvidia GPU drivers installed by [NVIDIA GPU Operator](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/index.html), If you don't have it already installed on your Openshift Cluster, 
[Kindly install on cluster manually](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/getting-started.html#operator-install-guide), or deploy the Morpheus operator in `OwnNamespace` installMode
As this is the only installMode that the NVIDIA GPU Operator supports - this will automatically deploy the GPU Operator ( Starts looking from Version 3.9.1) before the Morpheus Operator will be deployed**
```shell
operator-sdk run bundle ${IMAGE_BUNDLE_BASE}:${MORPHEUS_BUNDLE_VERSION} --install-mode=OwnNamespace
```

6. Check that the operator installed correctly ( wait until PHASE=`Succeeded`)
```shell
oc get csv morpheus-operator.v0.0.2 -w
```
### Using the Operator
7. Create a new project 
```shell
oc new-project morpheus-test-cr
```

8. Create a sample Custom Resource on that Project
```shell
cat > morpheus-cr.yaml << EOF
apiVersion: ai.redhat.com/v1alpha1
kind: Morpheus
metadata:
  labels:
    app.kubernetes.io/name: morpheus
    app.kubernetes.io/instance: morpheus-sample
    app.kubernetes.io/part-of: morpheus-operator
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/created-by: morpheus-operator
  name: morpheus-example
spec:
  serviceAccountName: morpheus-sa
  autoBindSccToSa: true
  milvus:
    minio:
      rootUser: admin
      rootPassword: admin123#
  jupyter:
    labPassword: yourPassword    
EOF

oc apply -f morpheus-cr.yaml
```
9. Watch the created resources:
```shell
watch " oc get morpheus ;oc get morpheus,sa,secret,role,rolebinding,pvc,deployment,svc,route -l created-by=morpheus-operator -o wide ; echo ; oc get pods -o wide"
```
Output:
```shell
Every 2.0s:  oc get morpheus ;oc get morpheus,sa,secret,role,rolebinding,pvc,deployment,svc,route -l created-by=morpheus-operator -o wide ; echo ; oc get pods -o wide                zgrinber-thinkpadp1gen3.raanaii.csb: Wed Jul  3 00:35:59 2024

NAME               AGE
morpheus-example   12m
NAME                         SECRETS   AGE
serviceaccount/morpheus-sa   1         12m

NAME                                    TYPE     DATA   AGE
secret/morpheus-example-jupyter-token   Opaque   1	12m

NAME                                              CREATED AT
role.rbac.authorization.k8s.io/morpheus-example   2024-07-02T21:23:38Z

NAME                                                     ROLE                    AGE   USERS   GROUPS   SERVICEACCOUNTS
rolebinding.rbac.authorization.k8s.io/morpheus-example   Role/morpheus-example   12m                    zvika-test/morpheus-sa

NAME                                      STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS   AGE   VOLUMEMODE
persistentvolumeclaim/milvus-data         Bound    pvc-b171b531-5016-4760-b681-fc6e35b3bdc1   2Gi        RWO            gp3            12m   Filesystem
persistentvolumeclaim/milvus-etcd-data    Bound    pvc-611cb9fa-da32-4e6f-b761-57cc0585ebb1   2Gi        RWO            gp3            12m   Filesystem
persistentvolumeclaim/milvus-minio-data   Bound    pvc-bb58b022-e7a2-4903-ae6b-e6e2cb6278b2   2Gi        RWO            gp3            12m   Filesystem
persistentvolumeclaim/morpheus-repo	  Bound    pvc-36d37e58-b6bf-4ba9-846e-c5c0f1b7a1fd   20Gi	 RWO            gp3            12m   Filesystem

NAME                                READY   UP-TO-DATE   AVAILABLE   AGE   CONTAINERS         IMAGES                                     SELECTOR
deployment.apps/milvus-etcd         1/1     1            1           12m   etcd               quay.io/coreos/etcd:v3.5.5                 app=milvus,component=etcd,version=v3.5.5
deployment.apps/milvus-minio        1/1     1            1           12m   minio              minio/minio:RELEASE.2023-03-20T20-16-18Z   app=milvus,component=minio,version=v3.5.5
deployment.apps/milvus-standalone   1/1     1            1           12m   milvus             milvusdb/milvus:v2.4.1                     app=milvus,component=milvus,version=v2.4.1
deployment.apps/morpheus-example    1/1     1            1           12m   morpheus-jupyter   quay.io/zgrinber/morpheus-jupyter:3        app=morpheus,component=jupyter,jupyter-password-hash=ff85305ab86ceff0f59877358928d81d,version=v24.03.
02
deployment.apps/triton-server       1/1     1            1           12m   triton             nvcr.io/nvidia/tritonserver:23.06-py3	 app=triton-server,version=v23.06

NAME                        TYPE        CLUSTER-IP	 EXTERNAL-IP   PORT(S)                      AGE   SELECTOR
service/milvus-etcd         ClusterIP   172.30.224.180   <none>        2379/TCP                     12m   app=milvus,component=etcd
service/milvus-minio        ClusterIP   172.30.185.17    <none>        9000/TCP,9001/TCP            12m   app=milvus,component=minio
service/milvus-standalone   ClusterIP   172.30.17.40     <none>        19530/TCP,9091/TCP           12m   app=milvus,component=milvus
service/morpheus-example    ClusterIP   172.30.12.36     <none>        8888/TCP                     12m   app=morpheus,component=jupyter
service/triton-server       ClusterIP   172.30.226.197   <none>        8001/TCP,8000/TCP,8002/TCP   12m   app=triton-server

NAME                                        HOST/PORT                                                              PATH   SERVICES           PORT    TERMINATION     WILDCARD
route.route.openshift.io/morpheus-example   morpheus-example-zvika-test.apps.cn-ai-lab.6aw6.p1.openshiftapps.com          morpheus-example   <all>   edge/Redirect   None

NAME                               READY   STATUS    RESTARTS   AGE   IP               NODE                           NOMINATED NODE   READINESS GATES
milvus-etcd-747cd8b6b9-llthz	   1/1     Running   0          12m   10.128.103.132   ip-10-0-219-241.ec2.internal   <none>           <none>
milvus-minio-5d4dd775d7-nzcr2	   1/1     Running   0          12m   10.128.103.131   ip-10-0-219-241.ec2.internal   <none>           <none>
milvus-standalone-58b7f696-m74mm   1/1     Running   0          12m   10.128.106.44    ip-10-0-177-56.ec2.internal    <none>           <none>
morpheus-example-6648d4745-vv4kn   1/1     Running   0          12m   10.128.103.130   ip-10-0-219-241.ec2.internal   <none>           <none>
triton-server-5865c9bfd5-lj9lk     1/1     Running   0          12m   10.128.6.111     ip-10-0-149-189.ec2.internal   <none>           <none>

```
10. See the Morpheus Custom resource instance Status 
```shell
oc describe morpheus morpheus-example
```
Output
```shell
Name:         morpheus-example
Namespace:    morpheus-zvika
Labels:       app.kubernetes.io/created-by=morpheus-operator
              app.kubernetes.io/instance=morpheus-sample
              app.kubernetes.io/managed-by=kustomize
              app.kubernetes.io/name=morpheus
              app.kubernetes.io/part-of=morpheus-operator
Annotations:  <none>
API Version:  ai.redhat.com/v1alpha1
Kind:         Morpheus
Metadata:
  Creation Timestamp:  2024-06-24T16:29:46Z
  Generation:          1
  Resource Version:    429925118
  UID:                 aa9c9a8c-d102-47f2-a6f6-e909f68e3644
Spec:
  Auto Bind Scc To Sa:  true
  Milvus:
    Minio:
      Root Password:     admin123#
      Root User:         admin
  Service Account Name:  morpheus-sa
  Jupyter:
    Lab Password: yourpassword
Status:
  Conditions:
    Last Transition Time:  2024-06-24T16:29:46Z
    Message:               Morpheus Deployment successfully created and deployed!
    Reason:                Reconciling:Create
    Status:                True
    Type:                  MorpheusDeployed
    Last Transition Time:  2024-06-24T16:29:46Z
    Message:               Triton Server successfully Deployed!
    Reason:                Reconciling:Create
    Status:                True
    Type:                  TritonDeployed
    Last Transition Time:  2024-06-24T16:29:46Z
    Message:               Etcd Instance successfully Deployed!
    Reason:                Reconciling:Create
    Status:                True
    Type:                  EtcdDeployed
    Last Transition Time:  2024-06-24T16:29:46Z
    Message:               Minio Instance successfully Deployed!
    Reason:                Reconciling:Create
    Status:                True
    Type:                  MinioDeployed
    Last Transition Time:  2024-06-24T16:29:46Z
    Message:               MilvusDB Instance successfully Deployed!
    Reason:                Reconciling:Create
    Status:                True
    Type:                  MilvusDBDeployed
Events:                    <none>
```

11. Access Jupyter Notebook/Lab Server
```shell
## ROUTE_NAME is like the Morpheus custom resource name
export ROUTE_NAME=morpheus-example
xdg-open https://$(oc get route $ROUTE_NAME -o=jsonpath="{..spec.host}")/lab
```

12. Enter the password/token you've provided in the Morpheus CR( `.spec.jupyter.labPassword` ) you can extract it in a one-liner command:
```shell
oc get morpheus morpheus-example -o=jsonpath='{.spec.jupyter.labPassword}' ; echo 
```
In case you didn't input jupyter password in the morpheus CR, the password/token will be generated automatically in a random way by the operator, the way to get it is as follows ( assuming that morpheus CR name=morpheus-example):
```shell
oc get secret morpheus-example-jupyter-token -o=jsonpath='{.data.JUPYTER_TOKEN}' | base64 -d ; echo

```
### Custom Resource Example
```yaml
apiVersion: ai.redhat.com/v1alpha1
kind: Morpheus
metadata:
  labels:
    app.kubernetes.io/name: morpheus
    app.kubernetes.io/instance: morpheus-sample
    app.kubernetes.io/part-of: morpheus-operator
    app.kubernetes.io/managed-by: kustomize
    app.kubernetes.io/created-by: morpheus-operator
  name: morpheus-example
spec:
  serviceAccountName: morpheus-sa
  autoBindSccToSa: true
  milvus:
    minio:
      rootUser: admin
      rootPassword: admin123#
  jupyter:
    labPassword: yourPassword
```

### Morpheus Operator API Spec
| Parameter                                 | Description                                                                                                                                                                                                                 | Values                | Default     |
|-------------------------------------------|-----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|-----------------------|-------------|
| spec.serviceAccountName                   | Service account name that will run morpheus jupyter deployment and triton deployment ,                                                                                                                                      | string               | morpheus-sa |
| spec.autoBindSccToSa                      | For openshift only, create a role + rolebinding for the `serviceAccountName so deployment, granting permission of anyUid scc, if not running on openshift ( k8), must explicitly changed to false                           | Boolean (true, false) | true
| spec.milvus.minio.rootUser                | root user of minio object store used by milvusDB instance to be deployed                                                                                                                                                    | string                | admin
| spec.milvus.minio.rootPassword            | root password of minio object store used by milvusDB instance to be deployed                                                                                                                                                | string                | admin123#
| spec.milvus.minio.storagePvcSize          | size of Pvc size used by Minio instance                                                                                                                                                                                     | string                | 2Gi
| spec.milvus.etcd.storagePvcSize           | size of Pvc size used by etcd key store instance used by MilvusDB                                                                                                                                                           | string                | 2Gi
| spec.milvus.StoragePvcSize                | size of Pvc size used by MilvusDB                                                                                                                                                                                           | string                | 2Gi
| spec.tritonServer.morpheusRepoStorageSize | size of Pvc size used by Triton inference server                                                                                                                                                                            | string                | 20Gi
| spec.jupyter.labPassword                  | Password of deployed jupyter notebook server, if not explicitly set, then a generated random token is being used, the password is stored in a secret in the CR namespace with name of  ${morpheus-cr-name} + -jupyter-token | string                | generated random token

### How it works
This project aims to follow the Kubernetes [Operator pattern](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/).

It uses [Controllers](https://kubernetes.io/docs/concepts/architecture/controller/),
which provide a reconcile function responsible for synchronizing resources until the desired state is reached on the cluster.


### Modifying the API definitions
If you are editing the API definitions, generate the manifests such as CRs or CRDs using:

```sh
make manifests
```

**NOTE:** Run `make --help` for more information on all potential `make` targets

More information can be found via the [Kubebuilder Documentation](https://book.kubebuilder.io/introduction.html)

## License

Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

