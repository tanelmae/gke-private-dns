# Private DNS records for GKE pods
Pods in [VPC-native Google Kubernetes Engine clusters](https://cloud.google.com/kubernetes-engine/docs/how-to/alias-ips) have routable IPs within the given VPC. But when connecting to pod in another cluster local kube-dns records are useless. This service maintains DNS records in a [private CloudDNS zone](https://cloud.google.com/dns/zones/#creating_private_zones) based on pod namespace and labeling. 

Service account used by the service needs to have proper permissions to manage DNS records in the given zone.

To keep DNS records predictable use [StatefulSet](https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/) as then pod names would follow predictable pattern.

DNS records pattern: `<pod-name>.<owner-name>.<configured-domain>`. Owner is the name of StatefulSet. For an example with StatefulSet called `my-service` using domain`my-cluster.my-service.private` : `my-service-0.my-service.my-cluster.my-service.private`. Including **owner** name allows single cluster pods to share wildcard certificates for `*.my-service.my-cluster.my-service.private` If you don't need owner name in the record pass `-short-format=true` argument. Then the example record would be: `my-service-0.my-cluster.my-service.private`

```
Usage:
  -domain string
    	Domain used for generated DNS records (Required)
  -zone string
    	GCP DNS zone where to write the records (Required)
  -label string
    	Resource label to watch (Required)
  -sa-file string
    	Path to GCP service account credentials (Required)
  -namespace string
    	Namespace in which to watch resource (default "default")
  -project string
    	GCP project where the DNS zone is. Defaults to the same as GKE cluster.
  -short-format
    	Omit owner name fron the DNS record (default false)
  -timeout duration
    	How long to wait for pod IP to be available (default 1m0s)
  -watcher-sync-interval duration
    	Interval for fallback sync jobs (default 10m0s)
  -fallback-sync-interval duration
    	Interval for fallback sync jobs (default 30m0s)
  -debug
    	Run in debug mode (default false)
        ```