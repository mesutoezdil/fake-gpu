// fakegpuctl drives the fake-gpu control API from a shell.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

const usage = `usage: fakegpuctl [-api URL] list
       fakegpuctl [-api URL] <action> -gpu N [key=value ...]

actions: crash recover busy idle overheat ecc-error throttle retire-page xgmi-down
options: busy util=95 mem=70, overheat temp=105, ecc-error count=2, retire-page count=2
The API address defaults to $FAKE_GPU_API or http://localhost:8080.`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errOut io.Writer) int {
	def := os.Getenv("FAKE_GPU_API")
	if def == "" {
		def = "http://localhost:8080"
	}
	global := flag.NewFlagSet("fakegpuctl", flag.ContinueOnError)
	global.SetOutput(errOut)
	global.Usage = func() { fmt.Fprintln(errOut, usage) }
	api := global.String("api", def, "control API address")
	if err := global.Parse(args); err != nil || global.NArg() == 0 {
		if err == nil {
			global.Usage()
		}
		return 2
	}
	client := &http.Client{Timeout: 10 * time.Second}
	cmd, rest := global.Arg(0), global.Args()[1:]

	if cmd == "list" {
		var body struct {
			GPUs []gpu `json:"gpus"`
		}
		if err := call(client, http.MethodGet, *api+"/api/gpus", &body); err != nil {
			fmt.Fprintln(errOut, "fakegpuctl:", err)
			return 1
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "GPU\tNAME\tSTATUS\tTEMP\tPOWER\tUTIL\tECC\tRETIRED")
		for _, g := range body.GPUs {
			fmt.Fprintf(w, "%d\t%s\t%s\t%d C\t%d W\t%d %%\t%d\t%d\n", g.Index, g.Name, g.Status, g.TemperatureC, g.PowerW, g.UtilizationPct, g.ECCErrors, g.RetiredPages)
		}
		w.Flush()
		return 0
	}

	sub := flag.NewFlagSet(cmd, flag.ContinueOnError)
	sub.SetOutput(errOut)
	index := sub.Int("gpu", -1, "GPU index")
	if err := sub.Parse(rest); err != nil {
		return 2
	}
	query := url.Values{}
	for _, kv := range sub.Args() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			fmt.Fprintf(errOut, "fakegpuctl: %q is not key=value\n", kv)
			return 2
		}
		query.Set(k, v)
	}
	if *index < 0 {
		fmt.Fprintln(errOut, "fakegpuctl: -gpu N is required")
		return 2
	}
	query.Set("gpu", fmt.Sprint(*index))
	var g gpu
	if err := call(client, http.MethodPost, *api+"/api/actions/"+url.PathEscape(cmd)+"?"+query.Encode(), &g); err != nil {
		fmt.Fprintln(errOut, "fakegpuctl:", err)
		return 1
	}
	fmt.Fprintf(out, "GPU %d: %s (%d C, %d W, %d %%)\n", g.Index, g.Status, g.TemperatureC, g.PowerW, g.UtilizationPct)
	return 0
}

type gpu struct {
	Index          int    `json:"index"`
	Name           string `json:"name"`
	Status         string `json:"status"`
	TemperatureC   int    `json:"temperature_c"`
	PowerW         int    `json:"power_w"`
	UtilizationPct int    `json:"utilization_pct"`
	ECCErrors      int    `json:"ecc_errors"`
	RetiredPages   int    `json:"retired_pages"`
}

func call(client *http.Client, method, target string, into any) error {
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, into)
}
