// capture-profile writes a fake-gpu profile for the AMD GPUs of this machine,
// read from the amdgpu sysfs tree. Run it on real hardware, then start the
// mock elsewhere with the result: node-agent --config captured.yaml
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mesutoezdil/fake-gpu/pkg/gpu/kfd"
	"gopkg.in/yaml.v3"
)

func main() {
	root := flag.String("root", "/", "filesystem root holding sys/ and proc/")
	out := flag.String("o", "", "output file (default stdout)")
	var o kfd.CaptureOptions
	flag.StringVar(&o.BDF, "gpu", "", "PCI address of the reference GPU, e.g. 0000:03:00.0 (default: the one with most VRAM)")
	flag.StringVar(&o.Name, "name", "", "marketing name to store in the profile")
	flag.StringVar(&o.Architecture, "arch", "", "architecture label, e.g. cdna3 or rdna4")
	flag.BoolVar(&o.KeepIDs, "keep-ids", false, "keep the real per-card unique IDs (they identify this hardware)")
	flag.Parse()

	profile, notes, err := kfd.CaptureProfile(*root, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "capture-profile:", err)
		os.Exit(1)
	}
	body, err := yaml.Marshal(profile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "capture-profile:", err)
		os.Exit(1)
	}
	text := fmt.Sprintf("# Captured from %d x %s\n", len(profile.Devices), profile.DeviceDefault.Name)
	for _, note := range notes {
		text += "# NOTE: " + note + "\n"
		fmt.Fprintln(os.Stderr, "note:", note)
	}
	text += string(body)
	if *out == "" {
		fmt.Print(text)
	} else if err := os.WriteFile(*out, []byte(text), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "capture-profile:", err)
		os.Exit(1)
	}
}
