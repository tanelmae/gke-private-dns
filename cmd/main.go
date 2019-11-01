package main

import (
	"flag"
	"fmt"
	"github.com/tanelmae/gke-private-dns/internal"
	"github.com/tanelmae/gke-private-dns/pkg/dns"
	"io/ioutil"
	"log"
	"net/http"
	"time"
)

func getMetadata(urlPath string) (string, error) {
	client := &http.Client{}
	req, err := http.NewRequest("GET",
		fmt.Sprintf("http://metadata/computeMetadata/v1/%s", urlPath), nil)
	req.Header.Add("Metadata-Flavor", "Google")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()

	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	return string(bodyBytes), nil
}

func main() {
	log.Println("service started")

	namespace := flag.String("namespace", "default", "Namespace in which to watch resource")
	resLabel := flag.String("label", "", "Resource label to watch")
	domain := flag.String("domain", "", "Domain used for generated DNS records")
	zone := flag.String("zone", "", "GCP DNS zone where to write the records")
	saFile := flag.String("sa-file", "", "Path to GCP service account credentials")
	project := flag.String("project", "", "GCP project where the DNS zone is. Defaults to the same as GKE cluster.")
	debug := flag.Bool("debug", false, "Run in debug mode")
	shortFormat := flag.Bool("short-format", false, "Omit owner name fron the DNS record")
	timeout := flag.Duration("timeout", time.Minute, "How long to wait for pod IP to be available")
	syncInterval := flag.Duration("fallback-sync-interval", time.Minute*30, "Interval for fallback sync jobs")
	watcherResync := flag.Duration("watcher-sync-interval", time.Minute*10, "Interval for fallback sync jobs")
	flag.Set("logtostderr", "true")
	flag.Parse()

	var (
		gcpProject string
		err        error
	)
	if *project == "" {
		for i := 1; i <= 3; i++ {
			gcpProject, err = getMetadata("project/project-id")
			if err != nil {
				if *debug {
					log.Printf("Reading GCP project name from metadata failed")
				}
				time.Sleep(time.Second * time.Duration(i))
				if *debug {
					log.Printf("Will try again reading GCP project name from metadata")
				}
			} else {
				break
			}
		}
		if gcpProject == "" {
			panic("Failed to resolve GCP project from instance metadata")
		}
	} else {
		gcpProject = *project
	}

	// JSON key file for service account with DNS admin permissions
	dnsClient := dns.FromJSON(*saFile, *zone, gcpProject, *domain, *shortFormat, *debug)
	if *debug {
		log.Printf("DNS client: %+v\n", dnsClient)
	}
	internal.Run(*namespace, *resLabel, *syncInterval, *watcherResync, *timeout, dnsClient, *debug)
}
