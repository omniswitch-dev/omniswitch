//! OmniSwitch guardrail accelerator.
//!
//! Compiled to wasm32-wasip1 and embedded into the gateway binary. Executes a
//! set of regex patterns in a single pass over the payload using the Rust
//! regex engine, returning byte-offset matches. The host (Go) owns all memory
//! allocation through `omni_alloc` / `omni_free`.

use regex::Regex;

struct Scanner {
    patterns: Vec<Regex>,
}

static mut SCANNERS: Option<Vec<Option<Scanner>>> = None;

#[allow(static_mut_refs)]
fn scanners() -> &'static mut Vec<Option<Scanner>> {
    unsafe {
        if SCANNERS.is_none() {
            SCANNERS = Some(Vec::new());
        }
        SCANNERS.as_mut().unwrap()
    }
}

fn get(handle: i32) -> Option<&'static mut Scanner> {
    if handle <= 0 {
        return None;
    }
    let idx = (handle - 1) as usize;
    match scanners().get_mut(idx) {
        Some(Some(scanner)) => Some(scanner),
        _ => None,
    }
}

/// Allocates a buffer inside WASM linear memory and returns its pointer.
#[no_mangle]
pub extern "C" fn omni_alloc(len: i32) -> i32 {
    if len <= 0 {
        return 0;
    }
    let mut buf: Vec<u8> = vec![0u8; len as usize];
    let ptr = buf.as_mut_ptr() as i32;
    std::mem::forget(buf);
    ptr
}

/// Frees a buffer previously returned by `omni_alloc`.
#[no_mangle]
pub extern "C" fn omni_free(ptr: i32, len: i32) {
    if ptr == 0 || len <= 0 {
        return;
    }
    unsafe {
        drop(Vec::from_raw_parts(ptr as *mut u8, len as usize, len as usize));
    }
}

/// Validates newline-separated patterns without retaining them. Writes the
/// indexes of patterns the Rust engine rejects into out_ptr (u32 LE each).
/// Returns the number of rejected patterns, or a negative error code.
#[no_mangle]
pub extern "C" fn scanner_validate(cfg_ptr: i32, cfg_len: i32, out_ptr: i32, out_cap: i32) -> i32 {
    let cfg = slice(cfg_ptr, cfg_len);
    if cfg.is_none() {
        return -1;
    }
    let text = match std::str::from_utf8(cfg.unwrap()) {
        Ok(t) => t,
        Err(_) => return -2,
    };
    let cap = (out_cap.max(0) as usize) / 4;
    let out = match slice_mut(out_ptr, (cap * 4) as i32) {
        Some(o) => o,
        None => return -3,
    };
    let mut failed: Vec<u32> = Vec::new();
    for (idx, line) in text.lines().enumerate() {
        let line = line.trim_end_matches('\r');
        if line.is_empty() {
            continue;
        }
        if Regex::new(line).is_err() && failed.len() < cap {
            failed.push(idx as u32);
        }
    }
    for (i, v) in failed.iter().enumerate() {
        out[i * 4..i * 4 + 4].copy_from_slice(&v.to_le_bytes());
    }
    failed.len() as i32
}

/// Creates a scanner from newline-separated regex patterns. Returns a positive
/// handle or a negative error. Patterns that fail to compile are replaced with
/// a never-matching expression so rule indexes stay stable across engines.
#[no_mangle]
pub extern "C" fn scanner_new(cfg_ptr: i32, cfg_len: i32) -> i32 {
    let cfg = match slice(cfg_ptr, cfg_len) {
        Some(c) => c,
        None => return -1,
    };
    let text = match std::str::from_utf8(cfg) {
        Ok(t) => t,
        Err(_) => return -2,
    };
    const NEVER_MATCH: &str = r"\b\B";
    let mut patterns = Vec::new();
    for line in text.lines() {
        let line = line.trim_end_matches('\r');
        if line.is_empty() {
            // Keep index alignment even for blank lines.
            patterns.push(Regex::new(NEVER_MATCH).unwrap());
            continue;
        }
        match Regex::new(line) {
            Ok(re) => patterns.push(re),
            Err(_) => patterns.push(Regex::new(NEVER_MATCH).unwrap()),
        }
    }
    let handle = (scanners().len() + 1) as i32;
    scanners().push(Some(Scanner { patterns }));
    handle
}

/// Scans data with the scanner identified by handle. Matches are written to
/// out_ptr as triples of u32 LE (rule_index, start, end). Returns the number
/// of matches written, truncated at out_cap/12, or a negative error code.
#[no_mangle]
pub extern "C" fn scanner_scan(handle: i32, data_ptr: i32, data_len: i32, out_ptr: i32, out_cap: i32) -> i32 {
    let scanner = match get(handle) {
        Some(s) => s,
        None => return -1,
    };
    let data = match slice(data_ptr, data_len) {
        Some(d) => d,
        None => return -2,
    };
    let text = match std::str::from_utf8(data) {
        Ok(t) => t,
        Err(_) => return -3,
    };
    let cap = (out_cap.max(0) as usize) / 12;
    let out = match slice_mut(out_ptr, (cap * 12) as i32) {
        Some(o) => o,
        None => return -4,
    };
    let mut n: usize = 0;
    for (idx, re) in scanner.patterns.iter().enumerate() {
        for m in re.find_iter(text) {
            if n >= cap {
                return n as i32;
            }
            let rec_start = n * 12;
            out[rec_start..rec_start + 4].copy_from_slice(&(idx as u32).to_le_bytes());
            out[rec_start + 4..rec_start + 8].copy_from_slice(&(m.start() as u32).to_le_bytes());
            out[rec_start + 8..rec_start + 12].copy_from_slice(&(m.end() as u32).to_le_bytes());
            n += 1;
        }
    }
    n as i32
}

/// Like `scanner_scan`, but records only the first match of each pattern and
/// stops early once every pattern has matched. This mirrors boolean rule
/// evaluation and avoids unbounded work on payloads with many hits.
#[no_mangle]
pub extern "C" fn scanner_scan_first(handle: i32, data_ptr: i32, data_len: i32, out_ptr: i32, out_cap: i32) -> i32 {
    let scanner = match get(handle) {
        Some(s) => s,
        None => return -1,
    };
    let data = match slice(data_ptr, data_len) {
        Some(d) => d,
        None => return -2,
    };
    let text = match std::str::from_utf8(data) {
        Ok(t) => t,
        Err(_) => return -3,
    };
    let cap = (out_cap.max(0) as usize) / 12;
    let out = match slice_mut(out_ptr, (cap * 12) as i32) {
        Some(o) => o,
        None => return -4,
    };
    let mut n: usize = 0;
    for (idx, re) in scanner.patterns.iter().enumerate() {
        if n >= cap {
            break;
        }
        if let Some(m) = re.find(text) {
            let rec_start = n * 12;
            out[rec_start..rec_start + 4].copy_from_slice(&(idx as u32).to_le_bytes());
            out[rec_start + 4..rec_start + 8].copy_from_slice(&(m.start() as u32).to_le_bytes());
            out[rec_start + 8..rec_start + 12].copy_from_slice(&(m.end() as u32).to_le_bytes());
            n += 1;
        }
    }
    n as i32
}

/// Drops the scanner referenced by handle.
#[no_mangle]
pub extern "C" fn scanner_free(handle: i32) {
    if handle <= 0 {
        return;
    }
    let idx = (handle - 1) as usize;
    let list = scanners();
    if idx < list.len() {
        list[idx] = None;
    }
}

fn slice(ptr: i32, len: i32) -> Option<&'static [u8]> {
    if ptr == 0 || len < 0 {
        return None;
    }
    Some(unsafe { std::slice::from_raw_parts(ptr as *const u8, len as usize) })
}

fn slice_mut(ptr: i32, len: i32) -> Option<&'static mut [u8]> {
    if ptr == 0 || len <= 0 {
        return None;
    }
    Some(unsafe { std::slice::from_raw_parts_mut(ptr as *mut u8, len as usize) })
}
