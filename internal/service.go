package internal

import (
	"github.com/tanelmae/gke-private-dns/pkg/dns"
	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"time"
)

// Run starts the service
func Run(namespace, resLabel string, syncInterval, watcherResync,
	podTimeout time.Duration, cloudDNS *dns.CloudDNS) {
	config, err := rest.InClusterConfig()
	if err != nil {
		klog.Fatalln(err)
	}

	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalln(err)
	}

	manager := RecordsManager{
		dnsClient:     cloudDNS,
		timeout:       podTimeout,
		watcherResync: watcherResync,
		syncInterval:  syncInterval,
		kubeClient:    kubeClient,
		namespace:     namespace,
		resLabel:      resLabel,
	}

	manager.startWatcher()
}

// RecordsManager ..
type RecordsManager struct {
	kubeClient    *kubernetes.Clientset
	dnsClient     *dns.CloudDNS
	timeout       time.Duration
	watcherResync time.Duration
	syncInterval  time.Duration
	pendingIP     map[string]*v1.Pod
	namespace     string
	resLabel      string
}

func (m RecordsManager) startWatcher() {
	watchlist := cache.NewFilteredListWatchFromClient(
		m.kubeClient.CoreV1().RESTClient(), "pods", m.namespace,
		func(options *metav1.ListOptions) {
			options.LabelSelector = m.resLabel
		})

	_, controller := cache.NewInformer(
		watchlist,
		&v1.Pod{},
		m.watcherResync,
		cache.ResourceEventHandlerFuncs{
			AddFunc:    m.podCreated,
			DeleteFunc: m.podDeleted,
			UpdateFunc: m.podUpdated,
		},
	)
	/*
		Initial startup will triggger AddFunc for all the pods that match the watchlist.
		Handlers are run sequentally as the events come in.
	*/
	controller.Run(wait.NeverStop)
	klog.Infof("Will watch pods with %s label in %s namespace\n", m.resLabel, m.namespace)

	// Checks with given interval that all expected records are there
	// and removes any stale record if any is found.
	m.startSyncJob()
}

/*
	Fallback job to check that DNS record exists for every pod
	that is supposed to have it. Should be run as goroutine.
	NOTE! This is the only part where DNS records are managed
	asynchronously. Otherwise handlers are run sequentally based on cluster events.
*/
// Could we use the Store instead?
func (m RecordsManager) startSyncJob() {
	go wait.PollInfinite(m.syncInterval, func() (done bool, err error) {
		klog.V(2).Infoln("Background sync job started")
		podsList, err := m.kubeClient.CoreV1().Pods(m.namespace).List(metav1.ListOptions{LabelSelector: m.resLabel})
		if err != nil {
			klog.Error(err)
			return false, err
		}
		klog.V(2).Infof("Found %d pods\n", len(podsList.Items))

		bulker := dns.GetBulker(m.dnsClient)
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

func (m RecordsManager) podUpdated(oldObj, newObj interface{}) {
	newPod := newObj.(*v1.Pod)
	klog.V(2).Infof("Pod updated: %s\n", newPod.Name)

	/*
		Pod update handler is triggered quite often and for things
		we don't care about here. So we keep in memory list of pods that
		we know that record hasn't been created.
	*/
	_, isPendingIP := m.pendingIP[newPod.GetName()]
	if isPendingIP && newPod.Status.PodIP != "" {
		m.dnsClient.CreateRecord(newPod.GetName(), newPod.GetOwnerReferences()[0].Name, newPod.Status.PodIP)
		delete(m.pendingIP, newPod.GetName())
	}
}

// Handler for pod creation
func (m RecordsManager) podCreated(obj interface{}) {
	pod := obj.(*v1.Pod)
	klog.V(2).Infof("Pod created: %s", pod.GetName())
	var err error

	/*
		Needs to wait for slow services to be ready but
		not block everything if pod fails for whatever reasons.
		Either pod updated event handler or fallback sync jobs
		should catch those missing DNS recrods.
	*/
	if pod.Status.PodIP == "" {
		klog.Warningln("Pod IP missing. Will try to resolve.")
		wait.Poll(2*time.Second, m.timeout, func() (bool, error) {
			pod, err := m.kubeClient.CoreV1().Pods(pod.Namespace).Get(pod.GetName(), metav1.GetOptions{})
			if err != nil {
				klog.Error(err)
				return false, nil
			}
			if pod.Status.PodIP != "" {
				klog.V(2).Infof("Pod IP resolved: %s\n", pod.Status.PodIP)
				return true, nil
			}
			return false, nil
		})

		pod, err = m.kubeClient.CoreV1().Pods(pod.Namespace).Get(pod.GetName(), metav1.GetOptions{})
		if err != nil {
			klog.Error(err)
		}

		// Leave if for the pod updated event handler
		if err != nil || pod.Status.PodIP == "" {
			klog.V(2).Infof("Failed get pod IP in %s\n", m.timeout)
			m.pendingIP[pod.GetName()] = pod
			return
		}
	}

	m.dnsClient.CreateRecord(pod.GetName(), pod.GetOwnerReferences()[0].Name, pod.Status.PodIP)
}

// Handler for pod deletion events
func (m RecordsManager) podDeleted(obj interface{}) {
	pod := obj.(*v1.Pod)
	klog.V(2).Infof("Pod deleted: %s", pod.GetName())

	m.dnsClient.DeleteRecord(pod.GetName(), pod.GetOwnerReferences()[0].Name, pod.Status.PodIP)
}
