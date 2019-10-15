package dns

import (
	"fmt"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/dns/v1"
	"io/ioutil"
	"log"
	"strings"
	"time"
)

// CloudDNS is a wrapper for GCP SDK api to hold relevant conf
type CloudDNS struct {
	dnsSvc      *dns.Service
	zone        string
	project     string
	domain      string
	debug       bool
	shortFormat bool
}

// FromJSON creaties DNS client instance with JSON key file
func FromJSON(filePath, zone, project, domain string, shortFormat, debug bool) *CloudDNS {
	dat, err := ioutil.ReadFile(filePath)
	if err != nil {
		panic(err)
	}

	conf, err := google.JWTConfigFromJSON(dat, dns.NdevClouddnsReadwriteScope)
	if err != nil {
		panic(err)
	}
	dnsSvc, err := dns.New(conf.Client(oauth2.NoContext))
	if err != nil {
		panic(err)
	}
	return &CloudDNS{dnsSvc: dnsSvc, zone: zone, project: project, domain: domain, debug: debug, shortFormat: shortFormat}
}

func (client CloudDNS) getRec(name, owner, ip string) *dns.ResourceRecordSet {
	var nameField string
	if client.shortFormat {
		nameField = fmt.Sprintf("%s.%s.", name, client.domain)
	} else {
		nameField = fmt.Sprintf("%s.%s.%s.", name, owner, client.domain)
	}

	return &dns.ResourceRecordSet{
		Name:    nameField,
		Rrdatas: []string{ip},
		Ttl:     int64(60),
		Type:    "A",
	}
}

// DeleteRecord deletes a record
func (client CloudDNS) DeleteRecord(name, owner, ip string) {
	rec := client.getRec(name, owner, ip)

	// We get the existing record from the DNS zone to check if it exists
	list, err := client.dnsSvc.ResourceRecordSets.List(client.project, client.zone).Name(rec.Name).Type(rec.Type).MaxResults(1).Do()

	if err != nil {
		panic(err)
	}

	if len(list.Rrsets) == 0 {
		if client.debug {
			log.Printf("No DNS record found for %s/%s", rec.Name, ip)
		}
		return
	}

	// If records and pods have somehow got into inconsistent state
	// we avoid deleting records that don't match the event.
	if ip != list.Rrsets[0].Rrdatas[0] {
		if client.debug {
			log.Printf("No DNS record found for %s with the same IP (%s)", rec.Name, ip)
		}
		return
	}

	if client.debug {
		log.Printf("Deleting record: %+v\n", rec)
	}

	change := &dns.Change{
		Deletions: []*dns.ResourceRecordSet{rec},
	}

	_, err = client.dnsSvc.Changes.Create(client.project, client.zone, change).Do()
	if err != nil {
		panic(err)
	}
}

// CreateRecord creates a record
func (client CloudDNS) CreateRecord(name, owner, ip string) {
	rec := client.getRec(name, owner, ip)
	// Look for existing records.
	list, err := client.dnsSvc.ResourceRecordSets.List(client.project, client.zone).Name(rec.Name).Type(rec.Type).MaxResults(1).Do()
	if err != nil {
		panic(err)
	}

	change := &dns.Change{
		Additions: []*dns.ResourceRecordSet{rec},
	}

	if len(list.Rrsets) > 0 {
		if rec.Name == list.Rrsets[0].Name && rec.Rrdatas[0] == list.Rrsets[0].Rrdatas[0] {
			if client.debug {
				log.Printf("Record exists: %+v\n", rec)
			}
			return
		}
		if rec.Name == list.Rrsets[0].Name && rec.Rrdatas[0] != list.Rrsets[0].Rrdatas[0] {
			// Just a safeguard for case there is some stale record
			if client.debug {
				log.Printf("Stale record found: %+v\n", list.Rrsets[0])
			}
			change.Deletions = []*dns.ResourceRecordSet{list.Rrsets[0]}
		}
	}

	if client.debug {
		log.Printf("Creating record: %+v\n", rec)
	}

	chg, err := client.dnsSvc.Changes.Create(client.project, client.zone, change).Do()
	if err != nil {
		panic(err)
	}

	// wait for change to be acknowledged
	for chg.Status == "pending" {
		time.Sleep(time.Second)

		chg, err = client.dnsSvc.Changes.Get(client.project, client.zone, chg.Id).Do()
		if err != nil {
			panic(err)
		}
	}
}

// BulkSync struct exists to reducse GCP API requests
// when running fallback job to check that DNS records
// exists for all the pods that are supposed to have them
type BulkSync struct {
	client *CloudDNS
	list   map[string]*dns.ResourceRecordSet
}

// GetBulker returns BulkSync instance with loaded DNS list
func GetBulker(client *CloudDNS) *BulkSync {
	bulker := BulkSync{client: client}
	bulker.loadList()
	return &bulker
}

// DeleteRemaining deletes all stale records
// Assumes that bulk.list only has stale records
func (bulk BulkSync) DeleteRemaining() {
	if len(bulk.list) == 0 {
		return
	}

	var deletions []*dns.ResourceRecordSet
	for _, rec := range bulk.list {
		deletions = append(deletions, rec)
	}

	if bulk.client.debug {
		log.Printf("%d stale records found\n", len(deletions))
	}

	change := &dns.Change{
		Deletions: deletions,
	}

	_, err := bulk.client.dnsSvc.Changes.Create(bulk.client.project, bulk.client.zone, change).Do()
	if err != nil {
		panic(err)
	}
}

// CheckNext checks next item from loaded DNS records
func (bulk BulkSync) CheckNext(name, owner, ip string) {
	// Check that record exists for the given pod with given IP
	rec, found := bulk.list[name]
	if found && rec.Rrdatas[0] == ip {
		if bulk.client.debug {
			log.Printf("Record found for %s:%v\n", name, rec)
		}
		delete(bulk.list, name)
	} else {
		bulk.client.CreateRecord(name, owner, ip)
	}
}

func (bulk BulkSync) loadList() {
	wholeZoneResponse, err := bulk.client.dnsSvc.ResourceRecordSets.List(bulk.client.project, bulk.client.zone).Do()
	if err != nil {
		panic(err)
	}

	list := make(map[string]*dns.ResourceRecordSet)
	for _, rec := range wholeZoneResponse.Rrsets {
		if strings.HasSuffix(rec.Name, bulk.client.domain+".") {
			name := rec.Name[:strings.IndexByte(rec.Name, '.')]
			list[name] = rec
			if bulk.client.debug {
				log.Printf("Found DNS record for %s:%s", name, rec.Name)
			}
		}
	}
	bulk.list = list
}
