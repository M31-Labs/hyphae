// Slice Y.F — vm.HostReceiver adapters for the candidate backend.
//
// candidateCanvasHost is the bytecode-side analog of
// hostCanvasAdapter (which wires the baseline path through
// surface.NewCanvasFromHostImpl). Both adapters share the SAME
// canvasRasterizer instance type so the framebuffers are pixel-
// comparable.
//
// candidateCtxHost answers ctx.PropsInto by JSON-unmarshalling the
// scenario's props into the Y.A-shaped composite Value carrier the
// VM uses for struct/map/slice values. The lowerer's `&x` pass-
// through (Y.E.3.5) means the OpHostCall hands us the props Value
// directly — same Fields map the user code reads from.

package main

import (
	"encoding/json"
	"fmt"

	"m31labs.dev/gosx/client/vm"
)

// candidateCanvasHost wires the bytecode VM's "c" receiver to the
// shared rasterizer. The switch-statement dispatch is the Y.E
// retrospective's recommended choice (explicit + clear diagnostic on
// a missing method — reflect would silently turn a typo into a runtime
// error).
type candidateCanvasHost struct {
	r       *canvasRasterizer
	callLog []string // for diagnostics on parity failures
}

func newCandidateCanvasHost(r *canvasRasterizer) *candidateCanvasHost {
	return &candidateCanvasHost{r: r}
}

// Call satisfies vm.HostReceiver. Every method graph_surface.go's
// handlers invoke gets a switch arm; unknown methods record a
// diagnostic via the returned error.
func (h *candidateCanvasHost) Call(method string, args []vm.Value) (vm.Value, error) {
	h.callLog = append(h.callLog, method)
	switch method {
	case "Width":
		return vm.IntVal(h.r.w), nil
	case "Height":
		return vm.IntVal(h.r.h), nil
	case "ClearRect":
		if len(args) >= 4 {
			h.r.ClearRect(args[0].Num, args[1].Num, args[2].Num, args[3].Num)
		}
	case "Save":
		h.r.Save()
	case "Restore":
		h.r.Restore()
	case "Translate":
		if len(args) >= 2 {
			h.r.Translate(args[0].Num, args[1].Num)
		}
	case "Scale":
		if len(args) >= 2 {
			h.r.Scale(args[0].Num, args[1].Num)
		}
	case "BeginPath":
		h.r.BeginPath()
	case "MoveTo":
		if len(args) >= 2 {
			h.r.MoveTo(args[0].Num, args[1].Num)
		}
	case "LineTo":
		if len(args) >= 2 {
			h.r.LineTo(args[0].Num, args[1].Num)
		}
	case "Arc":
		if len(args) >= 5 {
			h.r.Arc(args[0].Num, args[1].Num, args[2].Num, args[3].Num, args[4].Num)
		}
	case "Stroke":
		h.r.Stroke()
	case "Fill":
		h.r.Fill()
	case "FillText":
		if len(args) >= 3 {
			h.r.FillText(args[0].Str, args[1].Num, args[2].Num)
		}
	case "SetFillStyle":
		if len(args) >= 1 {
			h.r.SetFillStyle(args[0].Str)
		}
	case "SetStrokeStyle":
		if len(args) >= 1 {
			h.r.SetStrokeStyle(args[0].Str)
		}
	case "SetLineWidth":
		if len(args) >= 1 {
			h.r.SetLineWidth(args[0].Num)
		}
	case "SetFont":
		if len(args) >= 1 {
			h.r.SetFont(args[0].Str)
		}
	case "SetTextAlign":
		if len(args) >= 1 {
			h.r.SetTextAlign(args[0].Str)
		}
	case "StartLoop":
		// Harness drives the loop per Y.E Option F.b. The FuncLit
		// argument may not be a well-formed Value (the closure didn't
		// lower); we just no-op here so the rest of Mount completes.
		return vm.Value{}, nil
	default:
		return vm.Value{}, fmt.Errorf("candidateCanvasHost: unhandled method %q (args=%d)", method, len(args))
	}
	return vm.Value{}, nil
}

// candidateCtxHost satisfies vm.HostReceiver for the "ctx" receiver —
// the only Context method graph_surface.go invokes is PropsInto.
type candidateCtxHost struct {
	propsJSON []byte
}

// Call handles PropsInto by unmarshalling propsJSON into the props
// Value that the lowerer passes in. Per Y.E.3.5's `&x` pass-through
// the receiver gets the Value directly (no pointer wrapper); we
// mutate its Fields map in place so subsequent reads in the handler
// body see the decoded props.
func (h *candidateCtxHost) Call(method string, args []vm.Value) (vm.Value, error) {
	switch method {
	case "PropsInto":
		if len(args) < 1 {
			return vm.Value{}, fmt.Errorf("PropsInto: missing target arg")
		}
		// Decode propsJSON into a generic map[string]any so we can
		// populate the target Value's Fields map without knowing the
		// exact struct shape at host-side compile time.
		var raw map[string]any
		if err := json.Unmarshal(h.propsJSON, &raw); err != nil {
			return vm.Value{}, fmt.Errorf("PropsInto: unmarshal: %w", err)
		}
		// The target is the props composite Value (Y.A struct kind),
		// already constructed at the call site as `var props GraphProps`.
		// We populate its Fields map; the keys must match the JSON tag
		// names graph_surface.go uses (Nodes, Edges, Center → lowercase
		// JSON tags via the struct's `json:"nodes"` etc.).
		populateValueFromJSON(&args[0], raw)
		return vm.Value{}, nil
	}
	return vm.Value{}, fmt.Errorf("candidateCtxHost: unhandled method %q", method)
}

// populateValueFromJSON copies a decoded JSON tree into a composite
// Value's Fields/Items map. graph_surface.go reads props.Nodes
// (slice of GraphNode), props.Edges (slice of GraphEdge), and
// props.Center (string) — the populator maps each.
//
// The implementation is generic over the JSON tree shape so future
// scenarios that add fields don't need a per-scenario host
// extension.
func populateValueFromJSON(target *vm.Value, raw map[string]any) {
	if target.Fields == nil {
		target.Fields = map[string]vm.Value{}
	}
	for k, v := range raw {
		target.Fields[fieldKeyFromJSON(k)] = jsonToValue(v)
	}
}

// fieldKeyFromJSON converts a JSON field name into the struct field
// key the lowerer uses. graph_surface.go declares
// `Nodes []GraphNode `json:"nodes"`` so the JSON key "nodes" must
// map back to "Nodes" — the lowerer indexes by the Go field name,
// not the JSON tag. The mapping is hard-coded for the GraphProps
// shape (small + stable).
func fieldKeyFromJSON(k string) string {
	switch k {
	case "nodes":
		return "Nodes"
	case "edges":
		return "Edges"
	case "center":
		return "Center"
	case "id":
		return "ID"
	case "label":
		return "Label"
	case "type":
		return "Type"
	case "from":
		return "From"
	case "to":
		return "To"
	case "kind":
		return "Kind"
	}
	return k
}

// jsonToValue converts a JSON node (decoded via encoding/json into
// the generic any tree) into a vm.Value the bytecode reader can walk.
func jsonToValue(v any) vm.Value {
	switch x := v.(type) {
	case nil:
		return vm.Value{}
	case bool:
		return vm.BoolVal(x)
	case float64:
		return vm.FloatVal(x)
	case string:
		return vm.StringVal(x)
	case []any:
		items := make([]vm.Value, len(x))
		for i, it := range x {
			items[i] = jsonToValue(it)
		}
		return vm.ArrayVal(items)
	case map[string]any:
		fields := map[string]vm.Value{}
		for k, vv := range x {
			fields[fieldKeyFromJSON(k)] = jsonToValue(vv)
		}
		return vm.ObjectVal(fields)
	}
	return vm.Value{}
}
