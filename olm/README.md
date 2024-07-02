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

1. Render the new Morpheus Operator bundle version ( for example, v0.0.3)
```shell
 opm render quay.io/zgrinber/morpheus-operator-bundle:v0.0.3 --output=yaml > new-operator.yaml
```
2. Add this bundle to the catalog
```shell
sed -e  '/schema: olm.bundle[[:blank:]]*/ a &!&' olm/morpheus-catalog/operator.yaml | sed '/&!&/ r new-operator.yaml' | sed '/&!&/d'  > morpheus-catalog/operator.yaml
```

3. Add new Update graph
```shell
echo '[{"name": "morpheus-operator.v0.0.3","replaces": "morpheus-operator.v0.0.2" }]'  | yq -P | awk '{print"  " $0}' >> morpheus-catalog/operator.yaml
```

4. Build the updated catalog index image and push it to a container registry and override latest ( make sure the image is public in the container registry' server side)
```shell
podman build . -f morpheus-catalog.Dockerfile -t quay.io/zgrinber/morpheus-catalog:latest
podman push quay.io/zgrinber/morpheus-catalog:latest
```