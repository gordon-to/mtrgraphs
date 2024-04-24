package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/influxdata/influxdb-client-go/v2"
	"mtr-graphs/ping"
)

var saveToInfluxDB func(string)

type pingTarget struct {
	ip    string
	reply chan bool
}

var availableHosts = make(map[string]struct{})
var hostsLock sync.Mutex

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer func() {
		recover()
		stop()
	}()
	saveToInfluxDB = initInfluxDB(ctx)

	// create 10 goroutines to send rtt pings
	rttCh := make(chan *pingTarget, 10)
	for range 10 {
		go func() {
			for tgt := range rttCh {
				rtt, reply := ping.Rtt(ctx, tgt.ip)
				if !reply {
					tgt.reply <- false
					break
				}
				influxDbRecord := fmt.Sprintf("ping,dst=%s,rtt=%f\n", tgt.ip, rtt)
				saveToInfluxDB(influxDbRecord)
				tgt.reply <- true
			}
		}()
	}
	// create 10 goroutines to send ttl pings
	ttlCh := make(chan *pingTarget, 10)
	for range 10 {
		go func() {
			for tgt := range ttlCh {
				ttl := 1
				for {
					dst, reply := ping.Ttl(ctx, tgt.ip, ttl)
					if !reply {
						tgt.reply <- false
						break
					}
					monitorHost(ctx, &pingTarget{ip: dst, reply: make(chan bool)}, rttCh)
					if dst == tgt.ip {
						tgt.reply <- true
						break
					}
					<-time.After(10 * time.Millisecond)
					ttl++
				}
			}
		}()
	}

	cidrRanges := os.Args[1:]
	for _, cidr := range cidrRanges {
		// Scan the network
		ip, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			fmt.Println("Error parsing CIDR:", err)
			return
		}
		for ip = ip.Mask(ipNet.Mask); ipNet.Contains(ip); incrementIP(ip) {
			go traceIp(ctx, ip.String(), ttlCh)
		}
	}

	<-ctx.Done()
	close(ttlCh)
	close(rttCh)
}

func traceIp(ctx context.Context, ip string, ttlCh chan<- *pingTarget) {
	tgt := &pingTarget{ip: ip, reply: make(chan bool)}
	defer close(tgt.reply)
	for {
		select {
		case <-ctx.Done():
			return
		default:
			ttlCh <- tgt
			<-tgt.reply
			<-time.After(10 * time.Minute)
		}
	}
}

func monitorHost(ctx context.Context, target *pingTarget, rttCh chan<- *pingTarget) {
	hostsLock.Lock()
	if _, ok := availableHosts[target.ip]; ok {
		hostsLock.Unlock()
		return
	}
	availableHosts[target.ip] = struct{}{}
	hostsLock.Unlock()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				rttCh <- target
				reply := <-target.reply
				if !reply {
					hostsLock.Lock()
					delete(availableHosts, target.ip)
					hostsLock.Unlock()
					return
				}
				<-time.After(10 * time.Second)
			}
		}
	}()
}

func initInfluxDB(ctx context.Context) func(string) {
	// initialize influxdb connection
	dbURL := os.Getenv("INFLUXDB_URL")
	dbToken := os.Getenv("INFLUXDB_TOKEN")
	org := os.Getenv("INFLUXDB_ORG")
	bucket := os.Getenv("INFLUXDB_BUCKET")

	// create influxdb client
	// create write api
	client := influxdb2.NewClient(dbURL, dbToken)
	writeAPI := client.WriteAPI(org, bucket)
	go func() {
		<-ctx.Done()
		writeAPI.Flush()
		client.Close()
	}()
	go func() {
		for err := range writeAPI.Errors() {
			log.Fatalf("Error writing to InfluxDB, %v\n", err)
		}
	}()
	return func(line string) {
		fmt.Println(line)
		writeAPI.WriteRecord(line)
	}

}

func incrementIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}
