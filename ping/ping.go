package ping

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

const maxHops = 128

var ttlRegex = regexp.MustCompile(`^From ([0-9.]+)`)
var rttRegex = regexp.MustCompile(`time=([0-9.]+)`)

func Ttl(ctx context.Context, ip string, ttl int) (string, bool) {
	if ttl > maxHops {
		return "", false
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "ping", "-c", "1", "-t", strconv.Itoa(ttl), ip)
	output, err := cmd.CombinedOutput()
	// check if the output contains the TTL exceeded message
	if ttlRegex.Match(output) {
		return ttlRegex.FindStringSubmatch(string(output))[1], true
	}
	if err != nil {
		fmt.Printf("TTL Host: %s, No reply or error: %s\n", ip, err)
		return "", true // Continue tracing until max hops
	}
	fmt.Printf("TTL: Host: %s, Reply: %s\n", ip, output)
	return ip, string(output) != "" && !contains(output, "1 packets received")
}

func Rtt(ctx context.Context, ip string) (float64, bool) {
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "ping", "-c", "1", ip)
	output, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("RTT: Host: %s, No reply or error: %s\n", ip, err)
		return 0, false
	}
	if rttRegex.Match(output) {
		rtt, err := strconv.ParseFloat(rttRegex.FindStringSubmatch(string(output))[1], 64)
		if err != nil {
			fmt.Printf("Host: %s, Error parsing RTT: %s\n", ip, err)
			return 0, false
		}
		return rtt, true
	}
	fmt.Printf("Host: %s, Reply: %s\n", ip, output)
	return 0, false
}

func contains(b []byte, s string) bool {
	return bytes.Contains(b, []byte(s))
}
