package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/influxdata/influxdb-client-go/v2"
)

const mtrCycles = "10"

var saveToInfluxDB func(string)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer func() {
		recover()
		stop()
	}()
	saveToInfluxDB = initInfluxDB(ctx)

	// read all targets from args
	cidr := os.Args[1]
	// Scan the network
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		fmt.Println("Error parsing CIDR:", err)
		return
	}
	for ip := ip.Mask(ipnet.Mask); ipnet.Contains(ip); incrementIP(ip) {
		go func(ip string) {
			var cancel context.CancelFunc
			for {
				select {
				case <-ctx.Done():
					if cancel != nil {
						cancel()
					}
					return
				default:
					// send ping to check if the host is reachable
					if sendPing(ip) {
						var childCtx context.Context
						childCtx, cancel = context.WithTimeout(ctx, 10*time.Minute)
						go func() {
							for {
								select {
								case <-childCtx.Done():
									return
								default:
									runMtr(ctx, ip)
								}
								<-time.After(1 * time.Minute)
							}
						}()
						<-childCtx.Done()
					} else {
						// wait for 10 minutes before retrying the same IP
						<-time.After(10 * time.Minute)
					}

				}
			}
		}(ip.String())
	}
	<-ctx.Done()
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
		writeAPI.WriteRecord(line)
	}

}

// runMtr runs the mtr command with the given target and parses the output as JSON.
func runMtr(ctx context.Context, target string) {
	// create byte buffer to store the output
	cmd := exec.CommandContext(ctx, "mtr", "-c", mtrCycles, "-r", "-j", target)
	output, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("Error running mtr command: %v\n", err)
	}
	res := mtrReport{}
	err = json.Unmarshal(output, &res)
	if err != nil {
		log.Printf("Error parsing mtr output: %v\n", err)
	}
	for _, hub := range res.Report.Hubs {
		influxDbRecord := fmt.Sprintf("mtr,src=%s,dst=%s,host=%s Loss=%f,Snt=%d,Last=%f,Avg=%f,Best=%f,Wrst=%f,StDev=%f\n",
			res.Report.Mtr.Src, res.Report.Mtr.Dst, hub.Host, hub.Loss, hub.Snt, hub.Last, hub.Avg, hub.Best, hub.Wrst, hub.StDev)
		saveToInfluxDB(influxDbRecord)
		fmt.Println(influxDbRecord)
	}
}

type mtrReport struct {
	Report struct {
		Mtr struct {
			Src        string `json:"src"`
			Dst        string `json:"dst"`
			Tos        int    `json:"tos"`
			Tests      int    `json:"tests"`
			Psize      string `json:"psize"`
			Bitpattern string `json:"bitpattern"`
		} `json:"mtr"`
		Hubs []struct {
			Count int     `json:"count"`
			Host  string  `json:"host"`
			Loss  float64 `json:"Loss%"`
			Snt   int     `json:"Snt"`
			Last  float64 `json:"Last"`
			Avg   float64 `json:"Avg"`
			Best  float64 `json:"Best"`
			Wrst  float64 `json:"Wrst"`
			StDev float64 `json:"StDev"`
		} `json:"hubs"`
	} `json:"report"`
}

func sendPing(ip string) bool {
	cmd := exec.Command("ping", "-c", "1", ip)
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("Host: %s, No reply or error: %s\n", ip, err)
		return true // Continue tracing until max hops
	}
	fmt.Printf("Host: %s, Reply: %s\n", ip, output)
	return string(output) != "" && !contains(output, "1 packets received")
}

func contains(b []byte, s string) bool {
	return bytes.Contains(b, []byte(s))
}

func incrementIP(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}
