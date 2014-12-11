package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/mitchellh/goamz/aws"
	"github.com/mitchellh/goamz/ec2"
	r53 "github.com/mitchellh/goamz/route53"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings"
)

var (
	etcdAddress    string
	etcdPrefix     string
	tagName        string
	tagValuePrefix string
	stackName      string
	dnsZone        string
	delay          int
	verbose        bool
)

const (
	maxMachineIndex  = 100
	maxEtcdRedirects = 10
)

func main() {
	/*
	  parse args
	  read /etc/machine-id
	  connect etcd
	  find or grab an index under etcd /prefix and write machine-id into it
	  determine aws region and instance-id from metadata
	  connect aws (using IAM role granted to instance)
	  tag instance as {prefix}{index}
	  write A record {prefix}{index} into R53 zone
	*/
	parseFlags()
	if !strings.HasPrefix(etcdPrefix, "/") {
		log.Fatal("etcd-prefix must start with `/`, got `%s`", etcdPrefix)
	}
	if !strings.HasSuffix(dnsZone, ".") {
		dnsZone = dnsZone + "."
	}

	mid, err := machineId()
	if err != nil {
		log.Fatal(err)
	}

	index, err := findIndex(mid)
	if err != nil {
		log.Fatal(err)
	}

	publicIp, err := metadata("public-ipv4")
	if err != nil {
		log.Fatal(err)
	}
	instance, err := metadata("instance-id")
	if err != nil {
		log.Fatal(err)
	}
	availabilityZone, err := metadata("placement/availability-zone")
	if err != nil {
		log.Fatal(err)
	}
	region := availabilityZone[0 : len(availabilityZone)-1]

	if verbose {
		log.Printf("machine id = %v", mid)
		log.Printf("index = %d", index)
		log.Printf("region = %v", region)
	}

	auth, err := aws.GetAuth("", "")
	if err != nil {
		log.Fatal(err)
	}
	_region := aws.Regions[region]
	if tagName != "" {
		tag(ec2.New(auth, _region), instance, index)
	}
	if dnsZone != "" {
		dns(r53.New(auth, _region), publicIp, index)
	}
}

func parseFlags() {
	flag.StringVar(&etcdAddress, "etcd", "localhost:4001", "The ETCD endpoint")
	flag.StringVar(&etcdPrefix, "etcd-prefix", "/cloudtag", "The directory in ETCD to use for machine index allocation")
	flag.StringVar(&tagName, "tag-name", "Name", "The name of the AWS tag to set")
	flag.StringVar(&tagValuePrefix, "tag-value-prefix", "machine-", "The prefix to which machine index will be appended")
	flag.StringVar(&stackName, "stack-name", "", "The name of the stack")
	flag.StringVar(&dnsZone, "dns-zone", "", "The Route53 DNS zone to insert machine record into")
	flag.IntVar(&delay, "delay", 0, "If >0 then the tag is set again after number of seconds to combat CloudFormation reseting the Name tag")
	flag.BoolVar(&verbose, "verbose", false, "Print debug if true")
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: cloudtag [-etcd host[:port]] [-etcd-prefix /cloudtag] [-tag-name Name] [-tag-value-prefix machine-] [-stack-name stack-123] [-dns-zone cloud.some] [-delay 0]\n")
		fmt.Fprint(os.Stderr, "\tName tag = {stack-name-}{machine-}{index}\n")
		fmt.Fprint(os.Stderr, "\tDNS name = {machine-}{index}{.stack-name}{.dns-zone}\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
}

func machineId() (string, error) {
	const path = "/etc/machine-id"
	_id, err := ioutil.ReadFile(path)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(_id))
	if id == "" {
		return "", errors.New("Empty machine id read from " + path)
	}
	return id, nil
}

func findIndex(mid string) (index int, err error) {
	for i := 1; i < maxMachineIndex; i++ {
		maybe, err := get(i)
		if err != nil {
			return 0, nil
		}
		if verbose {
			log.Printf("index %d -> %v", i, maybe)
		}
		if maybe == mid {
			return i, nil
		} else if maybe == "" {
			return allocateIndex(mid, i)
		}
	}
	return 0, errors.New(fmt.Sprintf("Cannot find machine index - all slots are busy, checked %d slots", maxMachineIndex))
}

func allocateIndex(mid string, start int) (index int, err error) {
	for i := start; i < maxMachineIndex; i++ {
		ok, err := put(mid, i)
		if err != nil {
			return 0, err
		}
		if ok {
			return i, nil
		}
	}
	return 0, errors.New(fmt.Sprintf("Cannot allocate machine index - all slots are busy, checked %d slots", maxMachineIndex))
}

type EtcdNode struct {
	Key   string
	Value string
}

type EtcdOp struct {
	Action string
	Node   EtcdNode
}

func etcdUrl(etcdAddress string, etcdPrefix string, tagValuePrefix string, tagName string, index int) string {
	return fmt.Sprintf("http://%s/v2/keys%s/%s%s/%d", etcdAddress, etcdPrefix, tagValuePrefix, tagName, index)
}

func get(index int) (id string, err error) {
	url := etcdUrl(etcdAddress, etcdPrefix, tagValuePrefix, tagName, index)
	if verbose {
		log.Printf("getting %v", url)
	}
	res, err := http.Get(url)
	if verbose {
		log.Printf("got %+v %v", res, err)
	}
	if err != nil {
		return
	}
	if res.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if res.StatusCode != http.StatusOK {
		return "", errors.New(fmt.Sprintf("Don't know how to handle ETCD reply %+v", res))
	}
	bin, err := ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return
	}
	if verbose {
		log.Printf("body %s", bin)
	}
	var j EtcdOp
	err = json.Unmarshal(bin, &j)
	if err != nil {
		return
	}
	if verbose {
		log.Printf("json %+v", j)
	}
	return j.Node.Value, nil
}

func put(mid string, index int) (ok bool, err error) {
	url := etcdUrl(etcdAddress, etcdPrefix, tagValuePrefix, tagName, index) + "?prevExist=false"
	if verbose {
		log.Printf("putting %v", url)
	}
	put := true
	redirects := 0
	var res *http.Response
	for put {
		if redirects > maxEtcdRedirects {
			return false, errors.New(fmt.Sprintf("Too much redirects (%d) from ETCD while creating key %v", maxEtcdRedirects, url))
		}
		req, err := http.NewRequest("PUT", url, strings.NewReader("value="+mid))
		if err != nil {
			return false, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if verbose {
			log.Printf("sending %+v", req)
		}
		res, err = http.DefaultClient.Do(req)
		if verbose {
			log.Printf("got %+v %v", res, err)
		}
		if err != nil {
			return false, err
		}
		if res.StatusCode == http.StatusTemporaryRedirect {
			masterUrl, err := res.Location()
			if err != nil {
				return false, err
			}
			url = masterUrl.String()
			redirects++
		} else {
			put = false
		}
	}
	if res.StatusCode == http.StatusPreconditionFailed {
		return false, nil
	}
	if res.StatusCode != http.StatusCreated {
		return false, errors.New(fmt.Sprintf("Don't know how to handle ETCD reply %+v", res))
	}
	return true, nil
}

func metadata(what string) (value string, err error) {
	res, err := http.Get("http://169.254.169.254/latest/meta-data/" + what)
	if err != nil {
		return
	}
	bin, err := ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return
	}
	value = strings.TrimSpace(string(bin))
	if verbose {
		log.Printf("metadata %v -> %v", what, value)
	}
	if value == "" {
		return "", errors.New(fmt.Sprintf("Empty instance metadata %v", what))
	}
	return
}

func tag(ec2c *ec2.EC2, instance string, index int) {
	var _stack string
	if stackName != "" {
		_stack = stackName + "-"
	}
	value := fmt.Sprintf("%s%s%d", _stack, tagValuePrefix, index)
	_, err := ec2c.CreateTags([]string{instance}, []ec2.Tag{ec2.Tag{Key: tagName, Value: value}})
	if err != nil {
		log.Fatal(err)
	}
}

func dns(r53c *r53.Route53, publicIp string, index int) {
	res, err := r53c.ListHostedZones("", 0)
	if err != nil {
		log.Fatal(err)
	}
	var zoneId string
	for _, zone := range res.HostedZones { // hope the response is not truncated
		if verbose {
			log.Printf("DNS zone %v -> %v", zone.Name, zone.ID)
		}
		if zone.Name == dnsZone {
			zoneId = zone.ID
			break
		}
	}
	if zoneId == "" {
		log.Printf("Cannot determine DNS zone ID of %s, trying '%[1]s' as ID", dnsZone)
		zoneId = dnsZone
	}
	var _stack string
	if stackName != "" {
		_stack = "." + stackName
	}
	record := fmt.Sprintf("%s%d%s.%s", tagValuePrefix, index, _stack, dnsZone)
	_, err = r53c.ChangeResourceRecordSets(zoneId,
		&r53.ChangeResourceRecordSetsRequest{Changes: []r53.Change{r53.Change{Action: "UPSERT", Record: r53.ResourceRecordSet{Name: record, Type: "A", TTL: 300, Records: []string{publicIp}}}}})
	if err != nil {
		log.Fatal(err)
	}
}
