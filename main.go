package main

import (
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type SafeUpdatingSlice struct {
	sync.Mutex
	slice  []string
	length chan int
}

func main() {
	port := 27730
	batchSize := 100
	nodes := SafeUpdatingSlice{length: make(chan int)}

	// Initialize logging
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	log.Info().Msgf("Awaiting POST requests on port %d...", port)
	go listenPosts(&nodes, port)
	go watchNodes(&nodes, 5*time.Minute, batchSize)

	// Exit cleanly when an OS signal is received
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	log.Info().Msgf("Caught OS signal %v, exiting...", <-sigs)
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
	nodes.length <- numNodes
	log.Debug().Msgf("Pushed update: %d nodes", numNodes)
}

func watchNodes(nodes *SafeUpdatingSlice, interval time.Duration, batchSize int) {
	timer := time.NewTicker(interval)

	// Launch token push to current list of nodes, when either:
	//   - Interval has expired
	//   - List reaches capacity
	for {
		select {
		case nodeLen := <-nodes.length:
			log.Debug().Msg("Batch-size update")
			if nodeLen >= batchSize {
				nodes.Lock()
				doTokenPush(&nodes.slice)
				nodes.Unlock()
			} else {
				log.Trace().Msgf("Only %d nodes; skipping launch", nodeLen)
			}
		case <-timer.C:
			log.Debug().Msg("Timer launch")
			nodes.Lock()
			if len(nodes.slice) > 0 {
				doTokenPush(&nodes.slice)
			} else {
				log.Trace().Msg("No nodes; skipping launch")
			}
			nodes.Unlock()
		}
	}
}

func doTokenPush(hostnames *[]string) {
	// NOTE: The hostnames slice should be locked by external logic

	// TODO: Fetch a token from opaal and add it to the environment somehow
	log.Trace().Msgf("Launching token push to %v", *hostnames)

	// Compose our Ansible launch command, in exec form
	// A trailing comma is necessary for a single node, and fine for multiple nodes
	launchCmd := append([]string{"ansible-playbook", "main.yaml"}, "-i", strings.Join(hostnames, ",")+",")
	// TODO: How do exec?

	// The hostnames slice should be locked from outside this function, so we
	// can safely mutate it (in this case, clear it for later re-use)
	*hostnames = nil
}
