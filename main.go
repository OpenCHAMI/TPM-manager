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
	// Write our logs to stderr, leaving stdout for Ansible messages
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
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

	// Create a waitgroup for Ansible child processes
	var wg sync.WaitGroup

	// Launch the HTTP and node-slice watchers
	log.Info().Msgf("Awaiting POST requests on port %d...", *port)
	go http.ListenAndServe(fmt.Sprintf(":%d", *port), nil)
	go watchNodes(&nodes, *interval, *batchSize, playbook, &wg)

	// Exit cleanly when an OS interrupt signal is received
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT)
	log.Info().Msg("Interrupt (^C) to exit")
	log.Info().Msgf("Caught OS signal %v, exiting once all Ansible runs finish...", <-sigs)
	// TODO: Shut down HTTP server
	wg.Wait()
	log.Info().Msg("Exited cleanly")
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

func watchNodes(nodes *SafeUpdatingSlice, interval time.Duration, batchSize int, playbook *string, wg *sync.WaitGroup) {
	timer := time.NewTicker(interval)

	// Launch token push to current set of nodes, when either:
	//   - Slice has reached batch size
	//   - Interval has expired
	for {
		select {
		case nodeLen := <-nodes.length:
			log.Debug().Msg("Caught a buffer update!")
			if nodeLen >= batchSize {
				timer.Reset(interval)
				runAnsiblePlaybook(playbook, nodes, wg)
			} else {
				log.Debug().Msgf("Buffer now contains %d nodes; not launching yet", nodeLen)
			}
		case <-timer.C:
			log.Debug().Msg("Caught a timer tick!")
			nodes.Lock()
			nodeLen := len(nodes.slice)
			nodes.Unlock()
			if nodeLen > 0 {
				runAnsiblePlaybook(playbook, nodes, wg)
			} else {
				log.Debug().Msg("No nodes in buffer; skipping launch")
			}
		}
	}
}

func runAnsiblePlaybook(playbook *string, nodes *SafeUpdatingSlice, wg *sync.WaitGroup) {
	log.Info().Msgf("Launching token push to %v", nodes.slice)

	nodes.Lock()
	// Compose our Ansible launch command, in exec form
	// A trailing comma is necessary for a single node, and fine for multiple nodes
	ansibleArgs := []string{*playbook, "--inventory", strings.Join(nodes.slice, ",") + ","}
	// Clear node list, since we've launched the token push
	nodes.slice = nil
	nodes.Unlock()

	// Parallelize our Ansible runs
	wg.Add(1)
	go ansibleHost(&ansibleArgs, wg)
}

func ansibleHost(args *[]string, wg *sync.WaitGroup) {
	defer wg.Done()

	// Launch Ansible
	log.Debug().Msgf("Launching Ansible with %v", *args)
	ansible := exec.Command("ansible-playbook", *args...)
	// Don't die when the main process is SIGINT'd
	ansible.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Print all Ansible messages to stdout, since we use stderr for our own logging
	ansible.Stdout = os.Stdout
	ansible.Stderr = os.Stdout
	if err := ansible.Run(); err != nil {
		log.Error().Err(err).Msg("Failed to launch Ansible!")
	}
}
