package main

import (
	"flag"
	"fmt"
	"net/http"
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

	// Configure HTTP endpoints
	http.HandleFunc("/Node", func(w http.ResponseWriter, r *http.Request) {
		respondNodePost(w, r, &nodes)
	})

	// Launch the HTTP and node-slice watchers
	log.Info().Msgf("Awaiting POST requests on port %d...", *port)
	go http.ListenAndServe(fmt.Sprintf(":%d", *port), nil)
	go watchNodes(&nodes, *interval, *batchSize)

	// Exit cleanly when an OS signal is received
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	log.Info().Msgf("Caught OS signal %v, exiting...", <-sigs)
}

func respondNodePost(w http.ResponseWriter, r *http.Request, nodes *SafeUpdatingSlice) {
	if r.Method == http.MethodPost {
		// TODO: Add actual validation logic here, once we know our data format
		// Read (up to 100 chars of) the request body
		body := make([]byte, 100)
		length, _ := r.Body.Read(body)
		if length != 0 {
			// Add our node to the slice, and send a length update to anyone watching
			nodes.Lock()
			nodes.slice = append(nodes.slice, string(body))
			numNodes := len(nodes.slice)
			nodes.Unlock()
			log.Debug().Msgf("Slice updated: %d nodes", numNodes)
			nodes.length <- numNodes
			fmt.Fprintf(w, "Acknowledged")
		} else {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "A real error message will go here, once we know our data format")
		}
	} else {
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprintf(w, "This endpoint must be POSTed to")
	}
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
				doTokenPush(nodes)
			} else {
				log.Trace().Msgf("Only %d nodes; skipping launch", nodeLen)
			}
		case <-timer.C:
			log.Debug().Msg("Timer launch")
			if len(nodes.slice) > 0 {
				doTokenPush(nodes)
			} else {
				log.Trace().Msg("No nodes; skipping launch")
			}
		}
	}
}

func doTokenPush(nodes *SafeUpdatingSlice) {
	// TODO: Fetch a token from opaal and add it to the environment somehow
	log.Trace().Msgf("Launching token push to %v", nodes.slice)

	nodes.Lock()
	// Compose our Ansible launch command, in exec form
	// A trailing comma is necessary for a single node, and fine for multiple nodes
	launchCmd := append([]string{"ansible-playbook", "main.yaml"}, "-i", strings.Join(nodes.slice, ",")+",")
	// Clear node list, since we've launched the token push
	nodes.slice = nil
	nodes.Unlock()

	// TODO: How do exec?
	log.Trace().Msg("Would run: " + strings.Join(launchCmd, " "))
}
