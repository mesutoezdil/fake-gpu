package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// A scenario is a timed script of faults and checks, for testing whatever
// watches the GPUs: alert rules, remediation controllers, dashboards.
type scenario struct {
	Name  string `yaml:"name"`
	Steps []step `yaml:"steps"`
}

type step struct {
	At     time.Duration     `yaml:"at"` // offset from the start of the run
	GPU    int               `yaml:"gpu"`
	Do     string            `yaml:"do"`     // an API action; empty for a pure check
	Args   map[string]string `yaml:"args"`   // action options such as util or count
	Expect map[string]string `yaml:"expect"` // field: "<op> value", e.g. temperature_c: ">= 80"
	Within time.Duration     `yaml:"within"` // how long expect may take; default 30s
}

func runScenario(client *http.Client, api, path string, out io.Writer) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var sc scenario
	if err := yaml.Unmarshal(data, &sc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if len(sc.Steps) == 0 {
		return fmt.Errorf("%s: no steps", path)
	}
	sort.SliceStable(sc.Steps, func(a, b int) bool { return sc.Steps[a].At < sc.Steps[b].At })
	fmt.Fprintf(out, "scenario: %s (%d steps)\n", sc.Name, len(sc.Steps))

	start := time.Now()
	for i, st := range sc.Steps {
		time.Sleep(time.Until(start.Add(st.At)))
		stamp := fmt.Sprintf("[%6.1fs] gpu %d", time.Since(start).Seconds(), st.GPU)
		if st.Do != "" {
			query := url.Values{"gpu": {strconv.Itoa(st.GPU)}}
			for k, v := range st.Args {
				query.Set(k, v)
			}
			var g gpu
			if err := call(client, http.MethodPost, api+"/api/actions/"+url.PathEscape(st.Do)+"?"+query.Encode(), &g); err != nil {
				return fmt.Errorf("step %d (%s): %w", i+1, st.Do, err)
			}
			fmt.Fprintf(out, "%s %s -> %s\n", stamp, st.Do, g.Status)
		}
		if len(st.Expect) > 0 {
			if err := await(client, api, st); err != nil {
				return fmt.Errorf("step %d: %w", i+1, err)
			}
			fmt.Fprintf(out, "%s ok: %s\n", stamp, describe(st.Expect))
		}
	}
	fmt.Fprintf(out, "scenario passed in %.1fs\n", time.Since(start).Seconds())
	return nil
}

// await polls one GPU until every expectation holds or the deadline passes.
func await(client *http.Client, api string, st step) error {
	limit := st.Within
	if limit == 0 {
		limit = 30 * time.Second
	}
	deadline := time.Now().Add(limit)
	var unmet string
	for {
		var state map[string]any
		if err := call(client, http.MethodGet, fmt.Sprintf("%s/api/gpus/%d", api, st.GPU), &state); err != nil {
			return err
		}
		unmet = ""
		for field, want := range st.Expect {
			got, known := state[field]
			if !known {
				return fmt.Errorf("gpu %d has no field %q", st.GPU, field)
			}
			ok, err := holds(got, want)
			if err != nil {
				return fmt.Errorf("%s: %w", field, err)
			}
			if !ok {
				unmet = fmt.Sprintf("%s is %v, want %s", field, got, want)
			}
		}
		if unmet == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("gpu %d after %s: %s", st.GPU, limit, unmet)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// holds evaluates "<op> value" against a JSON value. A bare value means ==.
func holds(got any, want string) (bool, error) {
	op, value := "==", strings.TrimSpace(want)
	for _, candidate := range []string{">=", "<=", "==", "!=", ">", "<"} {
		if strings.HasPrefix(value, candidate) {
			op, value = candidate, strings.TrimSpace(value[len(candidate):])
			break
		}
	}
	if n, isNumber := got.(float64); isNumber {
		w, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return false, fmt.Errorf("%q is not a number", value)
		}
		switch op {
		case ">=":
			return n >= w, nil
		case "<=":
			return n <= w, nil
		case ">":
			return n > w, nil
		case "<":
			return n < w, nil
		case "!=":
			return n != w, nil
		}
		return n == w, nil
	}
	text := fmt.Sprint(got)
	switch op {
	case "==":
		return text == value, nil
	case "!=":
		return text != value, nil
	}
	return false, fmt.Errorf("%s needs a number, got %q", op, text)
}

func describe(expect map[string]string) string {
	parts := make([]string, 0, len(expect))
	for field, want := range expect {
		parts = append(parts, field+" "+want)
	}
	sort.Strings(parts)
	return strings.Join(parts, ", ")
}
