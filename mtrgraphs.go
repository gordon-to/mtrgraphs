package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api/write"
	"mtr-graphs/ping"
)

var saveToInfluxDB func(point *write.Point)

type pingTarget struct {
	ip      string
	reply   chan bool
	ttl     int
	rttOnce sync.Once
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

	rttCh := make(chan *pingTarget, 50)
	ttlCh := make(chan *pingTarget, 50)
	// Start 60 workers to ping hosts
	for range 60 {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case tgt := <-rttCh:
					rtt, reply := ping.Rtt(ctx, tgt.ip)
					if reply {
						influxPoint := influxdb2.NewPointWithMeasurement("ping")
						influxPoint.AddTag("host", tgt.ip)
						influxPoint.AddField("rtt", rtt)
						saveToInfluxDB(influxPoint)
					}
					tgt.reply <- reply
				case tgt := <-ttlCh:
					for {
						dst, reply := ping.Ttl(ctx, tgt.ip, tgt.ttl)
						tgt.ttl++
						if !reply {
							continue
						}
						monitorHost(ctx, &pingTarget{ip: dst, reply: make(chan bool)}, rttCh)
						if dst == tgt.ip {
							break
						}
					}
					tgt.reply <- true
				}
			}
		}()
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				<-time.After(10 * time.Second)
				hostsLock.Lock()
				numberOfHosts := len(availableHosts)
				fmt.Printf("Available hosts: %d, %v \n", numberOfHosts, availableHosts)
				hostsLock.Unlock()
				influxPoint := influxdb2.NewPointWithMeasurement("available_hosts")
				influxPoint.AddField("count", numberOfHosts)
				saveToInfluxDB(influxPoint)
			}
		}
	}()

	cidrRanges := os.Args[1:]
	for _, cidr := range cidrRanges {
		// Scan the network
		ip, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			fmt.Println("Error parsing CIDR:", err)
			return
		}
		for ip = ip.Mask(ipNet.Mask); ipNet.Contains(ip); incrementIP(ip) {
			fmt.Printf("Scanning IP: %s\n", ip.String())
			go traceIp(ctx, ip.String(), ttlCh, rttCh)
		}
	}

	<-ctx.Done()
	close(ttlCh)
	close(rttCh)
}

func traceIp(ctx context.Context, ip string, ttlCh, rttCh chan<- *pingTarget) {
	tgt := &pingTarget{ip: ip, reply: make(chan bool), ttl: 2}
	defer close(tgt.reply)
	for {
		select {
		case <-ctx.Done():
			return
		default:
			rttCh <- tgt
			found := <-tgt.reply
			if found {
				ttlCh <- tgt
				<-tgt.reply
			}
			<-time.After(10*time.Minute + (time.Duration(rand.Intn(100)) * time.Millisecond))
		}
	}
}

func monitorHost(ctx context.Context, target *pingTarget, rttCh chan<- *pingTarget) {
	hostsLock.Lock()
	defer hostsLock.Unlock()
	if _, ok := availableHosts[target.ip]; ok {
		return
	}
	availableHosts[target.ip] = struct{}{}
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
					fmt.Println("Host", target.ip, "is unreachable")
					delete(availableHosts, target.ip)
					hostsLock.Unlock()
					return
				}
				<-time.After(10 * time.Second)
			}
		}
	}()
}

func initInfluxDB(ctx context.Context) func(*write.Point) {
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
	return func(p *write.Point) {
		writeAPI.WritePoint(p)
		fmt.Printf("Wrote point: %v\n", p)
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
