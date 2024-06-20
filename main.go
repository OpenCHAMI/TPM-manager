package main

import (
	"flag"
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
	nodes := SafeUpdatingSlice{length: make(chan int)}

	// Configure and parse arguments
	port := flag.Int("port", 27730, "port on which to listen for POSTs")
	interval := flag.Duration("interval", 5*time.Minute, "how frequently to push tokens, regardless of buffer length")
	batchSize := flag.Int("batch-size", 100, "how full the node buffer must be to trigger a non-timed push")
	trace := flag.Bool("trace", false, "sets log level to trace")
	flag.Parse()
	if *trace {
		zerolog.SetGlobalLevel(zerolog.TraceLevel)
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	// Initialize logging
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	go listenPosts(&nodes, *port)
	go watchNodes(&nodes, *interval, *batchSize)

	// Exit cleanly when an OS signal is received
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	log.Info().Msgf("Caught OS signal %v, exiting...", <-sigs)
}

func listenPosts(nodes *SafeUpdatingSlice, port int) {
	log.Info().Msgf("Awaiting POST requests on port %d...", port)

	// FIXME: This is for testing only, until I figure out the Chi router
	time.Sleep(5 * time.Second)

	// Add our node to the slice, and send a length update to anyone watching
	nodes.Lock()
	// FIXME: For now, we use a list of multiple nodes, so that we can test with more than just one Ansible target
	exampleNodes := []string{"nid001", "nid002"}
	nodes.slice = append(nodes.slice, exampleNodes...)
	numNodes := len(nodes.slice)
	nodes.Unlock()
	log.Debug().Msgf("Pushing update: %d nodes", numNodes)
	nodes.length <- numNodes
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
	launchCmd := append([]string{"ansible-playbook", "main.yaml"}, "-i", strings.Join(*hostnames, ",")+",")
	// Clear node list, since we've launched the token push
	*hostnames = nil

	// TODO: How do exec?
	log.Trace().Msg("Would run: " + strings.Join(launchCmd, " "))
}
