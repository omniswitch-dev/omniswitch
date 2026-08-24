// Package accel executes performance-sensitive pattern scanning inside an
// embedded WebAssembly module compiled from Rust (see accel/ in the repo
// root). The pure-Go fallback in internal/guardrail remains authoritative
// whenever the accelerator is unavailable or returns an error.
package accel

import (
	"context"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed guardrails.wasm
var wasmBinary []byte

var (
	// ErrUnavailable indicates the accelerator module is not embedded in
	// this binary; callers must fall back to the pure-Go implementation.
	ErrUnavailable = errors.New("accelerator unavailable")
)

const (
	matchRecordSize = 12 // rule_index, start, end as uint32 LE
)

// Available reports whether an accelerator module is embedded.
func Available() bool { return len(wasmBinary) > 0 }

// Match identifies a pattern hit inside the scanned payload.
type Match struct {
	RuleIndex int
	Start     int
	End       int
}

// Scanner evaluates newline-delimited regex patterns against payloads using
// the Rust regex engine running in WASM. It is safe for concurrent use.
type Scanner struct {
	mu      sync.Mutex
	runtime wazero.Runtime
	module  api.Module
	handle  int32
	inPtr   uint32
	outPtr  uint32
	bufCap  uint32
}

// ValidatePatterns compiles patterns with the accelerator's engine and
// returns the indexes of any patterns it rejects. Those indexes should keep
// being evaluated by the pure-Go path.
func ValidatePatterns(patterns []string) ([]int, error) {
	if !Available() {
		return nil, ErrUnavailable
	}
	e, err := newEngine()
	if err != nil {
		return nil, err
	}
	defer e.close()

	cfg := joinPatterns(patterns)
	cfgPtr, err := e.alloc(uint32(len(cfg)))
	if err != nil {
		return nil, err
	}
	defer e.free(cfgPtr, uint32(len(cfg)))
	if !e.memory().Write(cfgPtr, cfg) {
		return nil, fmt.Errorf("accel: config write failed")
	}

	const maxRejected = 256
	outCap := uint32(maxRejected * 4)
	outPtr, err := e.alloc(outCap)
	if err != nil {
		return nil, err
	}
	defer e.free(outPtr, outCap)

	res, err := e.module.ExportedFunction("scanner_validate").Call(e.ctx, uint64(cfgPtr), uint64(len(cfg)), uint64(outPtr), uint64(outCap))
	if err != nil {
		return nil, fmt.Errorf("accel: validate failed: %w", err)
	}
	count := int(int32(res[0]))
	if count < 0 {
		return nil, fmt.Errorf("accel: validate rejected input (%d)", count)
	}
	raw, ok := e.memory().Read(outPtr, uint32(count*4))
	if !ok {
		return nil, fmt.Errorf("accel: validate result read failed")
	}
	rejected := make([]int, 0, count)
	for i := 0; i < count; i++ {
		rejected = append(rejected, int(binary.LittleEndian.Uint32(raw[i*4:])))
	}
	return rejected, nil
}

// NewScanner compiles patterns into a reusable accelerator scanner.
func NewScanner(patterns []string) (*Scanner, error) {
	if !Available() {
		return nil, ErrUnavailable
	}
	e, err := newEngine()
	if err != nil {
		return nil, err
	}

	cfg := joinPatterns(patterns)
	cfgPtr, err := e.alloc(uint32(len(cfg)))
	if err != nil {
		e.close()
		return nil, err
	}
	defer e.free(cfgPtr, uint32(len(cfg)))
	if len(cfg) > 0 && !e.memory().Write(cfgPtr, cfg) {
		e.close()
		return nil, fmt.Errorf("accel: config write failed")
	}

	res, err := e.module.ExportedFunction("scanner_new").Call(e.ctx, uint64(cfgPtr), uint64(len(cfg)))
	if err != nil || int32(res[0]) <= 0 {
		e.close()
		return nil, fmt.Errorf("accel: scanner init failed: %v", err)
	}
	s := &Scanner{runtime: e.runtime, module: e.module, handle: int32(res[0])}
	if err := s.ensureBuffers(4096); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Scan returns every match of the compiled patterns inside data.
func (s *Scanner) Scan(data []byte) ([]Match, error) {
	return s.scan(data, "scanner_scan")
}

// ScanFirst returns at most one match per pattern, mirroring boolean rule
// evaluation. It stops as early as possible and is the right entry point for
// trigger detection; Scan is for cases that need every position (redaction).
func (s *Scanner) ScanFirst(data []byte) ([]Match, error) {
	return s.scan(data, "scanner_scan_first")
}

func (s *Scanner) scan(data []byte, export string) ([]Match, error) {
	if len(data) == 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == 0 {
		return nil, ErrUnavailable
	}
	if err := s.ensureBuffers(uint32(len(data))); err != nil {
		return nil, err
	}
	if !s.memory().Write(s.inPtr, data) {
		return nil, fmt.Errorf("accel: payload write failed")
	}
	res, err := s.module.ExportedFunction(export).Call(
		context.Background(),
		uint64(s.handle), uint64(s.inPtr), uint64(len(data)),
		uint64(s.outPtr), uint64(s.bufCap),
	)
	if err != nil {
		return nil, fmt.Errorf("accel: scan failed: %w", err)
	}
	count := int(int32(res[0]))
	if count < 0 {
		return nil, fmt.Errorf("accel: scan rejected input (%d)", count)
	}
	raw, ok := s.memory().Read(s.outPtr, uint32(count*matchRecordSize))
	if !ok {
		return nil, fmt.Errorf("accel: scan result read failed")
	}
	matches := make([]Match, 0, count)
	for i := 0; i < count; i++ {
		rec := raw[i*matchRecordSize:]
		matches = append(matches, Match{
			RuleIndex: int(binary.LittleEndian.Uint32(rec)),
			Start:     int(binary.LittleEndian.Uint32(rec[4:])),
			End:       int(binary.LittleEndian.Uint32(rec[8:])),
		})
	}
	return matches, nil
}

// Close releases the scanner and its WASM runtime.
func (s *Scanner) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handle == 0 {
		return nil
	}
	s.handle = 0
	_, _ = s.module.ExportedFunction("scanner_free").Call(context.Background(), uint64(s.handle))
	err := s.module.Close(context.Background())
	if cerr := s.runtime.Close(context.Background()); err == nil {
		err = cerr
	}
	return err
}

func joinPatterns(patterns []string) []byte {
	cfg := make([]byte, 0, 64*len(patterns))
	for _, p := range patterns {
		cfg = append(cfg, []byte(p)...)
		cfg = append(cfg, '\n')
	}
	return cfg
}

func (s *Scanner) ensureBuffers(dataLen uint32) error {
	required := dataLen
	outRequired := dataLen/2 + 4096
	if required < 4096 {
		required = 4096
	}
	if s.inPtr != 0 && required <= s.bufCap && outRequired <= s.bufCap {
		return nil
	}
	capacity := required
	if outRequired > capacity {
		capacity = outRequired
	}
	capacity = (capacity + 4095) &^ 4095 // page-align growth
	mem := s.memory()
	if mem == nil {
		return fmt.Errorf("accel: module memory unavailable")
	}
	if s.inPtr != 0 {
		_, _ = s.module.ExportedFunction("omni_free").Call(context.Background(), uint64(s.inPtr), uint64(s.bufCap))
		s.inPtr = 0
	}
	if s.outPtr != 0 {
		_, _ = s.module.ExportedFunction("omni_free").Call(context.Background(), uint64(s.outPtr), uint64(s.bufCap))
		s.outPtr = 0
	}
	var err error
	if s.inPtr, err = s.alloc(capacity); err != nil {
		return err
	}
	if s.outPtr, err = s.alloc(capacity); err != nil {
		return err
	}
	s.bufCap = capacity
	return nil
}

type engine struct {
	ctx     context.Context
	runtime wazero.Runtime
	module  api.Module
}

func newEngine() (*engine, error) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCloseOnContextDone(true))
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("accel: wasi init failed: %w", err)
	}
	mod, err := rt.Instantiate(ctx, wasmBinary)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("accel: instantiate failed: %w", err)
	}
	return &engine{ctx: ctx, runtime: rt, module: mod}, nil
}

func (e *engine) memory() api.Memory { return e.module.Memory() }

func (e *engine) alloc(size uint32) (uint32, error) {
	if size == 0 {
		size = 1
	}
	res, err := e.module.ExportedFunction("omni_alloc").Call(e.ctx, uint64(size))
	if err != nil {
		return 0, fmt.Errorf("accel: alloc failed: %w", err)
	}
	ptr := uint32(res[0])
	if ptr == 0 {
		return 0, fmt.Errorf("accel: alloc returned null")
	}
	return ptr, nil
}

func (e *engine) free(ptr uint32, size uint32) {
	if ptr == 0 {
		return
	}
	_, _ = e.module.ExportedFunction("omni_free").Call(e.ctx, uint64(ptr), uint64(size))
}

func (e *engine) close() {
	_ = e.module.Close(e.ctx)
	_ = e.runtime.Close(e.ctx)
}

func (s *Scanner) alloc(size uint32) (uint32, error) {
	if size == 0 {
		size = 1
	}
	res, err := s.module.ExportedFunction("omni_alloc").Call(context.Background(), uint64(size))
	if err != nil {
		return 0, fmt.Errorf("accel: alloc failed: %w", err)
	}
	ptr := uint32(res[0])
	if ptr == 0 {
		return 0, fmt.Errorf("accel: alloc returned null")
	}
	return ptr, nil
}

func (s *Scanner) memory() api.Memory { return s.module.Memory() }
