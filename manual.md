For each etcd node, perform the following operations（195.17.180.57、195.17.180.58、195.17.90.30）


check etcd modify time:

ls -la /var/lib/etcd/pki/

check control panel modify time:
ls -la /var/lib/kubeadm/pki/

First renew external etcd node certificate

SSH to etcd node

ssh -i ~/.ssh/id_ed25519_eks ec2-user@195.17.180.57  # repeat for each etcd node

check if etcd node renew

echo | openssl s_client -connect 195.17.180.57:2379 2>/dev/null | openssl x509 -noout -dates

SSH to etcd node

ssh -i ~/.ssh/id_ed25519_eks ec2-user@195.17.180.58 

check if  etcd node renew
echo | openssl s_client -connect 195.17.180.58:2379 2>/dev/null | openssl x509 -noout -dates


SSH to etcd node
ssh -i ~/.ssh/id_ed25519_eks ec2-user@195.17.90.30

check if  etcd node renew
echo | openssl s_client -connect 195.17.90.30:2379 2>/dev/null | openssl x509 -noout -dates



enter  root shell

sudo sheltie


pull image

IMAGE_ID=$(apiclient get | apiclient exec admin jq -r '.settings["host-containers"]["kubeadm-bootstrap"].source')
ctr image pull ${IMAGE_ID}


backup certificates

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
/opt/bin/etcdadm join phase certificates http://eks-a-etcd-dumb-url --init-system kubelet



verify etcd node is running correctly

ETCD_CONTAINER_ID=$(ctr -n k8s.io c ls | grep -w "etcd-io" | cut -d " " -f1 | tail -1)
ctr -n k8s.io t exec -t --exec-id etcd ${ETCD_CONTAINER_ID} etcdctl \
     --cacert=/var/lib/etcd/pki/ca.crt \
     --cert=/var/lib/etcd/pki/server.crt \
     --key=/var/lib/etcd/pki/server.key \
     member list

Copy certificates to accessible location for bottlerocket

cp /var/lib/etcd/pki/apiserver-etcd-client.crt /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/home/ec2-user/

cp /var/lib/etcd/pki/apiserver-etcd-client.key /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/home/ec2-user/


Modify permissions for copying

chmod 644 /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/home/ec2-user/apiserver-etcd-client.key


exit  root shell

exit


exit etcd node

exit



2. Copy certificates from etcd node to local machin



After finished all etcd nodes’ certificates renew, copy certificate from one of the etcd node：

 Copy certificates from etcd node to local machine

copy certificate

scp -i ~/.ssh/id_ed25519_eks ec2-user@195.17.90.30:/home/ec2-user/apiserver-etcd-client.crt ./

copy private key

scp -i ~/.ssh/id_ed25519_eks ec2-user@195.17.90.30:/home/ec2-user/apiserver-etcd-client.key ./


3. Copy certificates to control plane nodes



Update certificates on the first control plane node

scp -i ~/.ssh/id_ed25519_eks ./apiserver-etcd-client.crt ec2-user@195.17.180.59:/home/ec2-user/

scp -i ~/.ssh/id_ed25519_eks ./apiserver-etcd-client.key ec2-user@195.17.180.59:/home/ec2-user/


Update certificates on the second control plane node

scp -i ~/.ssh/id_ed25519_eks ./apiserver-etcd-client.crt ec2-user@195.17.82.202:/home/ec2-user/

scp -i ~/.ssh/id_ed25519_eks ./apiserver-etcd-client.key ec2-user@195.17.82.202:/home/ec2-user/


4. Update first control panel node’s certificate

SSH to first control panel node

ssh -i ~/.ssh/id_ed25519_eks ec2-user@195.17.180.59


enter root shell

sudo sheltie


pull image

IMAGE_ID=$(apiclient get | apiclient exec admin jq -r '.settings["host-containers"]["kubeadm-bootstrap"].source')
ctr image pull ${IMAGE_ID}


renew certificate

ctr run \
--mount type=bind,src=/var/lib/kubeadm,dst=/var/lib/kubeadm,options=rbind:rw \
--mount type=bind,src=/var/lib/kubeadm,dst=/etc/kubernetes,options=rbind:rw \
--rm \
${IMAGE_ID} tmp-cert-renew \
/opt/bin/kubeadm certs renew all


Copy etcd client certificates to the correct location

cp /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/home/ec2-user/apiserver-etcd-client.key /var/lib/kubeadm/pki/

cp /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/home/ec2-user/apiserver-etcd-client.crt /var/lib/kubeadm/pki/server-etcd-client.crt


verify certificate

ls -la /var/lib/kubeadm/pki/apiserver-etcd-client.key
ls -la /var/lib/kubeadm/pki/server-etcd-client.crt


exit root shell

exit


restart static Pod

apiclient get | apiclient exec admin jq -r '.settings.kubernetes["static-pods"] | keys[]' | xargs -n 1 -I {} apiclient set settings.kubernetes.static-pods.{}.enabled=false

sleep 20

apiclient get | apiclient exec admin jq -r '.settings.kubernetes["static-pods"] | keys[]' | xargs -n 1 -I {} apiclient set settings.kubernetes.static-pods.{}.enabled=true


exit all contral panels nodes

exit


5. Update certificates on the second control plane node

SSH to second control panel node

ssh -i ~/.ssh/id_ed25519_eks ec2-user@195.17.82.202


enter root shell

sudo sheltie


pull image

IMAGE_ID=$(apiclient get | apiclient exec admin jq -r '.settings["host-containers"]["kubeadm-bootstrap"].source')
ctr image pull ${IMAGE_ID}


renew certificate

ctr run \
--mount type=bind,src=/var/lib/kubeadm,dst=/var/lib/kubeadm,options=rbind:rw \
--mount type=bind,src=/var/lib/kubeadm,dst=/etc/kubernetes,options=rbind:rw \
--rm \
${IMAGE_ID} tmp-cert-renew \
/opt/bin/kubeadm certs renew all


Copy etcd client certificates to the correct location

cp /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/home/ec2-user/apiserver-etcd-client.key /var/lib/kubeadm/pki/

cp /run/host-containerd/io.containerd.runtime.v2.task/default/admin/rootfs/home/ec2-user/apiserver-etcd-client.crt /var/lib/kubeadm/pki/server-etcd-client.crt


verify certificate

ls -la /var/lib/kubeadm/pki/apiserver-etcd-client.key

ls -la /var/lib/kubeadm/pki/server-etcd-client.crt


exit root shell

exit


restart static Pod

apiclient get | apiclient exec admin jq -r '.settings.kubernetes["static-pods"] | keys[]' | xargs -n 1 -I {} apiclient set settings.kubernetes.static-pods.{}.enabled=false

sleep 20

apiclient get | apiclient exec admin jq -r '.settings.kubernetes["static-pods"] | keys[]' | xargs -n 1 -I {} apiclient set settings.kubernetes.static-pods.{}.enabled=true


exit all contral panels nodes

exit
