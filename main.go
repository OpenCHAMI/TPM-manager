package main

import (
	"context"
	"errors"
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
	port := flag.Int("port", 27780, "port on which to listen for POSTs")
	interval := flag.Duration("interval", 30*time.Second, "how frequently to run Ansible, regardless of buffer length")
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

	// Configure HTTP server
	server := &http.Server{Addr: fmt.Sprintf(":%d", *port)}
	http.HandleFunc("/Node", func(w http.ResponseWriter, r *http.Request) {
		respondNodePost(w, r, &nodes)
	})

	// Create a waitgroup for Ansible child processes
	var wg sync.WaitGroup

	// Launch the node-slice watcher and HTTP server
	go watchNodes(&nodes, *interval, *batchSize, playbook, &wg)
	go func() {
		log.Info().Msgf("Awaiting HTTP POST requests on %s...", server.Addr)
		err := server.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatal().Msgf("HTTP server error: %v", err)
			os.Exit(1)
		}
		log.Debug().Msg("HTTP server shutdown complete")
	}()

	// Exit cleanly when an OS interrupt signal is received
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT)
	log.Info().Msg("Interrupt (^C) to exit")

	log.Info().Msgf("Caught OS signal %v, exiting once all Ansible runs finish...", <-sigs)
	// Shut down HTTP server
	ctx, release := context.WithTimeout(context.Background(), 10*time.Second)
	defer release()
	err := server.Shutdown(ctx)
	if err != nil {
		log.Error().Msgf("HTTP server shutdown error: %v", err)
		log.Info().Msg("Forcibly closing HTTP server")
		server.Close()
	}
	// Process any nodes left in the buffer
	nodes.Lock()
	nodeLen := len(nodes.slice)
	nodes.Unlock()
	if nodeLen > 0 {
		log.Info().Msgf("%d nodes remain in buffer!", nodeLen)
		runAnsiblePlaybook(playbook, &nodes, &wg)
	}

	// Stop handling OS signals, allowing for an unclean exit if interrupted again
	signal.Stop(sigs)
	// Ensure all Ansible runs have finished (we might be interrupted by an OS signal instead)
	wg.Wait()
	log.Info().Msg("Exited cleanly")
}

func respondNodePost(w http.ResponseWriter, r *http.Request, nodes *SafeUpdatingSlice) {
	// TODO: Log errors from this?
	if r.Method == http.MethodPost {
		// Validate POSTed data; should be of
		// Content-Type: application/x-www-form-urlencoded
		err := r.ParseForm()
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, err.Error())
			return
		}
		nodeName := r.FormValue("data")
		if nodeName == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "data field must not be empty")
			return
		}
		// Add our node to the slice, and send a length update to anyone watching
		nodes.Lock()
		nodes.slice = append(nodes.slice, nodeName)
		numNodes := len(nodes.slice)
		nodes.Unlock()
		log.Debug().Msgf("Buffer updated: %d nodes", numNodes)
		nodes.length <- numNodes
		fmt.Fprintf(w, "Acknowledged")
	} else {
		w.WriteHeader(http.StatusMethodNotAllowed)
		fmt.Fprintf(w, "This endpoint must be POSTed to")
	}
}

func watchNodes(nodes *SafeUpdatingSlice, interval time.Duration, batchSize int, playbook *string, wg *sync.WaitGroup) {
	// Register a SIGHUP handler
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	// And a timer
	timer := time.NewTicker(interval)

	// Launch Ansible against current set of nodes, when either:
	//   - Slice has reached batch size
	//   - Interval has expired
	//   - SIGHUP is received
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
		case <-sighup:
			timer.Reset(interval)
			log.Debug().Msg("Caught a SIGHUP!")
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
	log.Info().Msgf("Launching Ansible against %v", nodes.slice)

	nodes.Lock()
	// Compose our Ansible launch command, in exec form
	// A trailing comma is necessary for a single node, and fine for multiple nodes
	ansibleArgs := []string{*playbook, "--inventory", strings.Join(nodes.slice, ",") + ","}
	// Clear node list, since we've launched Ansible
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
		log.Error().Err(err).Msg("An Ansible error occurred!")
	}
}
