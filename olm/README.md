# Install Using OLM with Morpheus Catalog

In Order to support seamless upgrades of the operator with new versions, need to create a catalog source
of bundles containing list of bundles , and at least one channel, and a sequenced upgrade graph.


## Creating the Morpheus Operator Catalog

### Steps

1. Create directory for morpheus catalog:
```shell
mkdir -p morpheus-catalog
```

2. Create the catalog yaml using opm:
```shell
opm init morpheus-operator --default-channel=alpha --description=../README.md --output yaml >morpheus-catalog/operator.yaml
```

3. Render first bundle into the catalog:
```shell
opm render quay.io/zgrinber/morpheus-operator-bundle:v0.0.2 --output=yaml >> morpheus-catalog/operator.yam
```

4. Add `alpha` channel definition into the catalog yaml file, containing the bundle we just rendered into the catalog:
```shell
 cat << EOF >> morpheus-catalog/operator.yaml
---
schema: olm.channel
package: example-operator
name: alpha
entries:
  - name: morpheus-operator-bundle.v0.0.2
EOF
```

5. Validate the catalog ( should return nothing, verbose response means there is an error):
```shell
opm validate morpheus-catalog
# should print 0.
echo $?
```

6. Build the catalog index image and push it to a container registry ( make sure the image is public in the container registry' server side)
```shell
podman build . -f morpheus-catalog.Dockerfile -t quay.io/zgrinber/morpheus-catalog:latest
podman push quay.io/zgrinber/morpheus-catalog:latest
```

## First Deployment of Operator

1. Create new project
```shell
oc new-project morpheus-operator
```
2. Create new operator group to target all namespaces for operator's permissions
```shell
oc apply -f morpheus-og.yaml
```
**Note: This operator depends on Nvidia GPU drivers installed by [NVIDIA GPU Operator](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/index.html), If you don't have it already installed on your Openshift Cluster,
[Kindly install on cluster manually](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/getting-started.html#operator-install-guide), or deploy the Morpheus operator in `OwnNamespace` installMode
As this is the only installMode that the NVIDIA GPU Operator supports - this will automatically deploy the GPU Operator ( Starts looking from Version 3.9.1) before the Morpheus Operator will be deployed**
To Achieve that, you need to edit the [operatorgroup file](morpheus-og.yaml) before applying it to the cluster that way:
```yaml
apiVersion: operators.coreos.com/v1
kind: OperatorGroup
metadata:
    name: morpheus-og
# Comment Out to disable ownNamespace installMode ( required to enable automatic installation of Nvidia GPU Operator    
spec:
  targetNamespaces:
  - morpheus-operator
```
3. Create a new catalog source and waits for a registry pod to come up in order to serve the budnles from the catalog
```shell
oc apply -f morpheus-catalog-source.yaml
oc get pods 
```
Expected outputs:
```shell
NAME                     READY   STATUS    RESTARTS   AGE
morpheus-catalog-9kn5k   1/1     Running   0          24s

```

4. Create a subscription to install the operator via subscribing to morpheus catalog' `alpha` channel
```shell
oc apply -f morpheus-subscription.yaml
```

5. Wait for the operator to get installed
```shell
 oc get csv morpheus-operator.v0.0.2 -w
```

Expected output
```shell
morpheus-operator.v0.0.2                  morpheus-operator                0.0.2                                                       Pending
morpheus-operator.v0.0.2                  morpheus-operator                0.0.2                                                       InstallReady
morpheus-operator.v0.0.2                  morpheus-operator                0.0.2                                                       Installing
morpheus-operator.v0.0.2                  morpheus-operator                0.0.2                                                       Installing
morpheus-operator.v0.0.2                  morpheus-operator                0.0.2                                                       Installing
morpheus-operator.v0.0.2                  morpheus-operator                0.0.2                                                       Succeeded
```
6. check that the operator is running
```shell
oc get pods
```
Expected output:
```shell
NAME                                                              READY   STATUS      RESTARTS   AGE
b0d4d492ec89774cd2f5981897b8c08ea938452f139888541102b7b4989r7g2   0/1     Completed   0          113s
morpheus-catalog-9kn5k                                            1/1     Running     0          3m13s
morpheus-operator-controller-manager-58dc8dc97f-plg6p             2/2     Running     0          88s
```
## Upgrade of catalog with new bundle

### Pre-requisite

Before performing this process, you should first create a new image version of the operator  + new operator bundle image, please follow [These Section Steps 1-4](../README.md#build-and-install-operator-using-olm--operator-lifecycle-manager)

1. Render the new Morpheus Operator bundle version ( for example, v0.0.5)
```shell
 opm render quay.io/zgrinber/morpheus-operator-bundle:v0.0.5 --output=yaml > new-operator.yaml
```
2. Add this bundle to the catalog
```shell
export LINE_NUMBER=$(grep -n -E "schema: olm.bundle[[:blank:]]*"  morpheus-catalog/operator.yaml | tail -n 1 | awk -F ':' '{print $1}')
let "targetLine = $LINE_NUMBER + 1"
sed  "${targetLine}i: &&&/" morpheus-catalog/operator.yaml | sed '/&&&/ r new-operator.yaml' | sed '/&&&/d' | tee temp.yaml
```

3. Add new Update graph from last version to new version
```shell
echo '[{"name": "morpheus-operator.v0.0.5","replaces": "morpheus-operator.v0.0.4" }]'  | yq -P | awk '{print"  " $0}' >> temp.yaml
mv temp.yaml morpheus-catalog/operator.yaml
rm new-operator.yaml
```

4. Build the updated catalog index image and push it to a container registry and override latest ( make sure the image is public in the container registry' server side)
```shell
podman build . -f morpheus-catalog.Dockerfile -t quay.io/zgrinber/morpheus-catalog:latest
podman push quay.io/zgrinber/morpheus-catalog:latest
```