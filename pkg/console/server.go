// Package console serves the node agent's HTTP API, its Prometheus text
// metrics and the web console. Every action changes the fleet first and
// then writes the GPU's new state into the rendered tree.
package console

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/mesutoezdil/fake-gpu/pkg/fleet"
	"github.com/mesutoezdil/fake-gpu/pkg/sysfs"
)

// Server is the API of one node. Its tree may be nil, then only the fleet
// changes.
type Server struct {
	fleet *fleet.Fleet
	tree  *sysfs.Tree
	mux   *http.ServeMux
}

// New wires the routes.
func New(f *fleet.Fleet, t *sysfs.Tree) *Server {
	s := &Server{fleet: f, tree: t, mux: http.NewServeMux()}
	routes := map[string]http.HandlerFunc{
		"/api/gpus":                 s.gpus,
		"/api/gpus/":                s.gpu,
		"/api/system":               s.system,
		"/api/actions/crash":        s.act(func(i int, _ query) (fleet.GPU, error) { return f.Crash(i) }),
		"/api/actions/recover":      s.act(func(i int, _ query) (fleet.GPU, error) { return f.Recover(i) }),
		"/api/actions/idle":         s.act(func(i int, _ query) (fleet.GPU, error) { return f.Recover(i) }),
		"/api/actions/busy":         s.act(func(i int, q query) (fleet.GPU, error) { return f.Busy(i, q.percent("util", 85), q.percent("mem", 70)) }),
		"/api/actions/overheat":     s.act(func(i int, q query) (fleet.GPU, error) { return f.Overheat(i, q.int("temp", 105)) }),
		"/api/actions/ecc-error":    s.act(func(i int, q query) (fleet.GPU, error) { return f.ECC(i, q.count(1<<62)) }),
		"/api/actions/throttle":     s.act(func(i int, _ query) (fleet.GPU, error) { return f.Throttle(i) }),
		"/api/actions/retire-page":  s.act(func(i int, q query) (fleet.GPU, error) { return f.RetirePages(i, q.count(sysfs.MaxBadPages)) }),
		"/api/actions/xgmi-down":    s.act(func(i int, _ query) (fleet.GPU, error) { return f.XGMIDown(i) }),
		"/api/profiles":             s.profiles,
		"/api/profiles/switch":      s.fixed(s.switchProfile),
		"/api/profiles/switch-tray": s.fixed(s.switchTray),
		"/api/trays":                s.trays,
		"/api/trays/layout":         s.trayLayout,
		"/api/partitions":           s.partitions,
		"/api/partitions/set":       s.fixed(s.setPartition),
		"/metrics":                  s.metrics,
		"/healthz":                  s.healthz,
		"/":                         s.console,
	}
	for pattern, h := range routes {
		s.mux.HandleFunc(pattern, h)
	}
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// ListenAndServe serves on addr until it fails.
func (s *Server) ListenAndServe(addr string) error {
	log.Printf("API server listening on %s", addr)
	return http.ListenAndServe(addr, s)
}

// query reads the optional parameters of an action, with a default for
// anything missing or out of range.
type query struct{ r *http.Request }

func (q query) int(key string, def int) int {
	if v, err := strconv.Atoi(q.r.URL.Query().Get(key)); err == nil {
		return v
	}
	return def
}

func (q query) percent(key string, def int) int {
	if v := q.int(key, -1); v >= 0 && v <= 100 {
		return v
	}
	return def
}

// count is the "count" parameter, 1 unless it is between 1 and most.
func (q query) count(most int) int {
	if v := q.int("count", 0); v > 0 && v <= most {
		return v
	}
	return 1
}

// index is the ?gpu=N every action needs.
func index(r *http.Request) (int, bool) {
	i, err := strconv.Atoi(r.URL.Query().Get("gpu"))
	return i, err == nil && i >= 0
}

// act serves an action on one GPU and writes the result through.
func (s *Server) act(do func(int, query) (fleet.GPU, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		i, ok := index(r)
		if !ok {
			http.Error(w, "?gpu=N required", http.StatusBadRequest)
			return
		}
		g, err := do(i, query{r})
		if err != nil {
			fail(w, err)
			return
		}
		log.Printf("GPU %d %s via %s", i, g.Status, r.URL.Path)
		s.apply(&g)
		writeJSON(w, g)
	}
}

func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fleet.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, fleet.ErrCrashed):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

// apply writes a GPU's state into the tree so file consumers see it.
func (s *Server) apply(g *fleet.GPU) {
	if s.tree == nil {
		return
	}
	if err := s.tree.Apply(g); err != nil {
		log.Printf("warning: sysfs sync for GPU %d failed: %v", g.Index, err)
	}
}

// fixed refuses topology changes while the node runs fixed DPX/NPS2
// partitions: allocations hold those devices until a restart.
func (s *Server) fixed(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.tree != nil && s.tree.Profile().Card.Partition.Compute == "DPX" {
			http.Error(w, "fixed DPX/NPS2 topology: drain claims and restart with a new startup configuration", http.StatusConflict)
			return
		}
		h(w, r)
	}
}

func (s *Server) gpus(w http.ResponseWriter, _ *http.Request) {
	gpus, sys := s.fleet.All()
	writeJSON(w, map[string]any{"gpus": gpus, "system": sys, "trays": s.fleet.Trays()})
}

func (s *Server) gpu(w http.ResponseWriter, r *http.Request) {
	i, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/gpus/"), "/"))
	if err != nil || i < 0 {
		http.Error(w, "invalid gpu index", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodPatch && r.Method != http.MethodPut {
		g, ok := s.fleet.Get(i)
		if !ok {
			fail(w, fleet.ErrNotFound)
			return
		}
		writeJSON(w, g)
		return
	}
	var patch json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	g, err := s.fleet.Patch(i, patch)
	if err != nil {
		fail(w, err)
		return
	}
	log.Printf("GPU %d updated via API", i)
	s.apply(&g)
	writeJSON(w, g)
}

func (s *Server) system(w http.ResponseWriter, _ *http.Request) {
	_, sys := s.fleet.All()
	writeJSON(w, sys)
}

func (s *Server) profiles(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.fleet.Catalog())
}

func (s *Server) switchProfile(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("profile")
	if slug == "" {
		http.Error(w, "?profile=<slug> required", http.StatusBadRequest)
		return
	}
	gpus, p, ok := s.fleet.Switch(slug)
	if !ok {
		http.Error(w, "profile not found: "+slug, http.StatusNotFound)
		return
	}
	if s.tree != nil {
		if err := s.tree.Switch(p); err != nil {
			log.Printf("warning: sysfs re-render failed: %v", err)
		}
	}
	log.Printf("fleet switched to profile %s", slug)
	writeJSON(w, gpus)
}

func (s *Server) switchTray(w http.ResponseWriter, r *http.Request) {
	i, ok := index(r)
	slug := r.URL.Query().Get("profile")
	if !ok || slug == "" {
		http.Error(w, "?gpu=N&profile=<slug> required", http.StatusBadRequest)
		return
	}
	gpus, ok := s.fleet.SwitchTray(i, slug)
	if !ok {
		http.Error(w, "gpu or profile not found", http.StatusNotFound)
		return
	}
	log.Printf("tray of GPUs %d-%d switched to profile %s", gpus[0].Index, gpus[len(gpus)-1].Index, slug)
	writeJSON(w, gpus)
}

func (s *Server) trays(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.fleet.Trays())
}

func (s *Server) trayLayout(w http.ResponseWriter, _ *http.Request) {
	gpus, _ := s.fleet.All()
	model := ""
	if len(gpus) > 0 {
		model = gpus[0].Name
	}
	writeJSON(w, map[string]any{
		"tray_size": fleet.TraySize(len(gpus)),
		"can_split": fleet.Splittable(model),
		"gpu_count": len(gpus),
		"profile":   model,
	})
}

func (s *Server) partitions(w http.ResponseWriter, _ *http.Request) {
	gpus, _ := s.fleet.All()
	current := "SPX"
	if len(gpus) > 0 {
		current = gpus[0].Partition
	}
	writeJSON(w, map[string]any{"current": current, "available": fleet.Modes})
}

func (s *Server) setPartition(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("mode")
	if name == "" {
		http.Error(w, "?mode=SPX|DPX|QPX|CPX required", http.StatusBadRequest)
		return
	}
	m, ok := fleet.LookupMode(name)
	if !ok {
		http.Error(w, "invalid mode: "+name, http.StatusBadRequest)
		return
	}
	s.fleet.SetMode(m)
	log.Printf("compute partition set to %s (%d per GPU)", m.Name, m.Slices)
	gpus, sys := s.fleet.All()
	writeJSON(w, map[string]any{"mode": m.Name, "partitions": m.Slices, "total_gpus": len(gpus), "system": sys})
}

// metrics is the agent's own Prometheus text, for fault state AMD's exporter
// has no metric for.
func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	gpus, _ := s.fleet.All()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	var b strings.Builder
	for _, g := range gpus {
		card := fmt.Sprintf(`gpu_id="%d",card_model="%s"`, g.Index, g.Name)
		pci := card + fmt.Sprintf(`,pci_bdf="%s"`, g.BDF)
		for _, m := range []struct {
			name, labels string
			value        int
		}{
			{"gpu_temperature", pci, g.TempC},
			{"gpu_power", pci, g.PowerW},
			{"gpu_gfx_activity", pci, g.Util},
			{"gpu_used_vram", pci, g.MemUsedMB << 20},
			{"gpu_total_vram", pci, g.MemTotalMB << 20},
			{"gpu_clock", card + `,clock_type="gfx"`, g.GFXClockMHz},
			{"gpu_clock", card + `,clock_type="mem"`, g.MemClockMHz},
			{"gpu_ecc_uncorrectable", card, g.ECCErrors},
			{"gpu_power_cap", card, g.PowerCapW},
			{"gpu_retired_pages", card, g.RetiredPages},
			{"gpu_throttled", card, bit(g.Throttled)},
			{"gpu_xgmi_link_up", card, bit(g.Status != fleet.XGMIDown)},
		} {
			fmt.Fprintf(&b, "%s{%s} %d\n", m.name, m.labels, m.value)
		}
	}
	w.Write([]byte(b.String()))
}

func bit(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok"))
}

func (s *Server) console(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(page))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(v)
}
