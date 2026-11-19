// capture-profile writes a fake-gpu profile for the AMD GPUs of this machine,
// read from the amdgpu sysfs tree. Run it on real hardware, then start the
// mock elsewhere with the result: node-agent --config captured.yaml
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/mesutoezdil/fake-gpu/pkg/capture"
	"github.com/mesutoezdil/fake-gpu/pkg/profile"
	"gopkg.in/yaml.v3"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("capture-profile", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "/", "filesystem root holding sys/ and proc/")
	out := flags.String("o", "", "output file (default stdout)")
	var o capture.Options
	flags.StringVar(&o.BDF, "gpu", "", "PCI address of the reference GPU, e.g. 0000:03:00.0 (default: the one with most VRAM)")
	flags.StringVar(&o.Name, "name", "", "marketing name to store in the profile")
	flags.StringVar(&o.Architecture, "arch", "", "architecture label, e.g. cdna3 or rdna4")
	flags.BoolVar(&o.KeepIDs, "keep-ids", false, "keep the real per-card unique IDs (they identify this hardware)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "capture-profile: unexpected arguments %v\n", flags.Args())
		return 2
	}

	p, notes, err := capture.Capture(*root, o)
	if err != nil {
		fmt.Fprintln(stderr, "capture-profile:", err)
		return 1
	}
	text, err := format(p, notes)
	if err != nil {
		fmt.Fprintln(stderr, "capture-profile:", err)
		return 1
	}
	for _, note := range notes {
		fmt.Fprintln(stderr, "note:", note)
	}
	if *out == "" {
		_, err = io.WriteString(stdout, text)
	} else {
		err = os.WriteFile(*out, []byte(text), 0o644)
	}
	if err != nil {
		fmt.Fprintln(stderr, "capture-profile:", err)
		return 1
	}
	return 0
}

// format renders the profile as YAML under a comment header with the notes.
func format(p *profile.Profile, notes []string) (string, error) {
	body, err := yaml.Marshal(p)
	if err != nil {
		return "", err
	}
	text := fmt.Sprintf("# Captured from %d x %s\n", len(p.Devices), p.Card.Name)
	for _, note := range notes {
		text += "# NOTE: " + note + "\n"
	}
	return text + string(body), nil
}
