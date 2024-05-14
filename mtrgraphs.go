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

	"mtr-graphs/ping"

	"github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api/write"
)

var saveToInfluxDB func(point *write.Point)

const baseDelay = 5 * time.Second
const maxFails = 10

type pingTarget struct {
	ip    string
	reply chan bool
	ttl   int
	fails int
}

var availableHosts sync.Map

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
				currentHosts := make([]string, 0)
				availableHosts.Range(func(key, value any) bool {
					currentHosts = append(currentHosts, key.(string))
					return true
				})
				fmt.Printf("Available hosts: %d\n %v\n", len(currentHosts), currentHosts)
				influxPoint := influxdb2.NewPointWithMeasurement("available_hosts")
				influxPoint.AddField("count", len(currentHosts))
				saveToInfluxDB(influxPoint)
			}
		}
	}()

	cidrRanges := os.Args[1:]
	var targets []*pingTarget
	for _, cidr := range cidrRanges {
		// Scan the network
		ip, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			fmt.Println("Error parsing CIDR:", err)
			return
		}
		for ip = ip.Mask(ipNet.Mask); ipNet.Contains(ip); incrementIP(ip) {
			targets = append(targets, &pingTarget{
				ip:    ip.String(),
				reply: make(chan bool),
				ttl:   2,
			})
		}
	}
	f := func() {
		for _, t := range targets {
			go func(tgt *pingTarget) {
				if _, loaded := availableHosts.Load(tgt.ip); loaded {
					return
				}
				rttCh <- tgt
				if found := <-tgt.reply; found {
					ttlCh <- tgt
				}
			}(t)
		}
	}
	f()
	tkr := time.NewTicker(10 * time.Minute)
	for range tkr.C {
		f()
	}

	<-ctx.Done()
	tkr.Stop()
	close(ttlCh)
	close(rttCh)
}

func monitorHost(ctx context.Context, target *pingTarget, rttCh chan<- *pingTarget) {
	if _, loaded := availableHosts.LoadOrStore(target.ip, struct{}{}); loaded {
		return
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				rttCh <- target
				reply := <-target.reply
				if !reply {
					target.fails++

					influxPoint := influxdb2.NewPointWithMeasurement("drop")
					influxPoint.AddTag("host", target.ip)
					influxPoint.AddField("fails", target.fails)
					saveToInfluxDB(influxPoint)

					if target.fails >= maxFails {
						availableHosts.Delete(target.ip)
						fmt.Println("Host", target.ip, "is unreachable")
						return
					}
				} else {
					target.fails = 0
				}
				<-time.After(baseDelay)
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
