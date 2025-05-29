Certificate rotation

How to rotate certificates for etcd and control plane nodes
Certificates for external etcd and control plane nodes expire after 1 year in EKS Anywhere. EKS Anywhere automatically rotates these certificates when new machines are rolled out in the cluster. New machines are rolled out during cluster lifecycle operations such as upgrade. If you upgrade your cluster at least once a year, as recommended, manual rotation of cluster certificates will not be necessary.
This page shows the process for manually rotating certificates if you have not upgraded your cluster in 1 year.
The following table lists the cluster certificate files:

etcd node	control plane node
apiserver-etcd-client	apiserver-etcd-client
ca	ca
etcdctl-etcd-client	front-proxy-ca
peer	sa
server	etcd/ca.crt
	apiserver-kubelet-client
	apiserver
	front-proxy-client

Commands below can be used for quickly checking your certificates expiring date:


The expiry time of api-server certificate on you cp node

echo | openssl s_client -connect ${CONTROL_PLANE_IP}:6443 2>/dev/null | openssl x509 -noout -dates


The expiry time of certificate used by your external etcd server, if you configured one

echo | openssl s_client -connect ${EXTERNAL_ETCD_IP}:2379 2>/dev/null | openssl x509 -noout -dates



External etcd nodes

If your cluster is using external etcd nodes, you need to renew the etcd node certificates first.



Note

You can check for external etcd nodes by running the following command:



kubectl get etcdadmcluster -A

1.SSH into each etcd node and run the following commands. Etcd automatically detects the new certificates and deprecates its old certificates.

For Bottlrtocket:


you would be in the admin container when you ssh to the Bottlerocket machine

open a root shell

sudo sheltie


pull the image

IMAGE_ID=$(apiclient get | apiclient exec admin jq -r '.settings["host-containers"]["kubeadm-bootstrap"].source')
ctr image pull ${IMAGE_ID}


backup certs

cd /var/lib/etcd
cp -r pki pki.bak
rm pki/*
cp pki.bak/ca.* pki


recreate certificates

ctr run \
--mount type=bind,src=/var/lib/etcd/pki,dst=/etc/etcd/pki,options=rbind:rw \
--net-host \
--rm \
${IMAGE_ID} tmp-cert-renew \
/opt/bin/etcdadm join phase certificates http://eks-a-etcd-dumb-url —init-system kubelet



2.Verify your etcd node is running correctly
For Bottlrtocket:
ETCD_CONTAINER_ID=$(ctr -n k8s.io c ls | grep -w "etcd-io" | cut -d " " -f1 | tail -1)
ctr -n k8s.io t exec -t —exec-id etcd ${ETCD_CONTAINER_ID} etcdctl \
--cacert=/var/lib/etcd/pki/ca.crt \
--cert=/var/lib/etcd/pki/server.crt \
--key=/var/lib/etcd/pki/server.key \
member list


If the above command fails due to multiple etcd containers existing, then navigate to /var/log/containers/etcd and confirm which container was running during the issue timeframe (this container would be the ‘stale’ container). Delete this older etcd once you have renewed the certs and the new etcd container will be able to enter a functioning state. If you don’t do this, the two etcd containers will stay indefinitely and the etcd will not recover.
Repeat the above steps for all etcd nodes.

Save the apiserver-etcd-client crt and key file as a Secret from one of the etcd nodes, so the key can be picked up by new control plane nodes. You will also need them when renewing the certificates on control plane nodes. See the Kubernetes documentation for details on editing Secrets.


kubectl edit secret ${cluster-name}-apiserver-etcd-client -n eksa-system

Note: On Bottlerocket control plane nodes, the certificate filename of apiserver-etcd-client is server-etcd-client.crt instead of apiserver-etcd-client.crt.




Control plane nodes

When there are no external etcd nodes, you only need to rotate the certificates for control plane nodes, as etcd certificates are managed by kubeadm when there are no external etcd nodes.

1.SSH into each control plane node and run the following commands.

For Bottlrtocket:
you would be in the admin container when you ssh to the Bottlerocket machine

open root shell

sudo sheltie


pull the image

IMAGE_ID=$(apiclient get | apiclient exec admin jq -r '.settings["host-containers"]["kubeadm-bootstrap"].source')
ctr image pull ${IMAGE_ID}


renew certs

you may see missing etcd certs error, which is expected if you have external etcd nodes

ctr run \
--mount type=bind,src=/var/lib/kubeadm,dst=/var/lib/kubeadm,options=rbind:rw \
--mount type=bind,src=/var/lib/kubeadm,dst=/etc/kubernetes,options=rbind:rw \
--rm \
${IMAGE_ID} tmp-cert-renew \
/opt/bin/kubeadm certs renew all



2.Verify the certificates have been rotated.

# you may see missing etcd certs error, which is expected if you have external etcd nodes
ctr run \
--mount type=bind,src=/var/lib/kubeadm,dst=/var/lib/kubeadm,options=rbind:rw \
--mount type=bind,src=/var/lib/kubeadm,dst=/etc/kubernetes,options=rbind:rw \
--rm \
${IMAGE_ID} tmp-cert-renew \
/opt/bin/kubeadm certs check-expiration



3.If you have external etcd nodes, manually replace the server-etcd-client.crt and apiserver-etcd-client.key files in the /etc/kubernetes/pki (or /var/lib/kubeadm/pki in Bottlerocket) folder with the files you saved from any etcd node.

For Bottlerocket:
cp apiserver-etcd-client.key /tmp/
cp server-etcd-client.crt /tmp/
sudo sheltie
cp /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp/apiserver-etcd-client.key /var/lib/kubeadm/pki/
cp /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/tmp/server-etcd-client.crt /var/lib/kubeadm/pki/


4.Restart static control plane pods.
For Ubuntu and RHEL: temporarily move all manifest files from /etc/kubernetes/manifests/ and wait for 20 seconds, then move the manifests back to this file location.
For Bottlerocket: re-enable the static pods:

apiclient get | apiclient exec admin jq -r '.settings.kubernetes["static-pods"] | keys[]' | xargs -n 1 -I {} apiclient set settings.kubernetes.static-pods.{}.enabled=false 
apiclient get | apiclient exec admin jq -r '.settings.kubernetes["static-pods"] | keys[]' | xargs -n 1 -I {} apiclient set settings.kubernetes.static-pods.{}.enabled=true

