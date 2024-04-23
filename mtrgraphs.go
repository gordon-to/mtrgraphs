package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/influxdata/influxdb-client-go/v2"
)

const mtrCycles = "10"

var mtrTargets []string

var saveToInfluxDB func(string)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// read all targets from args
	if len(os.Args) > 1 {
		mtrTargets = os.Args[1:]
	} else {
		log.Fatalf("No targets provided\n")
	}

	saveToInfluxDB = initInfluxDB(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		default:
			for _, target := range mtrTargets {
				// run mtr command
				runMtr(ctx, target)
			}
			// wait 10 seconds before running mtr again
			<-time.After(10 * time.Second)
		}
	}
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
	var output bytes.Buffer
	cmd := exec.CommandContext(ctx, "mtr", "-c", mtrCycles, "-r", "-j", target)
	cmd.Stderr = os.Stderr
	cmd.Stdout = &output
	err := cmd.Run()
	if err != nil {
		log.Fatalf("Error running mtr command: %v\n", err)
	}
	res := mtrReport{}
	err = json.Unmarshal(output.Bytes(), &res)
	if err != nil {
		log.Fatalf("Error parsing mtr output: %v\n", err)
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
