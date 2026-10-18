package main

import (
	"log"
	"time"

	"github.com/omniswitch-dev/omniswitch/internal/guardrail"
)

// guardrailRetireDelay keeps a replaced guardrail engine open for a while so
// in-flight requests can finish their scans on its WASM accelerator. A scan
// on a closed engine still works: it falls back to Go regular expressions.
const guardrailRetireDelay = 30 * time.Second

// retireGuardrailEngine closes an engine that config hot-reload replaced,
// after guardrailRetireDelay. Without it every reload leaked the old
// engine's compiled WASM scanners. It is a variable so tests can observe
// the call.
var retireGuardrailEngine = func(engine *guardrail.Engine) {
	if engine == nil {
		return
	}
	time.AfterFunc(guardrailRetireDelay, func() {
		if err := engine.Close(); err != nil {
			log.Printf("closing replaced guardrail engine: %v", err)
		}
	})
}
