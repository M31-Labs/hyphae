// Slice Y.F — bytecode-VM candidate backend for the parity test.
//
// renderCandidate lowers graph_surface.go through gosx's ir/golower
// (the Y.A-Y.E AST compiler chain) into a *program.Program, then
// evaluates the relevant handlers on a fresh VM with the parity
// rasterizer wired as the "c" and "ctx" host receivers.
//
// The flow mirrors the production engine-surface runtime:
//
//   1. LowerFile reads graph_surface.go and produces bytecode. A
//      single residual diagnostic is expected (the FuncLit closure
//      in Mount's c.StartLoop call — the one Y.G/Phase-4 gap remaining
//      after Y.E). The diagnostic doesn't block evaluation of the rest
//      of the surface; the OpHostCall for c.StartLoop is still emitted
//      and our adapter no-ops it (the harness drives the loop instead
//      per Y.E's Option F.b recommendation).
//
//   2. A fresh VM gets InitSignals so the package-level signals
//      (gNodes, gPos, gVel, gDrag, gTx, gSelected) materialize with
//      their declared zero values.
//
//   3. BindHost("c", adapter) and BindHost("ctx", contextHostAdapter)
//      route every selector call in the handler bodies to the parity
//      rasterizer / props decoder.
//
//   4. Mount runs (which decodes props, seeds positions, and no-ops on
//      StartLoop). Then the harness drives stepLayout + draw N times
//      via OpIndirectCall through the user-function registry — exact
//      same Y.D dispatch path the production runtime uses for
//      cross-handler calls.

package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"m31labs.dev/gosx/client/vm"
	"m31labs.dev/gosx/island/program"
	"m31labs.dev/gosx/ir/golower"
)

// renderCandidate produces the bytecode-VM framebuffer for the
// scenario. Returns an error only when the harness can't bootstrap;
// expected residual diagnostics from FuncLit are tolerated.
func renderCandidate(s parityScenario) (*canvasRasterizer, error) {
	src, err := readGraphSurfaceSource()
	if err != nil {
		return nil, fmt.Errorf("read graph_surface.go: %w", err)
	}

	prog, lerr := golower.LowerFile(src)
	if prog == nil {
		return nil, fmt.Errorf("LowerFile returned nil program (residual error: %v)", lerr)
	}
	// Expected post-Y.E residual: 1 FuncLit diagnostic. Anything else is
	// a regression — fail loudly so a future lowering bug doesn't get
	// papered over.
	if lerr != nil {
		le, ok := lerr.(*golower.LowerError)
		if !ok {
			return nil, fmt.Errorf("LowerFile non-LowerError: %w", lerr)
		}
		if len(le.Issues) != 1 {
			return nil, fmt.Errorf("post-Y.E expected exactly 1 residual issue (FuncLit closure); got %d:\n%s", len(le.Issues), le.Error())
		}
	}

	// Seed the global RNG to match the baseline run. graph_surface.go's
	// initPositions calls rand.Float64() for the jitter term; both
	// backends must see the same sequence for SSIM = 1.0.
	rand.Seed(s.Seed)

	host := newCanvasRasterizer(s.Width, s.Height)
	canvasAdapter := newCandidateCanvasHost(host)

	propsJSON, err := json.Marshal(s.Props)
	if err != nil {
		return nil, fmt.Errorf("marshal props: %w", err)
	}
	ctxAdapter := &candidateCtxHost{propsJSON: propsJSON}

	machine := vm.NewVM(prog, nil)
	vm.InitSignals(machine, prog)
	machine.BindHost("c", canvasAdapter)
	machine.BindHost("ctx", ctxAdapter)

	// Run Mount: decodes props, seeds positions, no-ops on StartLoop.
	if err := invokeHandlerByName(machine, prog, "Mount"); err != nil {
		return nil, fmt.Errorf("invoke Mount: %w", err)
	}

	// Drive stepLayout + draw from the harness (Y.E Option F.b — side-
	// steps the FuncLit closure that StartLoop would otherwise own).
	//
	// stepLayout(_ float64) ignores its dt parameter (per the source);
	// draw(c *surface.Canvas) references c only as `c.Method(...)` which
	// lowers to OpHostCall("c.Method", ...) — `c` itself is never read
	// as a Value. Both can therefore be invoked as Handlers (no args)
	// and the host bindings carry the canvas dispatch automatically.
	for i := 0; i < s.Steps; i++ {
		if err := invokeHandlerByName(machine, prog, "stepLayout"); err != nil {
			return nil, fmt.Errorf("step %d: %w", i, err)
		}
		if err := invokeHandlerByName(machine, prog, "draw"); err != nil {
			return nil, fmt.Errorf("draw %d: %w", i, err)
		}
	}
	return host, nil
}

// readGraphSurfaceSource locates the canonical graph_surface.go file
// relative to the test binary's working directory. The same source
// the baseline path executes natively — that's the whole point.
func readGraphSurfaceSource() ([]byte, error) {
	// Try the path relative to the cmd/hypha-viz package (where this
	// test runs).
	candidates := []string{
		"graphsurface/graph_surface.go",
		"./graphsurface/graph_surface.go",
		filepath.Join("cmd", "hypha-viz", "graphsurface", "graph_surface.go"),
	}
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err == nil {
			return data, nil
		}
	}
	return nil, fmt.Errorf("graph_surface.go not found in any candidate path: %v", candidates)
}

// invokeHandlerByName finds the handler with the given name in the
// program and evaluates its body in a fresh VM frame. Returns an
// error when the handler is missing — every test scenario expects
// Mount to be present.
func invokeHandlerByName(machine *vm.VM, prog *program.Program, name string) error {
	for _, h := range prog.Handlers {
		if h.Name != name {
			continue
		}
		if len(h.Body) == 0 {
			return fmt.Errorf("handler %q has empty body", name)
		}
		machine.EvalWithFrame(h.Body[0])
		return nil
	}
	return fmt.Errorf("handler %q not found in lowered program", name)
}

