package main

import (
	"flag"
	"fmt"
	"github.com/tanelmae/gke-private-dns/dns"
	"io/ioutil"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"log"
	"net/http"
	"time"
)

var (
	kubeClient *kubernetes.Clientset
	dnsClient  *dns.CloudDNS
	debug      bool
	timeout    time.Duration
	pendingIP  = make(map[string]*v1.Pod)
)

/*
	Fallback job to check that DNS record exists for every pod
	that is supposed to have it. Should be run as goroutine.
	NOTE! This is the only part where DNS records are managed
	asynchronously. Otherwise handlers are run sequentally based on cluster events.
*/
func syncAllPodsJob(interval time.Duration, namespace, resLabel string) {
	wait.PollInfinite(interval, func() (done bool, err error) {
		podsList, err := kubeClient.CoreV1().Pods(namespace).List(metav1.ListOptions{LabelSelector: resLabel})
		if err != nil {
			log.Println(err)
			return false, err
		}

		bulker := dns.GetBulker(dnsClient)
		for _, pod := range podsList.Items {
			bulker.CheckNext(pod.GetName(), pod.GetOwnerReferences()[0].Name, pod.Status.PodIP)
		}
		// This would delete any DNS record that doesn't match any of the pods
		// passed into CheckNext. To scary to call it, will comment out.
		//bulker.DeleteRemaining()

		// If we return true it will stop PollInfinite
		return false, nil
	})
}

func podUpdated(oldObj, newObj interface{}) {
	newPod := newObj.(*v1.Pod)

	if debug {
		log.Printf("Pod updated: %s\n", newPod.Name)
	}

	/*
		Pod update handler is triggered quite often and for things
		we don't care about here. So we keep in memory list of pods that
		we know that record hasn't been created.
	*/
	_, isPendingIP := pendingIP[newPod.GetName()]
	if isPendingIP && newPod.Status.PodIP != "" {
		dnsClient.CreateRecord(newPod.GetName(), newPod.GetOwnerReferences()[0].Name, newPod.Status.PodIP)
		delete(pendingIP, newPod.GetName())
	}
}

// Handler for pod creation
func podCreated(obj interface{}) {
	pod := obj.(*v1.Pod)
	log.Println("Pod created: " + pod.GetName())
	var err error

	/*
		Needs to wait for slow services to be ready but
		not block everything if pod fails for whatever reasons.
		Either pod updated event handler or fallback sync jobs
		should catch those missing DNS recrods.
	*/
	if pod.Status.PodIP == "" {
		log.Println("Pod IP missing. Will try to resolve.")
		wait.Poll(2*time.Second, timeout, func() (bool, error) {
			pod, err := kubeClient.CoreV1().Pods(pod.Namespace).Get(pod.GetName(), metav1.GetOptions{})
			if err != nil {
				panic(err)
			}
			if pod.Status.PodIP != "" {
				log.Printf("Pod IP resolved: %s\n", pod.Status.PodIP)
				return true, nil
			}
			return false, nil
		})

		pod, err = kubeClient.CoreV1().Pods(pod.Namespace).Get(pod.GetName(), metav1.GetOptions{})
		if err != nil {
			panic(err)
		}

		// Leave if for the pod updated event handler
		if pod.Status.PodIP == "" {
			log.Printf("Failed get pod IP in %s\n", timeout)
			pendingIP[pod.GetName()] = pod
			return
		}
	}

	dnsClient.CreateRecord(pod.GetName(), pod.GetOwnerReferences()[0].Name, pod.Status.PodIP)
}

// Handler for pod deletion events
func podDeleted(obj interface{}) {
	pod := obj.(*v1.Pod)
	log.Println("Pod deleted: " + pod.GetName())

	dnsClient.DeleteRecord(pod.GetName(), pod.GetOwnerReferences()[0].Name, pod.Status.PodIP)
}

func getMetadata(urlPath string) string {
	client := &http.Client{}
	req, err := http.NewRequest("GET",
		fmt.Sprintf("http://metadata/computeMetadata/v1/%s", urlPath), nil)
	req.Header.Add("Metadata-Flavor", "Google")
	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}

	defer resp.Body.Close()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}

	return string(bodyBytes)
}

func main() {
	var zone, project, saFile, namespace, resLabel, domain string
	var syncInterval, watcherResync time.Duration
	var shortFormat bool
	log.Println("service started")
	flag.StringVar(&namespace, "namespace", "default", "Namespace in which to watch resource")
	flag.StringVar(&resLabel, "label", "", "Resource label to watch")
	flag.StringVar(&domain, "domain", "", "Domain used for generated DNS records")
	flag.StringVar(&zone, "zone", "", "GCP DNS zone where to write the records")
	flag.StringVar(&saFile, "sa-file", "", "Path to GCP service account credentials")
	flag.StringVar(&project, "project", "", "GCP project where the DNS zone is. Defaults to the same as GKE cluster.")
	flag.BoolVar(&debug, "debug", false, "Run in debug mode")
	flag.BoolVar(&shortFormat, "short-format", false, "Omit owner name fron the DNS record")
	flag.DurationVar(&timeout, "timeout", time.Minute, "How long to wait for pod IP to be available")
	flag.DurationVar(&syncInterval, "fallback-sync-interval", time.Minute*30, "Interval for fallback sync jobs")
	flag.DurationVar(&watcherResync, "watcher-sync-interval", time.Minute*10, "Interval for fallback sync jobs")
	flag.Set("logtostderr", "true")
	flag.Parse()

	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err)
	}

	kubeClient, err = kubernetes.NewForConfig(config)
	if err != nil {
		panic(err)
	}

	watchlist := cache.NewFilteredListWatchFromClient(
		kubeClient.CoreV1().RESTClient(), "pods", namespace,
		func(options *metav1.ListOptions) {
			options.LabelSelector = resLabel
		})

	_, controller := cache.NewInformer(
		watchlist,
		&v1.Pod{},
		watcherResync,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    podCreated,
			DeleteFunc: podDeleted,
			UpdateFunc: podUpdated,
		},
	)
	log.Printf("Will watch pods with %s label in %s namespace\n", resLabel, namespace)

	if project == "" {
		project = getMetadata("project/project-id")
	}

	// JSON key file for service account with DNS admin permissions
	dnsClient = dns.DNSFromJSON(saFile, zone, project, domain, shortFormat, debug)
	log.Printf("DNS client: %+v\n", dnsClient)

	go syncAllPodsJob(syncInterval, namespace, resLabel)

	/*
		Initial startup will triggger AddFunc for all the pods that match the watchlist.
		Handlers are run sequentally as the events come in.
	*/
	controller.Run(wait.NeverStop)
}
