// Copyright 2017 The TensorFlow Authors. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// ==============================================================================

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/tpu/v1alpha1"
)

var legacyNetwork = net.IPv4(10, 240, 0, 0)

func selectCIDRBlock(routes []*compute.Route, cidrBlockSize uint, network string) (string, error) {
	cidrBlocks := make([]*net.IPNet, 0, len(routes))
	for _, i := range routes {
		// Filter out network ranges that are not peered with our GCP VPC Network.
		if !strings.HasSuffix(i.Network, network) {
			fmt.Println("continue")
			continue
		}
		_, ipNet, err := net.ParseCIDR(i.DestRange)
		if err != nil {
			return "", err
		}
		maskSize, _ := ipNet.Mask.Size()
		if maskSize < 8 {
			continue
		}
		if legacyNetwork.Equal(ipNet.IP) && maskSize <= 16 {
			return "", fmt.Errorf("Cloud TPUs cannot be used with legacy networks, please create a new GCP project")
		}
		if maskSize <= 16 && ipNet.Contains(net.IPv4(10, 240, 1, 1)) && ipNet.Contains(net.IPv4(10, 240, 250, 250)) {
			return "", fmt.Errorf("existing routing entries appear to entirely cover the IP-range ctpu uses")
		}
		cidrBlocks = append(cidrBlocks, ipNet)
	}

	fourthOctetIncrement := 1 << (32 - cidrBlockSize)

	rand.Seed(time.Now().UnixNano())

	// Select a random IP address.
	for {
		thirdOctet := byte(rand.Intn(254) + 1)
		fourthOctet := byte(rand.Intn((255/fourthOctetIncrement)*fourthOctetIncrement + 1))
		candidateIPAddress := net.IPv4(10, 240, thirdOctet, fourthOctet)
		for _, block := range cidrBlocks {
			if block.Contains(candidateIPAddress) {
				continue
			}
		}
		_, newCidr, err := net.ParseCIDR(fmt.Sprintf("%s/%d", candidateIPAddress.String(), cidrBlockSize))
		if err != nil {
			return "", fmt.Errorf("error parsing constructed CIDR: %v", err)
		}
		split := strings.Split(newCidr.String(), "/")
		if len(split) != 2 {
			return "", fmt.Errorf("error parsing cidr block %q", newCidr.String())
		}
		return newCidr.String(), nil
	}

	// for thirdOctet := byte(1); thirdOctet < 255; thirdOctet++ {
	// nextCandidate:
	// 	for fourthOctetBase := 1; fourthOctetBase < 255; fourthOctetBase += fourthOctetIncrement {
	// 		for candidateFourthOctet := fourthOctetBase; candidateFourthOctet < fourthOctetBase+fourthOctetIncrement; candidateFourthOctet += 2 {
	// 			candidateIPAddress := net.IPv4(10, 240, thirdOctet, byte(candidateFourthOctet))
	// 			for _, block := range cidrBlocks {
	// 				if block.Contains(candidateIPAddress) {
	// 					continue nextCandidate
	// 				}
	// 			}
	// 		}
	// 		candidateIPAddress := net.IPv4(10, 240, thirdOctet, byte(fourthOctetBase))
	// 		_, newCidr, err := net.ParseCIDR(fmt.Sprintf("%s/%d", candidateIPAddress.String(), cidrBlockSize))
	// 		if err != nil {
	// 			return "", fmt.Errorf("error parsing constructed CIDR: %v", err)
	// 		}
	// 		split := strings.Split(newCidr.String(), "/")
	// 		if len(split) != 2 {
	// 			return "", fmt.Errorf("error parsing cidr block %q", newCidr.String())
	// 		}
	// 		return newCidr.String(), nil
	// 	}
	// }
	// return "", errors.New("no available CIDR blocks found")
}

// TODO: handle cidr block sizes larger than 24 bits.
var tpuDeviceNetworkSizes = map[string]uint{
	"v2-8":    29,
	"v2-32":   29,
	"v2-128":  27,
	"v2-256":  26,
	"v2-512":  25,
	"v3-8":    29,
	"v3-32":   29,
	"v3-64":   28,
	"v3-128":  27,
	"v3-256":  26,
	"v3-512":  25,
	"v3-1024": 24,
	// "v3-2048": 24,  // TODO(saeta): Support full-size pods.
}

// cidrBlockSize returns the number of ones in the CIDR range, or an error.
func cidrBlockSize(hardwareType string) (ones uint, err error) {
	cidrBits, present := tpuDeviceNetworkSizes[hardwareType]
	if !present {
		return 0, fmt.Errorf("unknown TPU device size %q", hardwareType)
	}
	return cidrBits, nil
}

func printCidrBlock(network string, project string, tpuHardware string) error {

	var client *http.Client
	var err error
	var ctx = context.Background()

	client, err = google.DefaultClient(ctx, compute.ComputeScope, tpu.CloudPlatformScope)
	if err != nil {
		return err
	}

	var userAgent = fmt.Sprintf("ctpu/%s env/%s", "1.9-dev", "gcloud")

	computeService, err := compute.New(client)
	if err != nil {
		return err
	}
	computeService.UserAgent = userAgent

	routeItems := make([]*compute.Route, 0)
	err = computeService.Routes.List(project).Pages(ctx, func(routeList *compute.RouteList) error {
		routeItems = append(routeItems, routeList.Items...)
		return nil
	})
	if err != nil {
		return err
	}

	cidrBlockSize, err := cidrBlockSize(tpuHardware)
	if err != nil {
		return err
	}

	cidrBlock, err := selectCIDRBlock(routeItems, cidrBlockSize, network)
	if err != nil {
		return err
	}
	fmt.Println(cidrBlock)
	return nil
}

func main() {
	network := flag.String("network", "default", "help")
	project := flag.String("project", "PLEASE-PROVIDE-A-PROJECT", "help")
	tpu := flag.String("tpu", "v3-8", "help")

	flag.Parse()

	err := printCidrBlock(*network, *project, *tpu)
	if err != nil {
		log.Fatal(err)
	}
}
