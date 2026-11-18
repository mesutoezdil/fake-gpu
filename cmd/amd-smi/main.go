// amd-smi is a stand-in for the AMD SMI CLI that reports the mock GPUs. It
// reads the same snapshots as the mock libamd_smi.so and prints a subset of
// the real tool's subcommands in the same layout. Invoked as rocm-smi it
// prints the legacy summary table.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/mesutoezdil/fake-gpu/pkg/gpu/kfd"
)

const usage = "usage: amd-smi {list|static|metric|monitor|bad-pages|version} [--json] [--gpu N]"

func main() { os.Exit(run(os.Args, os.Stdout, os.Stderr)) }

func run(args []string, out, errOut io.Writer) int {
	dir := os.Getenv("MOCK_AMDSMI_STATE_DIR")
	if dir == "" {
		dir = "/var/lib/fake-gpu/smi"
	}
	cmd := "monitor"
	if filepath.Base(args[0]) != "rocm-smi" {
		if len(args) < 2 {
			fmt.Fprintln(errOut, usage)
			return 2
		}
		cmd, args = args[1], args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(errOut)
	asJSON := fs.Bool("json", false, "print JSON")
	only := fs.Int("gpu", -1, "limit output to one GPU index")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if cmd == "version" {
		fmt.Fprintln(out, "AMDSMI Tool: fake-gpu mock | AMDSMI Library version: mock")
		return 0
	}
	gpus, err := kfd.ReadSMIState(dir)
	if err != nil {
		fmt.Fprintf(errOut, "amd-smi: no mock GPU state in %s: %v\n", dir, err)
		return 1
	}
	if *only >= 0 {
		if *only >= len(gpus) {
			fmt.Fprintf(errOut, "amd-smi: GPU %d not found\n", *only)
			return 1
		}
		gpus = gpus[*only : *only+1]
	}
	view, ok := views[cmd]
	if !ok {
		fmt.Fprintln(errOut, usage)
		return 2
	}
	if cmd == "monitor" && !*asJSON {
		monitor(out, gpus)
		return 0
	}
	if *asJSON {
		rows := make([]section, len(gpus))
		for i, g := range gpus {
			rows[i] = append(section{{"gpu", g.Index}}, view(g)...)
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "    ")
		enc.Encode(rows)
		return 0
	}
	for _, g := range gpus {
		fmt.Fprintf(out, "GPU: %d\n", g.Index)
		printSection(out, view(g), "    ")
		fmt.Fprintln(out)
	}
	return 0
}

// section is an ordered map: a value is a scalar, a quantity or a section.
type section []struct {
	Key string
	Val any
}

type quantity struct {
	Value int    `json:"value"`
	Unit  string `json:"unit"`
}

func (s section) MarshalJSON() ([]byte, error) {
	buf := []byte{'{'}
	for i, kv := range s {
		if i > 0 {
			buf = append(buf, ',')
		}
		k, _ := json.Marshal(kv.Key)
		v, err := json.Marshal(kv.Val)
		if err != nil {
			return nil, err
		}
		buf = append(append(append(buf, k...), ':'), v...)
	}
	return append(buf, '}'), nil
}

func printSection(out io.Writer, s section, indent string) {
	for _, kv := range s {
		key := upper(kv.Key)
		switch v := kv.Val.(type) {
		case section:
			fmt.Fprintf(out, "%s%s:\n", indent, key)
			printSection(out, v, indent+"    ")
		case quantity:
			fmt.Fprintf(out, "%s%s: %d %s\n", indent, key, v.Value, v.Unit)
		default:
			fmt.Fprintf(out, "%s%s: %v\n", indent, key, v)
		}
	}
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

var views = map[string]func(kfd.GPUState) section{
	"list": func(g kfd.GPUState) section {
		return section{{"bdf", g.PCIBDF}, {"uuid", g.UUID}, {"node_id", g.Index + 1}}
	},
	"static": func(g kfd.GPUState) section {
		return section{
			{"asic", section{{"market_name", g.Name}}},
			{"bus", section{{"bdf", g.PCIBDF}}},
			{"limit", section{{"max_power", quantity{g.PowerCapW, "W"}}}},
			{"vram", section{{"size", quantity{g.MemoryTotalMB, "MB"}}}},
		}
	},
	"metric":  metric,
	"monitor": metric,
	"bad-pages": func(g kfd.GPUState) section {
		return section{{"retired", g.RetiredPages}}
	},
}

func metric(g kfd.GPUState) section {
	throttle := "UNTHROTTLED"
	if g.Throttled {
		throttle = "THROTTLED"
	}
	return section{
		{"status", g.Status},
		{"usage", section{{"gfx_activity", quantity{g.UtilizationPct, "%"}}}},
		{"power", section{{"socket_power", quantity{g.PowerW, "W"}}, {"throttle_status", throttle}}},
		{"clock", section{{"gfx_0", section{{"clk", quantity{g.ClockGFXMHz, "MHz"}}}}, {"mem_0", section{{"clk", quantity{g.ClockMemMHz, "MHz"}}}}}},
		{"temperature", section{{"edge", quantity{g.TemperatureC, "°C"}}}},
		{"ecc", section{{"total_uncorrectable_count", g.ECCErrors}}},
		{"mem_usage", section{
			{"total_vram", quantity{g.MemoryTotalMB, "MB"}},
			{"used_vram", quantity{g.MemoryUsedMB, "MB"}},
			{"free_vram", quantity{g.MemoryTotalMB - g.MemoryUsedMB, "MB"}},
		}},
	}
}

func monitor(out io.Writer, gpus []kfd.GPUState) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "GPU\tPOWER\tGPU_TEMP\tGFX_UTIL\tGFX_CLOCK\tMEM_CLOCK\tVRAM_USED\tVRAM_TOTAL\tSTATUS\t")
	for _, g := range gpus {
		fmt.Fprintf(w, "%d\t%d W\t%d °C\t%d %%\t%d MHz\t%d MHz\t%d MB\t%d MB\t%s\t\n",
			g.Index, g.PowerW, g.TemperatureC, g.UtilizationPct, g.ClockGFXMHz, g.ClockMemMHz, g.MemoryUsedMB, g.MemoryTotalMB, g.Status)
	}
	w.Flush()
}
