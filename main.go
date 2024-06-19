package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type SafeUpdatingSlice struct {
	sync.Mutex
	slice []string
	len   chan int
}

func main() {
	port := 27730
	batchSize := 100
	nodes := SafeUpdatingSlice{}

	fmt.Printf("Awaiting POST requests on port %d...\n", port)
	go listenPosts(&nodes, port)
	go watchNodes(&nodes, 5*time.Minute, batchSize)
}

func listenPosts(nodes *SafeUpdatingSlice, port int) {
	// FIXME: This is for testing only, until I figure out the Chi router
	time.Sleep(5 * time.Second)

	// Add our node to the slice, and send a length update to anyone watching
	nodes.Lock()
	// FIXME: For now, we use a list of multiple nodes, so that we can test with more than just one Ansible target
	exampleNodes := []string{"nid001", "nid002"}
	nodes.slice = append(nodes.slice, exampleNodes...)
	numNodes := len(nodes.slice)
	nodes.Unlock()
	nodes.len <- numNodes
}

func watchNodes(nodes *SafeUpdatingSlice, interval time.Duration, batchSize int) {
	timer := time.NewTicker(interval)

	// Launch token push to current list of nodes, when either:
	//   - Interval has expired
	//   - List reaches capacity
	for {
		select {
		case nodeLen := <-nodes.len:
			if nodeLen >= batchSize {
				nodes.Lock()
				doTokenPush(nodes.slice)
				nodes.Unlock()
			}
		case <-timer.C:
			nodes.Lock()
			if len(nodes.slice) > 0 {
				doTokenPush(nodes.slice)
			}
			nodes.Unlock()
		}
	}
}

func doTokenPush(hostnames []string) {
	// TODO: Fetch a token from opaal and add it to the environment somehow

	// Compose our Ansible launch command, in exec form
	// A trailing comma is necessary for a single node, and fine for multiple nodes
	launchCmd := append([]string{"ansible-playbook", "main.yaml"}, "-i", strings.Join(hostnames, ",")+",")
	// TODO: How do exec?
}
