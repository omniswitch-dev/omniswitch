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
	"runtime"
	"sort"
	"strings"
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

// Scanner evaluates regex patterns against payloads using the Rust regex
// engine running in WASM. It is safe for concurrent use: it keeps a small
// pool of independent module instances so concurrent scans do not
// serialise on a single lock.
type Scanner struct {
	patterns []string
	max      int

	mu      sync.Mutex
	closed  bool
	created int
	idle    chan *instance
	done    chan struct{}
}

// instance is one WASM module instance with a compiled scanner. It is used
// by one goroutine at a time.
type instance struct {
	module api.Module
	handle int32
	inPtr  uint32
	outPtr uint32
	bufCap uint32
}

// maxPoolSize caps how many module instances one Scanner creates. Each
// instance owns its own linear memory, so the pool stays small.
const maxPoolSize = 4

// sharedRuntime compiles the embedded module once per process. Every
// scanner instance is an anonymous instantiation of that compiled module in
// the shared runtime, with its own linear memory, so building a scanner no
// longer recompiles the WASM binary (which took hundreds of milliseconds)
// and closing one releases only its own instance.
var sharedRuntime = sync.OnceValues(func() (*wasmRuntime, error) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCloseOnContextDone(true))
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("accel: wasi init failed: %w", err)
	}
	compiled, err := rt.CompileModule(ctx, wasmBinary)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("accel: compile failed: %w", err)
	}
	return &wasmRuntime{runtime: rt, compiled: compiled}, nil
})

type wasmRuntime struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
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

	cfg, unsafe := joinPatterns(patterns)
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
	rejected := make([]int, 0, count+len(unsafe))
	for i := 0; i < count; i++ {
		rejected = append(rejected, int(binary.LittleEndian.Uint32(raw[i*4:])))
	}
	rejected = append(rejected, unsafe...)
	sort.Ints(rejected)
	return rejected, nil
}

// NewScanner compiles patterns into a reusable accelerator scanner. Patterns
// that cannot be encoded safely (see SafePattern) never match; callers
// should evaluate them with the pure-Go path.
func NewScanner(patterns []string) (*Scanner, error) {
	if !Available() {
		return nil, ErrUnavailable
	}
	size := runtime.GOMAXPROCS(0)
	if size > maxPoolSize {
		size = maxPoolSize
	}
	if size < 1 {
		size = 1
	}
	s := &Scanner{
		patterns: append([]string(nil), patterns...),
		max:      size,
		idle:     make(chan *instance, size),
		done:     make(chan struct{}),
	}
	// Build the first instance eagerly so configuration errors surface
	// here rather than on the first scan.
	inst, err := s.newInstance()
	if err != nil {
		return nil, err
	}
	s.created = 1
	s.idle <- inst
	return s, nil
}

// SafePattern reports whether a pattern can be passed to the accelerator.
// Patterns are sent newline-delimited, so a pattern containing a line
// break would be split and shift every later rule index.
func SafePattern(pattern string) bool {
	return !strings.ContainsAny(pattern, "\r\n")
}

func (s *Scanner) newInstance() (*instance, error) {
	e, err := newEngine()
	if err != nil {
		return nil, err
	}
	cfg, _ := joinPatterns(s.patterns)
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
	inst := &instance{module: e.module, handle: int32(res[0])}
	if err := inst.ensureBuffers(4096); err != nil {
		_ = inst.close()
		return nil, err
	}
	return inst, nil
}

// acquire returns an idle instance, creating one while the pool is below
// its cap, or waits for one to be released.
func (s *Scanner) acquire() (*instance, error) {
	select {
	case inst := <-s.idle:
		return inst, nil
	default:
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrUnavailable
	}
	if s.created < s.max {
		s.created++
		s.mu.Unlock()
		inst, err := s.newInstance()
		if err == nil {
			return inst, nil
		}
		s.mu.Lock()
		s.created--
		none := s.created == 0
		s.mu.Unlock()
		if none {
			// Nothing to wait for: no other instance exists.
			return nil, err
		}
		// Fall through and wait for an existing instance.
	} else {
		s.mu.Unlock()
	}
	select {
	case inst := <-s.idle:
		return inst, nil
	case <-s.done:
		return nil, ErrUnavailable
	}
}

// release returns an instance to the pool. A broken instance, or any
// instance released after Close, is closed instead.
func (s *Scanner) release(inst *instance, broken bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || broken {
		s.created--
		_ = inst.close()
		return
	}
	s.idle <- inst // never blocks: at most max instances exist
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
	if s == nil {
		return nil, ErrUnavailable
	}
	inst, err := s.acquire()
	if err != nil {
		return nil, err
	}
	matches, broken, err := inst.scan(data, export)
	s.release(inst, broken)
	return matches, err
}

// scan runs one scan. broken reports that the instance must not be reused
// (the WASM call trapped or memory could not be accessed).
func (inst *instance) scan(data []byte, export string) (matches []Match, broken bool, err error) {
	if err := inst.ensureBuffers(uint32(len(data))); err != nil {
		return nil, true, err
	}
	if !inst.memory().Write(inst.inPtr, data) {
		return nil, true, fmt.Errorf("accel: payload write failed")
	}
	res, err := inst.module.ExportedFunction(export).Call(
		context.Background(),
		uint64(inst.handle), uint64(inst.inPtr), uint64(len(data)),
		uint64(inst.outPtr), uint64(inst.bufCap),
	)
	if err != nil {
		return nil, true, fmt.Errorf("accel: scan failed: %w", err)
	}
	count := int(int32(res[0]))
	if count < 0 {
		return nil, false, fmt.Errorf("accel: scan rejected input (%d)", count)
	}
	raw, ok := inst.memory().Read(inst.outPtr, uint32(count*matchRecordSize))
	if !ok {
		return nil, true, fmt.Errorf("accel: scan result read failed")
	}
	matches = make([]Match, 0, count)
	for i := 0; i < count; i++ {
		rec := raw[i*matchRecordSize:]
		matches = append(matches, Match{
			RuleIndex: int(binary.LittleEndian.Uint32(rec)),
			Start:     int(binary.LittleEndian.Uint32(rec[4:])),
			End:       int(binary.LittleEndian.Uint32(rec[8:])),
		})
	}
	return matches, false, nil
}

// Close releases the scanner and its WASM module instances. Idle ones are
// closed immediately; instances in use by a concurrent scan are closed when
// that scan finishes. Close is idempotent and Scan returns ErrUnavailable
// afterwards.
func (s *Scanner) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	close(s.done)
	var err error
	for {
		select {
		case inst := <-s.idle:
			s.created--
			if cerr := inst.close(); err == nil {
				err = cerr
			}
		default:
			return err
		}
	}
}

// close frees the scanner handle inside the module and releases the
// module instance. The handle is captured before it is zeroed so the real
// scanner is freed (previously handle 0 was passed to scanner_free).
func (inst *instance) close() error {
	handle := inst.handle
	inst.handle = 0
	if handle != 0 {
		_, _ = inst.module.ExportedFunction("scanner_free").Call(context.Background(), uint64(handle))
	}
	return inst.module.Close(context.Background())
}

// joinPatterns encodes patterns newline-delimited, one per line, which is
// the format the WASM module expects. A pattern that is not SafePattern is
// written as a blank line (which never matches, keeping indexes aligned)
// and its index is returned in unsafe.
func joinPatterns(patterns []string) (cfg []byte, unsafe []int) {
	cfg = make([]byte, 0, 64*len(patterns))
	for i, p := range patterns {
		if !SafePattern(p) {
			unsafe = append(unsafe, i)
			p = ""
		}
		cfg = append(cfg, []byte(p)...)
		cfg = append(cfg, '\n')
	}
	return cfg, unsafe
}

func (inst *instance) ensureBuffers(dataLen uint32) error {
	required := dataLen
	outRequired := dataLen/2 + 4096
	if required < 4096 {
		required = 4096
	}
	if inst.inPtr != 0 && required <= inst.bufCap && outRequired <= inst.bufCap {
		return nil
	}
	capacity := required
	if outRequired > capacity {
		capacity = outRequired
	}
	capacity = (capacity + 4095) &^ 4095 // page-align growth
	mem := inst.memory()
	if mem == nil {
		return fmt.Errorf("accel: module memory unavailable")
	}
	if inst.inPtr != 0 {
		_, _ = inst.module.ExportedFunction("omni_free").Call(context.Background(), uint64(inst.inPtr), uint64(inst.bufCap))
		inst.inPtr = 0
	}
	if inst.outPtr != 0 {
		_, _ = inst.module.ExportedFunction("omni_free").Call(context.Background(), uint64(inst.outPtr), uint64(inst.bufCap))
		inst.outPtr = 0
	}
	var err error
	if inst.inPtr, err = inst.alloc(capacity); err != nil {
		return err
	}
	if inst.outPtr, err = inst.alloc(capacity); err != nil {
		return err
	}
	inst.bufCap = capacity
	return nil
}

type engine struct {
	ctx    context.Context
	module api.Module
}

func newEngine() (*engine, error) {
	ctx := context.Background()
	shared, err := sharedRuntime()
	if err != nil {
		return nil, err
	}
	mod, err := shared.runtime.InstantiateModule(ctx, shared.compiled, wazero.NewModuleConfig().WithName(""))
	if err != nil {
		return nil, fmt.Errorf("accel: instantiate failed: %w", err)
	}
	return &engine{ctx: ctx, module: mod}, nil
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
}

func (inst *instance) alloc(size uint32) (uint32, error) {
	if size == 0 {
		size = 1
	}
	res, err := inst.module.ExportedFunction("omni_alloc").Call(context.Background(), uint64(size))
	if err != nil {
		return 0, fmt.Errorf("accel: alloc failed: %w", err)
	}
	ptr := uint32(res[0])
	if ptr == 0 {
		return 0, fmt.Errorf("accel: alloc returned null")
	}
	return ptr, nil
}

func (inst *instance) memory() api.Memory { return inst.module.Memory() }
