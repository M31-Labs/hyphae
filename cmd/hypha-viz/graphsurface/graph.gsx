package graphsurface

import (
	"github.com/odvcencio/gosx"
)

//gosx:engine surface
//gosx:capabilities canvas pointer wheel
func Graph(props GraphProps) gosx.Node {
	return <canvas id="graph-canvas"
		onMount={Mount}
		onPointerDown={OnDown}
		onPointerMove={OnMove}
		onPointerUp={OnUp}
		onWheel={OnZoom}
		onDblClick={OnDouble}
		onResize={OnResize}
	/>
}
