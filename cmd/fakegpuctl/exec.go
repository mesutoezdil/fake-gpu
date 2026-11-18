package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
)

// runExec runs a command and keeps one card busy for exactly as long as it
// runs, so work that really happens on the CPU (a HIP-CPU build of a kernel,
// a stand-in for a training job) shows up as GPU load. It returns the
// command's exit code.
func runExec(client *http.Client, api string, index int, opts, command []string, errOut io.Writer) int {
	query := url.Values{"gpu": {strconv.Itoa(index)}}
	for _, kv := range opts {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			fmt.Fprintf(errOut, "fakegpuctl: %q is not key=value\n", kv)
			return 2
		}
		query.Set(k, v)
	}
	var g gpu
	if err := call(client, http.MethodPost, api+"/api/actions/busy?"+query.Encode(), &g); err != nil {
		fmt.Fprintln(errOut, "fakegpuctl:", err)
		return 1
	}
	// Whatever happens to the command, the card goes back to idle.
	defer call(client, http.MethodPost, api+"/api/actions/idle?gpu="+strconv.Itoa(index), &g)

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(errOut, "fakegpuctl:", err)
		return 127
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	go func() {
		for s := range signals {
			cmd.Process.Signal(s)
		}
	}()

	err := cmd.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if code := exit.ExitCode(); code >= 0 {
			return code
		}
		return 128 + int(exit.Sys().(syscall.WaitStatus).Signal()) // killed by a signal
	}
	if err != nil {
		fmt.Fprintln(errOut, "fakegpuctl:", err)
		return 1
	}
	return 0
}
