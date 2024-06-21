package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/diode"
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
	playbook := flag.String("playbook", "main.yaml", "Ansible playbook to run against nodes")
	debug := flag.Bool("debug", false, "sets log level to debug")
	flag.Parse()

	// Initialize logging
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	dwr := diode.NewWriter(os.Stderr, 1000, 10*time.Millisecond,
		func(missed int) { fmt.Printf("Logger dropped %d messages", missed) })
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        dwr,
		TimeFormat: time.RFC3339})
	if *debug {
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	} else {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}

	// Configure HTTP endpoints
	http.HandleFunc("/Node", func(w http.ResponseWriter, r *http.Request) {
		respondNodePost(w, r, &nodes)
	})

	// Launch the HTTP and node-slice watchers
	log.Info().Msgf("Awaiting POST requests on port %d...", *port)
	go http.ListenAndServe(fmt.Sprintf(":%d", *port), nil)
	go watchNodes(&nodes, *interval, *batchSize, playbook)

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
			log.Debug().Msgf("Buffer updated: %d nodes", numNodes)
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

func watchNodes(nodes *SafeUpdatingSlice, interval time.Duration, batchSize int, playbook *string) {
	timer := time.NewTicker(interval)

	// Launch token push to current set of nodes, when either:
	//   - Slice has reached batch size
	//   - Interval has expired
	for {
		select {
		case nodeLen := <-nodes.length:
			log.Info().Msg("Caught a buffer update!")
			if nodeLen >= batchSize {
				timer.Reset(interval)
				runAnsiblePlaybook(playbook, nodes)
			} else {
				log.Debug().Msgf("Buffer now contains %d nodes; not launching yet", nodeLen)
			}
		case <-timer.C:
			log.Info().Msg("Caught a timer tick!")
			if len(nodes.slice) > 0 {
				runAnsiblePlaybook(playbook, nodes)
			} else {
				log.Debug().Msg("No nodes in buffer; skipping launch")
			}
		}
	}
}

func runAnsiblePlaybook(playbook *string, nodes *SafeUpdatingSlice) {
	log.Info().Msgf("Launching token push to %v", nodes.slice)

	nodes.Lock()
	// Compose our Ansible launch command, in exec form
	// A trailing comma is necessary for a single node, and fine for multiple nodes
	ansibleArgs := []string{*playbook, "--inventory", strings.Join(nodes.slice, ",")+","}
	// Clear node list, since we've launched the token push
	nodes.slice = nil
	nodes.Unlock()

	// Launch Ansible
	log.Debug().Msgf("Launching Ansible with %v", ansibleArgs)
	ansible := exec.Command("ansible-playbook", ansibleArgs...)
	ansible.Run()
}
