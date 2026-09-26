use crate::DesktopRdpWindow;
use slint::{CloseRequestResponse, ComponentHandle, Timer};
#[cfg(windows)]
use slint::{Image, Rgba8Pixel, SharedPixelBuffer};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{self, Sender};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;
use std::time::{Instant, SystemTime, UNIX_EPOCH};
use std::{cell::RefCell, collections::HashMap, rc::Rc};
use std::{fs::File, io::BufWriter, io::Write, path::PathBuf};

#[cfg(windows)]
use slint::TimerMode;
#[cfg(windows)]
use std::time::Duration;

/// Parameters for one in-memory RDP connection.
///
/// The UI supplies all connection parameters for one in-memory session. The
/// target is never serialized or logged.
#[cfg_attr(not(windows), allow(dead_code))]
pub struct RdpTarget {
    pub host: String,
    pub port: u16,
    pub username: Option<String>,
    pub password: Option<String>,
    pub domain: Option<String>,
    /// Accept a certificate that FreeRDP cannot validate for this session only.
    ///
    /// This is deliberately not persisted and never maps to
    /// `FreeRDP_IgnoreCertificate`, which would silently disable certificate
    /// verification for every connection.
    pub allow_untrusted_certificate: bool,
    pub width: u32,
    pub height: u32,
}

pub struct RdpPerformanceOptions {
    pub show_hud: bool,
    pub record: bool,
    pub data_dir: PathBuf,
}

impl Default for RdpPerformanceOptions {
    fn default() -> Self {
        Self {
            show_hud: false,
            record: false,
            data_dir: PathBuf::new(),
        }
    }
}

impl RdpTarget {
    pub fn with_credentials(
        host: String,
        username: String,
        password: String,
        domain: Option<String>,
    ) -> Self {
        Self {
            host,
            port: 3389,
            username: Some(username),
            password: Some(password),
            domain,
            allow_untrusted_certificate: false,
            width: 1920,
            height: 1080,
        }
    }
}

impl Drop for RdpTarget {
    fn drop(&mut self) {
        if let Some(password) = &mut self.password {
            wipe_string(password);
        }
    }
}

#[cfg_attr(not(windows), allow(dead_code))]
#[derive(Debug)]
struct RdpFrame {
    width: u32,
    height: u32,
    /// This is an owned Slint buffer; optimized snapshots are created on the
    /// UI thread and FreeRDP never writes them.
    #[cfg(windows)]
    pixels: SharedPixelBuffer<Rgba8Pixel>,
    #[cfg(not(windows))]
    pixels: Vec<u8>,
}

const BYTES_PER_PIXEL: usize = 4;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct DirtyRect {
    x: i32,
    y: i32,
    width: i32,
    height: i32,
}

impl DirtyRect {
    fn clipped(self, frame_width: usize, frame_height: usize) -> Option<Self> {
        if self.width <= 0 || self.height <= 0 {
            return None;
        }

        let left = i64::from(self.x).max(0);
        let top = i64::from(self.y).max(0);
        let right = (i64::from(self.x) + i64::from(self.width)).min(frame_width as i64);
        let bottom = (i64::from(self.y) + i64::from(self.height)).min(frame_height as i64);
        if right <= left || bottom <= top {
            return None;
        }

        Some(Self {
            x: left as i32,
            y: top as i32,
            width: (right - left) as i32,
            height: (bottom - top) as i32,
        })
    }

    fn area_pixels(self) -> u64 {
        u64::try_from(self.width).unwrap_or(0) * u64::try_from(self.height).unwrap_or(0)
    }
}

/// Return a non-overlapping rectangular partition of the clipped dirty area.
///
/// FreeRDP can report overlapping rectangles in one EndPaint batch. Copying
/// those rectangles directly repeats bytes in the overlap, so partition their
/// union before copying.
fn coalesce_dirty_rects(
    rects: &[DirtyRect],
    frame_width: usize,
    frame_height: usize,
) -> Vec<DirtyRect> {
    let rects: Vec<_> = rects
        .iter()
        .copied()
        .filter_map(|rect| rect.clipped(frame_width, frame_height))
        .collect();
    if rects.is_empty() {
        return Vec::new();
    }

    let mut y_edges = Vec::with_capacity(rects.len() * 2);
    for rect in &rects {
        y_edges.push(rect.y);
        y_edges.push(rect.y + rect.height);
    }
    y_edges.sort_unstable();
    y_edges.dedup();

    let mut result = Vec::new();
    for y_pair in y_edges.windows(2) {
        let y = y_pair[0];
        let height = y_pair[1] - y;
        if height <= 0 {
            continue;
        }

        let mut intervals: Vec<(i32, i32)> = rects
            .iter()
            .filter(|rect| rect.y <= y && rect.y + rect.height >= y + height)
            .map(|rect| (rect.x, rect.x + rect.width))
            .collect();
        intervals.sort_unstable();

        let mut merged = Vec::new();
        for (start, end) in intervals {
            let Some(last) = merged.last_mut() else {
                merged.push((start, end));
                continue;
            };
            if start <= last.1 {
                last.1 = last.1.max(end);
            } else {
                merged.push((start, end));
            }
        }

        for (x, right) in merged {
            let width = right - x;
            if width <= 0 {
                continue;
            }
            if let Some(previous) = result.iter_mut().rev().find(|previous: &&mut DirtyRect| {
                previous.x == x && previous.width == width && previous.y + previous.height == y
            }) {
                previous.height += height;
            } else {
                result.push(DirtyRect {
                    x,
                    y,
                    width,
                    height,
                });
            }
        }
    }
    result
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum DirtyFrameMode {
    Legacy,
    Optimized,
}

impl DirtyFrameMode {
    fn as_str(self) -> &'static str {
        match self {
            Self::Legacy => "legacy",
            Self::Optimized => "optimized",
        }
    }

    fn from_environment() -> Self {
        match std::env::var("CHUZI_RDP_DIRTY_FRAME_MODE") {
            Ok(value) if value.trim().eq_ignore_ascii_case("legacy") => Self::Legacy,
            _ => Self::Optimized,
        }
    }
}

#[derive(Debug)]
struct CpuFramebuffer {
    width: u32,
    height: u32,
    generation: u64,
    pixels: Vec<u8>,
}

#[derive(Debug, Default)]
struct Framebuffers {
    latest: Option<CpuFramebuffer>,
    latest_snapshot_generation: u64,
    next_generation: u64,
}

#[derive(Debug, Clone, Copy)]
struct FrameCopyResult {
    actual_copy_bytes: usize,
    overwrote_pending: bool,
}

impl Framebuffers {
    fn update(
        &mut self,
        width: u32,
        height: u32,
        source: &[u8],
        stride: usize,
        dirty_rects: &[DirtyRect],
    ) -> Result<FrameCopyResult, String> {
        let width_usize = usize::try_from(width).map_err(|_| "RDP framebuffer width is invalid")?;
        let height_usize =
            usize::try_from(height).map_err(|_| "RDP framebuffer height is invalid")?;
        let width_i32 = i32::try_from(width).map_err(|_| "RDP framebuffer width is invalid")?;
        let height_i32 = i32::try_from(height).map_err(|_| "RDP framebuffer height is invalid")?;
        let row_bytes = width_usize
            .checked_mul(BYTES_PER_PIXEL)
            .ok_or("RDP framebuffer row is too large")?;
        let source_bytes = stride
            .checked_mul(height_usize)
            .ok_or("RDP framebuffer stride is too large")?;
        let frame_bytes = row_bytes
            .checked_mul(height_usize)
            .ok_or("RDP framebuffer is too large")?;
        if stride < row_bytes || source.len() < source_bytes {
            return Err("RDP framebuffer layout is invalid".to_owned());
        }

        let resized = self
            .latest
            .as_ref()
            .map(|frame| frame.width != width || frame.height != height)
            .unwrap_or(true);
        let overwrote_pending = self
            .latest
            .as_ref()
            .map(|frame| frame.generation > self.latest_snapshot_generation)
            .unwrap_or(false);

        if resized {
            self.latest = Some(CpuFramebuffer {
                width,
                height,
                generation: 0,
                pixels: vec![0; frame_bytes],
            });
        }
        let frame = self.latest.as_mut().expect("framebuffer was initialized");
        let copied_rects;
        let actual_copy_bytes;
        if resized {
            // A resize has no previous complete image to retain. Prime the
            // persistent buffer once, then later EndPaint batches copy only
            // their dirty partition.
            copied_rects = vec![DirtyRect {
                x: 0,
                y: 0,
                width: width_i32,
                height: height_i32,
            }];
            actual_copy_bytes = frame_bytes;
        } else {
            copied_rects = coalesce_dirty_rects(dirty_rects, width_usize, height_usize);
            actual_copy_bytes = copied_rects
                .iter()
                .map(|rect| rect.area_pixels() as usize * BYTES_PER_PIXEL)
                .sum();
        }

        copy_dirty_rects(&mut frame.pixels, source, stride, row_bytes, &copied_rects)?;
        self.next_generation = self.next_generation.wrapping_add(1);
        if self.next_generation == 0 {
            self.next_generation = 1;
        }
        frame.generation = self.next_generation;

        Ok(FrameCopyResult {
            actual_copy_bytes,
            overwrote_pending,
        })
    }

    fn snapshot_latest(&mut self) -> Option<RdpFrame> {
        let frame = self.latest.as_ref()?;
        if frame.generation <= self.latest_snapshot_generation {
            return None;
        }

        #[cfg(windows)]
        let pixels = {
            let mut pixels = SharedPixelBuffer::<Rgba8Pixel>::new(frame.width, frame.height);
            pixels.make_mut_bytes().copy_from_slice(&frame.pixels);
            pixels
        };
        #[cfg(not(windows))]
        let pixels = frame.pixels.clone();

        self.latest_snapshot_generation = frame.generation;
        Some(RdpFrame {
            width: frame.width,
            height: frame.height,
            pixels,
        })
    }
}

fn copy_dirty_rects(
    destination: &mut [u8],
    source: &[u8],
    source_stride: usize,
    destination_stride: usize,
    rects: &[DirtyRect],
) -> Result<(), String> {
    for rect in rects {
        let x = usize::try_from(rect.x).map_err(|_| "RDP dirty rectangle has a negative x")?;
        let y = usize::try_from(rect.y).map_err(|_| "RDP dirty rectangle has a negative y")?;
        let width =
            usize::try_from(rect.width).map_err(|_| "RDP dirty rectangle has an invalid width")?;
        let height = usize::try_from(rect.height)
            .map_err(|_| "RDP dirty rectangle has an invalid height")?;
        let row_bytes = width
            .checked_mul(BYTES_PER_PIXEL)
            .ok_or("RDP dirty rectangle row is too large")?;
        let x_offset = x
            .checked_mul(BYTES_PER_PIXEL)
            .ok_or("RDP dirty rectangle x offset is too large")?;
        let source_start = y
            .checked_mul(source_stride)
            .and_then(|offset| offset.checked_add(x_offset))
            .ok_or("RDP dirty rectangle source offset is too large")?;
        let destination_start = y
            .checked_mul(destination_stride)
            .and_then(|offset| offset.checked_add(x_offset))
            .ok_or("RDP dirty rectangle destination offset is too large")?;

        for row in 0..height {
            let source_row_offset = row
                .checked_mul(source_stride)
                .ok_or("RDP dirty rectangle source row is too large")?;
            let destination_row_offset = row
                .checked_mul(destination_stride)
                .ok_or("RDP dirty rectangle destination row is too large")?;
            let source_offset = source_start
                .checked_add(source_row_offset)
                .ok_or("RDP dirty rectangle source row is too large")?;
            let destination_offset = destination_start
                .checked_add(destination_row_offset)
                .ok_or("RDP dirty rectangle destination row is too large")?;
            let source_row = source
                .get(source_offset..source_offset + row_bytes)
                .ok_or("RDP dirty rectangle exceeds source framebuffer")?;
            let destination_row = destination
                .get_mut(destination_offset..destination_offset + row_bytes)
                .ok_or("RDP dirty rectangle exceeds destination framebuffer")?;
            destination_row.copy_from_slice(source_row);
        }
    }
    Ok(())
}

#[cfg_attr(not(windows), allow(dead_code))]
#[derive(Debug, Clone, Copy)]
enum PointerAction {
    Move,
    Down,
    Up,
    Cancel,
}

#[cfg_attr(not(windows), allow(dead_code))]
#[derive(Debug, Clone, Copy)]
enum PointerButton {
    Left,
    Right,
    Middle,
    Other,
}

#[cfg_attr(not(windows), allow(dead_code))]
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
enum RemoteKey {
    ScanCode { code: u8, extended: bool },
    Unicode(u16),
}

#[cfg_attr(not(windows), allow(dead_code))]
#[derive(Debug, Clone, Copy)]
enum KeyAction {
    Down { repeat: bool },
    Up,
}

#[cfg_attr(not(windows), allow(dead_code))]
#[derive(Debug, Clone)]
enum RdpInput {
    Pointer {
        action: PointerAction,
        button: PointerButton,
        x: f32,
        y: f32,
        viewport_width: f32,
        viewport_height: f32,
    },
    Wheel {
        delta_x: f32,
        delta_y: f32,
        x: f32,
        y: f32,
        viewport_width: f32,
        viewport_height: f32,
    },
    Key {
        action: KeyAction,
        keys: Vec<RemoteKey>,
    },
    ReleaseAllKeys,
}

#[cfg_attr(not(any(windows, test)), allow(dead_code))]
fn wheel_delta_to_rdp(delta: f32) -> Option<(bool, u16)> {
    if !delta.is_finite() || delta == 0.0 {
        return None;
    }

    let units = (delta.abs() * 2.0).round().clamp(1.0, 255.0) as u16;
    Some((delta.is_sign_negative(), units))
}

#[cfg_attr(not(any(windows, test)), allow(dead_code))]
fn special_key_virtual_code(key: char) -> Option<u16> {
    let code = match key {
        '\u{0008}' => 0x08, // Backspace
        '\u{0009}' => 0x09, // Tab
        '\u{000a}' => 0x0d, // Return
        '\u{0010}' => 0x10, // Left Shift
        '\u{0011}' => 0x11, // Left Control
        '\u{0012}' => 0x12, // Left Alt
        '\u{0013}' => 0xa5, // Right Alt
        '\u{0014}' => 0x14, // Caps Lock
        '\u{0015}' => 0xa1, // Right Shift
        '\u{0016}' => 0xa3, // Right Control
        '\u{0017}' => 0x5b, // Left Windows
        '\u{0018}' => 0x5c, // Right Windows
        '\u{001b}' => 0x1b, // Escape
        '\u{007f}' => 0x2e, // Delete
        ' ' => 0x20,
        '\u{f700}' => 0x26,                                      // Up
        '\u{f701}' => 0x28,                                      // Down
        '\u{f702}' => 0x25,                                      // Left
        '\u{f703}' => 0x27,                                      // Right
        '\u{f704}'..='\u{f71b}' => 0x70 + (key as u16 - 0xf704), // F1-F24
        '\u{f727}' => 0x2d,                                      // Insert
        '\u{f729}' => 0x24,                                      // Home
        '\u{f72b}' => 0x23,                                      // End
        '\u{f72c}' => 0x21,                                      // Page Up
        '\u{f72d}' => 0x22,                                      // Page Down
        '\u{f72f}' => 0x91,                                      // Scroll Lock
        '\u{f730}' => 0x13,                                      // Pause
        '\u{f731}' => 0x2c,                                      // Print Screen
        '\u{f735}' => 0x5d,                                      // Context Menu
        _ => return None,
    };
    Some(code)
}

fn take_pressed_key(
    pressed: &mut HashMap<String, Vec<RemoteKey>>,
    key: &str,
    encoded: &[RemoteKey],
) -> Option<Vec<RemoteKey>> {
    pressed.remove(key).or_else(|| {
        let matching_key = pressed
            .iter()
            .find_map(|(pressed_text, keys)| (keys == encoded).then(|| pressed_text.clone()))?;
        pressed.remove(&matching_key)
    })
}

#[cfg(windows)]
fn encode_remote_key(text: &str) -> Vec<RemoteKey> {
    use windows_sys::Win32::UI::Input::KeyboardAndMouse::{
        MapVirtualKeyW, VkKeyScanW, MAPVK_VK_TO_VSC_EX,
    };

    fn scan_code(virtual_code: u16) -> Option<RemoteKey> {
        let mapped = unsafe { MapVirtualKeyW(u32::from(virtual_code), MAPVK_VK_TO_VSC_EX) };
        if mapped == 0 {
            return None;
        }
        let prefix = (mapped >> 8) as u8;
        Some(RemoteKey::ScanCode {
            code: mapped as u8,
            extended: prefix == 0xe0,
        })
    }

    let mut keys = Vec::new();
    for character in text.chars() {
        let virtual_code = special_key_virtual_code(character).or_else(|| {
            if character.is_control() || ('\u{e000}'..='\u{f8ff}').contains(&character) {
                return None;
            }
            if character as u32 <= u16::MAX as u32 {
                let mapped = unsafe { VkKeyScanW(character as u16) };
                (mapped != -1).then_some(mapped as u16 & 0x00ff)
            } else {
                None
            }
        });

        if let Some(virtual_code) = virtual_code {
            if let Some(key) = scan_code(virtual_code) {
                keys.push(key);
                continue;
            }
        }

        if !character.is_control() && !('\u{e000}'..='\u{f8ff}').contains(&character) {
            let mut utf16 = [0; 2];
            keys.extend(
                character
                    .encode_utf16(&mut utf16)
                    .iter()
                    .copied()
                    .map(RemoteKey::Unicode),
            );
        }
    }
    keys
}

#[cfg(not(windows))]
fn encode_remote_key(_text: &str) -> Vec<RemoteKey> {
    Vec::new()
}

#[cfg_attr(not(windows), allow(dead_code))]
struct PendingUpdate {
    state: String,
    status: String,
    frame: Option<RdpFrame>,
}

#[cfg_attr(not(windows), allow(dead_code))]
struct RdpPerfStats {
    enabled: bool,
    mode: DirtyFrameMode,
    started: Instant,
    last_report: Mutex<Instant>,
    last_ui_handoff: Mutex<Option<Instant>>,
    last_ui_tick: Mutex<Option<Instant>>,
    frame_width: AtomicU64,
    frame_height: AtomicU64,
    rdp_update_batches: AtomicU64,
    dirty_rect_count: AtomicU64,
    dirty_area_pixels: AtomicU64,
    dirty_union_area_pixels: AtomicU64,
    full_frame_copy_bytes: AtomicU64,
    actual_copy_bytes: AtomicU64,
    framebuffer_copy_us: AtomicU64,
    frame_to_image_us: AtomicU64,
    ui_handoff_interval_us: AtomicU64,
    ui_handoff_interval_samples_us: Mutex<Vec<u64>>,
    ui_handoff_recent_samples_us: Mutex<Vec<u64>>,
    coalesced_frames: AtomicU64,
    queue_overwrites: AtomicU64,
    ui_handoffs: AtomicU64,
    ui_handoff_time_us: AtomicU64,
    ui_ticks: AtomicU64,
    ui_tick_commits: AtomicU64,
    ui_tick_interval_us: AtomicU64,
    ui_tick_processing_us: AtomicU64,
    log_writer: Option<Mutex<BufWriter<File>>>,
    emit_stderr: bool,
}

#[cfg_attr(not(windows), allow(dead_code))]
impl RdpPerfStats {
    fn new(mode: DirtyFrameMode) -> Self {
        let enabled = std::env::var("CHUZI_RDP_PERF")
            .map(|value| {
                matches!(
                    value.trim().to_ascii_lowercase().as_str(),
                    "1" | "true" | "yes"
                )
            })
            .unwrap_or(false);
        Self::configured(mode, enabled, None, enabled)
    }

    fn configured(
        mode: DirtyFrameMode,
        enabled: bool,
        log_file: Option<File>,
        emit_stderr: bool,
    ) -> Self {
        let now = Instant::now();
        Self {
            enabled,
            mode,
            started: now,
            last_report: Mutex::new(now),
            last_ui_handoff: Mutex::new(None),
            last_ui_tick: Mutex::new(None),
            frame_width: AtomicU64::new(0),
            frame_height: AtomicU64::new(0),
            rdp_update_batches: AtomicU64::new(0),
            dirty_rect_count: AtomicU64::new(0),
            dirty_area_pixels: AtomicU64::new(0),
            dirty_union_area_pixels: AtomicU64::new(0),
            full_frame_copy_bytes: AtomicU64::new(0),
            actual_copy_bytes: AtomicU64::new(0),
            framebuffer_copy_us: AtomicU64::new(0),
            frame_to_image_us: AtomicU64::new(0),
            ui_handoff_interval_us: AtomicU64::new(0),
            ui_handoff_interval_samples_us: Mutex::new(Vec::new()),
            ui_handoff_recent_samples_us: Mutex::new(Vec::new()),
            coalesced_frames: AtomicU64::new(0),
            queue_overwrites: AtomicU64::new(0),
            ui_handoffs: AtomicU64::new(0),
            ui_handoff_time_us: AtomicU64::new(0),
            ui_ticks: AtomicU64::new(0),
            ui_tick_commits: AtomicU64::new(0),
            ui_tick_interval_us: AtomicU64::new(0),
            ui_tick_processing_us: AtomicU64::new(0),
            log_writer: log_file.map(|file| Mutex::new(BufWriter::new(file))),
            emit_stderr,
        }
    }

    fn record_rdp_update(
        &self,
        width: usize,
        height: usize,
        dirty_rects: &[DirtyRect],
        union_rects: &[DirtyRect],
    ) {
        if !self.enabled {
            return;
        }
        self.frame_width.store(width as u64, Ordering::Relaxed);
        self.frame_height.store(height as u64, Ordering::Relaxed);
        self.rdp_update_batches.fetch_add(1, Ordering::Relaxed);
        self.dirty_rect_count
            .fetch_add(dirty_rects.len() as u64, Ordering::Relaxed);
        self.dirty_area_pixels.fetch_add(
            dirty_rects.iter().map(|rect| rect.area_pixels()).sum(),
            Ordering::Relaxed,
        );
        self.dirty_union_area_pixels.fetch_add(
            union_rects.iter().map(|rect| rect.area_pixels()).sum(),
            Ordering::Relaxed,
        );
    }

    fn record_copy(
        &self,
        actual_bytes: usize,
        full_frame_bytes: usize,
        elapsed: std::time::Duration,
        overwrote_pending: bool,
    ) {
        if !self.enabled {
            return;
        }
        self.full_frame_copy_bytes
            .fetch_add(full_frame_bytes as u64, Ordering::Relaxed);
        self.actual_copy_bytes
            .fetch_add(actual_bytes as u64, Ordering::Relaxed);
        self.framebuffer_copy_us
            .fetch_add(elapsed.as_micros() as u64, Ordering::Relaxed);
        if overwrote_pending {
            self.record_coalesced();
        }
        self.report_if_due(false);
    }

    fn record_coalesced(&self) {
        if !self.enabled {
            return;
        }
        self.coalesced_frames.fetch_add(1, Ordering::Relaxed);
        self.queue_overwrites.fetch_add(1, Ordering::Relaxed);
    }

    fn record_frame_to_image(&self, elapsed: std::time::Duration) {
        if self.enabled {
            self.frame_to_image_us
                .fetch_add(elapsed.as_micros() as u64, Ordering::Relaxed);
        }
    }

    fn record_ui_handoff(&self, elapsed: std::time::Duration) {
        if !self.enabled {
            return;
        }
        self.ui_handoffs.fetch_add(1, Ordering::Relaxed);
        self.ui_handoff_time_us
            .fetch_add(elapsed.as_micros() as u64, Ordering::Relaxed);
        if let Ok(mut last_ui_handoff) = self.last_ui_handoff.lock() {
            let now = Instant::now();
            if let Some(previous) = *last_ui_handoff {
                let interval_us = previous.elapsed().as_micros() as u64;
                self.ui_handoff_interval_us
                    .fetch_add(interval_us, Ordering::Relaxed);
                if let Ok(mut samples) = self.ui_handoff_interval_samples_us.lock() {
                    samples.push(interval_us);
                }
                if let Ok(mut samples) = self.ui_handoff_recent_samples_us.lock() {
                    samples.push(interval_us);
                    if samples.len() > 240 {
                        let excess = samples.len() - 240;
                        samples.drain(..excess);
                    }
                }
            }
            *last_ui_handoff = Some(now);
        }
        self.report_if_due(false);
    }

    fn record_ui_tick(&self, started: Instant, committed: bool) {
        if !self.enabled {
            return;
        }
        let now = Instant::now();
        if let Ok(mut last_ui_tick) = self.last_ui_tick.lock() {
            if let Some(previous) = *last_ui_tick {
                self.ui_tick_interval_us
                    .fetch_add(previous.elapsed().as_micros() as u64, Ordering::Relaxed);
            }
            *last_ui_tick = Some(now);
        }
        self.ui_ticks.fetch_add(1, Ordering::Relaxed);
        self.ui_tick_processing_us
            .fetch_add(started.elapsed().as_micros() as u64, Ordering::Relaxed);
        if committed {
            self.ui_tick_commits.fetch_add(1, Ordering::Relaxed);
            self.report_if_due(false);
        }
    }

    fn report_if_due(&self, force: bool) {
        if !self.enabled {
            return;
        }
        let Ok(mut last_report) = self.last_report.lock() else {
            return;
        };
        if !force && last_report.elapsed() < std::time::Duration::from_secs(1) {
            return;
        }
        *last_report = Instant::now();
        let interval_samples = self
            .ui_handoff_interval_samples_us
            .lock()
            .map(|mut samples| std::mem::take(&mut *samples))
            .unwrap_or_default();
        let full_frame_pixels =
            self.full_frame_copy_bytes.load(Ordering::Relaxed) / BYTES_PER_PIXEL as u64;
        let dirty_area_ratio = if full_frame_pixels == 0 {
            0.0
        } else {
            self.dirty_union_area_pixels.load(Ordering::Relaxed) as f64 / full_frame_pixels as f64
        };
        let line = format!(
            "rdp_perf {{\"elapsed_ms\":{},\"dirty_frame_mode\":\"{}\",\"frame_width\":{},\"frame_height\":{},\"rdp_update_batches\":{},\"dirty_rect_count\":{},\"dirty_area_pixels\":{},\"dirty_union_area_pixels\":{},\"dirty_area_ratio\":{},\"full_frame_copy_bytes\":{},\"actual_copy_bytes\":{},\"framebuffer_copy_us\":{},\"frame_to_image_us\":{},\"ui_handoffs\":{},\"ui_handoff_time_us\":{},\"ui_handoff_interval_us\":{},\"ui_handoff_interval_samples_us\":{:?},\"coalesced_frames\":{},\"queue_overwrites\":{},\"ui_ticks\":{},\"ui_tick_commits\":{},\"ui_tick_interval_us\":{},\"ui_tick_processing_us\":{}}}",
            self.started.elapsed().as_millis(),
            self.mode.as_str(),
            self.frame_width.load(Ordering::Relaxed),
            self.frame_height.load(Ordering::Relaxed),
            self.rdp_update_batches.load(Ordering::Relaxed),
            self.dirty_rect_count.load(Ordering::Relaxed),
            self.dirty_area_pixels.load(Ordering::Relaxed),
            self.dirty_union_area_pixels.load(Ordering::Relaxed),
            dirty_area_ratio,
            self.full_frame_copy_bytes.load(Ordering::Relaxed),
            self.actual_copy_bytes.load(Ordering::Relaxed),
            self.framebuffer_copy_us.load(Ordering::Relaxed),
            self.frame_to_image_us.load(Ordering::Relaxed),
            self.ui_handoffs.load(Ordering::Relaxed),
            self.ui_handoff_time_us.load(Ordering::Relaxed),
            self.ui_handoff_interval_us.load(Ordering::Relaxed),
            interval_samples,
            self.coalesced_frames.load(Ordering::Relaxed),
            self.queue_overwrites.load(Ordering::Relaxed),
            self.ui_ticks.load(Ordering::Relaxed),
            self.ui_tick_commits.load(Ordering::Relaxed),
            self.ui_tick_interval_us.load(Ordering::Relaxed),
            self.ui_tick_processing_us.load(Ordering::Relaxed),
        );
        if self.emit_stderr {
            eprintln!("{line}");
        }
        if let Some(writer) = &self.log_writer {
            if let Ok(mut writer) = writer.lock() {
                let _ = writeln!(writer, "{line}");
                let _ = writer.flush();
            }
        }
    }

    fn hud_text(&self, presentmon: &str) -> String {
        let elapsed = self.started.elapsed().as_secs_f64().max(0.001);
        let updates = self.rdp_update_batches.load(Ordering::Relaxed);
        let handoffs = self.ui_handoffs.load(Ordering::Relaxed);
        let full_frame_pixels =
            self.full_frame_copy_bytes.load(Ordering::Relaxed) as f64 / BYTES_PER_PIXEL as f64;
        let dirty_ratio = if full_frame_pixels == 0.0 {
            0.0
        } else {
            self.dirty_union_area_pixels.load(Ordering::Relaxed) as f64 / full_frame_pixels
        };
        let copy_mib_s =
            self.actual_copy_bytes.load(Ordering::Relaxed) as f64 / elapsed / (1024.0 * 1024.0);
        let avg_copy_us = if updates == 0 {
            0
        } else {
            self.framebuffer_copy_us.load(Ordering::Relaxed) / updates
        };
        let avg_snapshot_us = if handoffs == 0 {
            0
        } else {
            self.frame_to_image_us.load(Ordering::Relaxed) / handoffs
        };
        let intervals = self
            .ui_handoff_recent_samples_us
            .lock()
            .map(|samples| {
                let mut sorted = samples.clone();
                sorted.sort_unstable();
                sorted
            })
            .unwrap_or_default();
        let p50 = percentile_us(&intervals, 0.50);
        let p95 = percentile_us(&intervals, 0.95);
        let delivered = handoffs as f64
            / (handoffs + self.coalesced_frames.load(Ordering::Relaxed)).max(1) as f64
            * 100.0;
        format!(
            "{} | RDP {:.1}/s | dirty {:.2}% | copy {:.2} MiB/s | copy {} us | snapshot {} us\nUI handoff p50/p95: {}/{} ms | delivered {:.1}%\n{}",
            self.mode.as_str(), updates as f64 / elapsed, dirty_ratio * 100.0, copy_mib_s, avg_copy_us,
            avg_snapshot_us, p50 as f64 / 1000.0, p95 as f64 / 1000.0, delivered, presentmon
        )
    }
}

fn percentile_us(sorted_samples: &[u64], percentile: f64) -> u64 {
    if sorted_samples.is_empty() {
        return 0;
    }
    let index = ((sorted_samples.len() as f64 * percentile).ceil() as usize)
        .saturating_sub(1)
        .min(sorted_samples.len() - 1);
    sorted_samples[index]
}

fn read_available_csv_records(
    reader: &mut csv::Reader<File>,
    mut consume: impl FnMut(&csv::StringRecord),
) -> csv::Result<()> {
    use std::io::SeekFrom;

    let mut record = csv::StringRecord::new();
    loop {
        match reader.read_record(&mut record) {
            Ok(true) => consume(&record),
            Ok(false) => {
                let position = reader.position().clone();
                reader.seek_raw(SeekFrom::Start(position.byte()), position)?;
                return Ok(());
            }
            Err(error) => return Err(error),
        }
    }
}

#[cfg_attr(not(windows), allow(dead_code))]
struct SharedState {
    update: Mutex<PendingUpdate>,
    framebuffers: Mutex<Framebuffers>,
    certificate_failure: Mutex<Option<String>>,
    dirty_frame_mode: DirtyFrameMode,
    perf: RdpPerfStats,
    pending: AtomicBool,
    stop: AtomicBool,
}

impl SharedState {
    fn new() -> Self {
        Self::with_mode(DirtyFrameMode::from_environment())
    }

    fn with_mode(dirty_frame_mode: DirtyFrameMode) -> Self {
        let enabled = std::env::var("CHUZI_RDP_PERF")
            .map(|value| {
                matches!(
                    value.trim().to_ascii_lowercase().as_str(),
                    "1" | "true" | "yes"
                )
            })
            .unwrap_or(false);
        Self::with_performance(dirty_frame_mode, enabled, None, enabled)
    }

    fn with_performance(
        dirty_frame_mode: DirtyFrameMode,
        enabled: bool,
        log_file: Option<File>,
        emit_stderr: bool,
    ) -> Self {
        Self {
            update: Mutex::new(PendingUpdate {
                state: "connecting".to_owned(),
                status: "正在连接 RDP…".to_owned(),
                frame: None,
            }),
            framebuffers: Mutex::new(Framebuffers::default()),
            certificate_failure: Mutex::new(None),
            dirty_frame_mode,
            perf: RdpPerfStats::configured(dirty_frame_mode, enabled, log_file, emit_stderr),
            pending: AtomicBool::new(false),
            stop: AtomicBool::new(false),
        }
    }

    fn set_state(&self, state: impl Into<String>, status: impl Into<String>) {
        let state = state.into();
        let status = status.into();
        let mut changed = false;
        if let Ok(mut update) = self.update.lock() {
            if update.state != state || update.status != status {
                update.state = state;
                update.status = status;
                changed = true;
            }
        }
        if changed {
            self.pending.store(true, Ordering::Release);
        }
    }

    #[cfg_attr(not(windows), allow(dead_code))]
    fn set_certificate_failure(&self, status: impl Into<String>) {
        let status = status.into();
        if let Ok(mut failure) = self.certificate_failure.lock() {
            *failure = Some(status.clone());
        }
        self.set_state("connecting", status);
    }

    #[cfg_attr(not(windows), allow(dead_code))]
    fn certificate_failure(&self) -> Option<String> {
        self.certificate_failure
            .lock()
            .ok()
            .and_then(|failure| failure.clone())
    }

    #[cfg_attr(not(windows), allow(dead_code))]
    fn set_frame(&self, frame: RdpFrame) {
        if let Ok(mut update) = self.update.lock() {
            // Keep the newest complete framebuffer. A stale frame must not
            // block a newer RDP update from reaching the UI.
            if update.frame.is_some() {
                self.perf.record_coalesced();
            }
            update.frame = Some(frame);
            self.pending.store(true, Ordering::Release);
        }
    }

    #[cfg_attr(not(windows), allow(dead_code))]
    fn mode(&self) -> DirtyFrameMode {
        self.dirty_frame_mode
    }

    #[cfg_attr(not(windows), allow(dead_code))]
    fn update_framebuffer(
        &self,
        width: u32,
        height: u32,
        source: &[u8],
        stride: usize,
        dirty_rects: &[DirtyRect],
    ) -> Result<(), String> {
        let width_usize = usize::try_from(width).map_err(|_| "RDP framebuffer width is invalid")?;
        let height_usize =
            usize::try_from(height).map_err(|_| "RDP framebuffer height is invalid")?;
        let union_rects = coalesce_dirty_rects(dirty_rects, width_usize, height_usize);
        self.perf
            .record_rdp_update(width_usize, height_usize, dirty_rects, &union_rects);
        let started = Instant::now();
        let result = {
            let mut framebuffers = self
                .framebuffers
                .lock()
                .map_err(|_| "RDP framebuffer lock is poisoned".to_owned())?;
            framebuffers.update(width, height, source, stride, dirty_rects)?
        };
        let full_frame_bytes = width_usize
            .checked_mul(height_usize)
            .and_then(|pixels| pixels.checked_mul(BYTES_PER_PIXEL))
            .ok_or("RDP framebuffer is too large")?;
        self.perf.record_copy(
            result.actual_copy_bytes,
            full_frame_bytes,
            started.elapsed(),
            result.overwrote_pending,
        );
        self.pending.store(true, Ordering::Release);
        Ok(())
    }

    #[cfg_attr(not(windows), allow(dead_code))]
    fn snapshot_latest_frame(&self) -> Option<RdpFrame> {
        self.framebuffers
            .lock()
            .ok()
            .and_then(|mut framebuffers| framebuffers.snapshot_latest())
    }

    #[cfg_attr(not(windows), allow(dead_code))]
    fn poll(&self) -> Option<PendingUpdate> {
        if !self.pending.swap(false, Ordering::AcqRel) {
            return None;
        }

        let Ok(mut update) = self.update.lock() else {
            return Some(PendingUpdate {
                state: "failed".to_owned(),
                status: "RDP 状态不可用。".to_owned(),
                frame: None,
            });
        };

        let snapshot_started = Instant::now();
        let frame = update.frame.take().or_else(|| self.snapshot_latest_frame());
        if frame.is_some() {
            self.perf.record_frame_to_image(snapshot_started.elapsed());
        }
        Some(PendingUpdate {
            state: update.state.clone(),
            status: update.status.clone(),
            frame,
        })
    }

    #[cfg_attr(not(windows), allow(dead_code))]
    fn has_pending(&self) -> bool {
        self.pending.load(Ordering::Acquire)
    }

    fn status_snapshot(&self) -> (String, String) {
        let Ok(update) = self.update.lock() else {
            return ("failed".to_owned(), "RDP 状态不可用。".to_owned());
        };
        (update.state.clone(), update.status.clone())
    }
}

pub struct DesktopRdpController {
    _window: DesktopRdpWindow,
    shared: Arc<SharedState>,
    worker: Option<JoinHandle<()>>,
    frame_timer: Timer,
    #[cfg_attr(not(windows), allow(dead_code))]
    input_tx: Sender<RdpInput>,
    #[cfg(windows)]
    presentmon: Option<Rc<RefCell<PresentMonCapture>>>,
}

impl DesktopRdpController {
    pub fn new_with_target(target: RdpTarget) -> Result<Self, String> {
        let legacy_stderr = std::env::var("CHUZI_RDP_PERF")
            .map(|value| {
                matches!(
                    value.trim().to_ascii_lowercase().as_str(),
                    "1" | "true" | "yes"
                )
            })
            .unwrap_or(false);
        Self::new_with_target_internal(target, RdpPerformanceOptions::default(), legacy_stderr)
    }

    pub fn new_with_target_and_performance(
        target: RdpTarget,
        options: RdpPerformanceOptions,
    ) -> Result<Self, String> {
        Self::new_with_target_internal(target, options, false)
    }

    fn new_with_target_internal(
        mut target: RdpTarget,
        options: RdpPerformanceOptions,
        emit_perf_stderr: bool,
    ) -> Result<Self, String> {
        target.host = target.host.trim().to_owned();
        if target.host.is_empty() {
            return Err("RDP host must not be empty".to_owned());
        }
        if target.host.contains('\0') {
            return Err("RDP host contains an invalid NUL character".to_owned());
        }
        if target.port == 0 {
            return Err("RDP port must be between 1 and 65535".to_owned());
        }
        if target
            .username
            .as_deref()
            .map(str::trim)
            .unwrap_or_default()
            .is_empty()
        {
            return Err("RDP username is required".to_owned());
        }
        if target.password.as_deref().unwrap_or_default().is_empty() {
            return Err("RDP password is required".to_owned());
        }

        let window = DesktopRdpWindow::new().map_err(|error| error.to_string())?;
        window.set_host(target.host.clone().into());
        window.set_state("connecting".into());
        window.set_status("正在连接 RDP，等待远程桌面画面…".into());
        window
            .show()
            .map_err(|error| format!("desktop_rdp_window_show: {error}"))?;

        #[cfg(windows)]
        {
            let weak = window.as_weak();
            slint::Timer::single_shot(Duration::from_millis(1), move || {
                if let Some(window) = weak.upgrade() {
                    apply_windows_window_chrome(&window);
                }
            });
        }

        let weak = window.as_weak();
        window.on_hide_window(move || {
            if let Some(window) = weak.upgrade() {
                let _ = window.hide();
            }
        });

        window
            .window()
            .on_close_requested(move || CloseRequestResponse::HideWindow);

        let perf_enabled = options.show_hud || options.record || emit_perf_stderr;
        if (options.show_hud || options.record) && options.data_dir.as_os_str().is_empty() {
            return Err("RDP 性能数据目录不可用。".to_owned());
        }
        let performance_dir = options.data_dir.join("rdp-performance");
        let mut perf_log = None;
        #[cfg(windows)]
        let mut presentmon_path: Option<PathBuf> = None;
        if options.show_hud || options.record {
            std::fs::create_dir_all(&performance_dir)
                .map_err(|error| format!("无法创建 RDP 性能数据目录：{error}"))?;
            let session_id = performance_session_id();
            if options.record {
                let log_path = performance_dir.join(format!("rdp-{session_id}.rdp.log"));
                perf_log = Some(
                    File::create(&log_path)
                        .map_err(|error| format!("无法创建 RDP 性能日志：{error}"))?,
                );
            }
            #[cfg(windows)]
            {
                presentmon_path =
                    Some(performance_dir.join(format!("rdp-{session_id}.presentmon.csv")));
            }
        }
        let shared = Arc::new(SharedState::with_performance(
            DirtyFrameMode::from_environment(),
            perf_enabled,
            perf_log,
            emit_perf_stderr,
        ));
        #[cfg(windows)]
        let presentmon = {
            presentmon_path.map(|csv_path| {
                let executable = std::env::current_exe()
                    .ok()
                    .and_then(|path| path.parent().map(PathBuf::from))
                    .map(|path| path.join("CorePayload").join("PresentMon.exe"))
                    .unwrap_or_else(|| PathBuf::from("PresentMon.exe"));
                Rc::new(RefCell::new(PresentMonCapture::start(
                    executable,
                    csv_path,
                    !options.record,
                )))
            })
        };
        let (input_tx, input_rx) = mpsc::channel();
        let input_callback_tx = input_tx.clone();
        window.on_rdp_pointer_event(move |kind, button, x, y, viewport_width, viewport_height| {
            let action = match kind.as_str() {
                "move" => PointerAction::Move,
                "down" => PointerAction::Down,
                "up" => PointerAction::Up,
                _ => PointerAction::Cancel,
            };
            let button = match button.as_str() {
                "left" => PointerButton::Left,
                "right" => PointerButton::Right,
                "middle" => PointerButton::Middle,
                _ => PointerButton::Other,
            };
            let _ = input_callback_tx.send(RdpInput::Pointer {
                action,
                button,
                x,
                y,
                viewport_width,
                viewport_height,
            });
        });
        let input_scroll_tx = input_tx.clone();
        window.on_rdp_scroll_event(
            move |delta_x, delta_y, x, y, viewport_width, viewport_height| {
                let _ = input_scroll_tx.send(RdpInput::Wheel {
                    delta_x,
                    delta_y,
                    x,
                    y,
                    viewport_width,
                    viewport_height,
                });
            },
        );

        let input_key_tx = input_tx.clone();
        let pressed_keys: Rc<RefCell<HashMap<String, Vec<RemoteKey>>>> = Default::default();
        let callback_pressed_keys = Rc::clone(&pressed_keys);
        window.on_rdp_key_event(move |kind, key, repeat| match kind.as_str() {
            "down" => {
                let mut pressed = callback_pressed_keys.borrow_mut();
                if let Some(keys) = pressed.get(key.as_str()) {
                    if repeat {
                        let _ = input_key_tx.send(RdpInput::Key {
                            action: KeyAction::Down { repeat: true },
                            keys: keys.clone(),
                        });
                    }
                    return;
                }
                let keys = encode_remote_key(key.as_str());
                if keys.is_empty() {
                    return;
                }
                pressed.insert(key.to_string(), keys.clone());
                let _ = input_key_tx.send(RdpInput::Key {
                    action: KeyAction::Down { repeat: false },
                    keys,
                });
            }
            "up" => {
                let encoded = encode_remote_key(key.as_str());
                if let Some(keys) = take_pressed_key(
                    &mut callback_pressed_keys.borrow_mut(),
                    key.as_str(),
                    &encoded,
                ) {
                    let _ = input_key_tx.send(RdpInput::Key {
                        action: KeyAction::Up,
                        keys,
                    });
                }
            }
            "release-all" => {
                callback_pressed_keys.borrow_mut().clear();
                let _ = input_key_tx.send(RdpInput::ReleaseAllKeys);
            }
            _ => {}
        });

        #[cfg(windows)]
        let worker = {
            let shared = Arc::clone(&shared);
            Some(std::thread::spawn(move || {
                freerdp::run(target, shared, input_rx)
            }))
        };

        #[cfg(not(windows))]
        let worker = {
            let _ = &input_rx;
            let _ = target;
            let _ = pressed_keys;
            shared.set_state(
                "failed",
                "FreeRDP 仅随 Windows 构建提供；当前平台没有 RDP backend。",
            );
            None
        };

        let frame_timer = Timer::default();
        #[cfg(windows)]
        {
            let timer_shared = Arc::clone(&shared);
            let timer_window = window.as_weak();
            let timer_presentmon = presentmon.clone();
            let show_hud = options.show_hud;
            let mut last_hud_update = Instant::now() - Duration::from_secs(1);
            frame_timer.start(TimerMode::Repeated, Duration::from_millis(16), move || {
                let started = Instant::now();
                let committed = timer_shared.has_pending()
                    && apply_pending_update(&timer_window, &timer_shared);
                timer_shared.perf.record_ui_tick(started, committed);
                if last_hud_update.elapsed() >= Duration::from_millis(500) {
                    last_hud_update = Instant::now();
                    let (state, _) = timer_shared.status_snapshot();
                    let presentmon_text = timer_presentmon.as_ref().map(|capture| {
                        let mut capture = capture.borrow_mut();
                        let text = capture.refresh();
                        if matches!(state.as_str(), "closed" | "failed") {
                            capture.finish();
                        }
                        text
                    });
                    if show_hud {
                        if let Some(window) = timer_window.upgrade() {
                            let presentmon_text = presentmon_text
                                .unwrap_or_else(|| "PresentMon unavailable".to_owned());
                            window.set_perf_hud_text(
                                timer_shared.perf.hud_text(&presentmon_text).into(),
                            );
                        }
                    }
                }
            });
            if options.show_hud {
                window.set_perf_hud_visible(true);
            }
        }

        Ok(Self {
            _window: window,
            shared,
            worker,
            frame_timer,
            input_tx,
            #[cfg(windows)]
            presentmon,
        })
    }

    pub fn status_snapshot(&self) -> (String, String) {
        self.shared.status_snapshot()
    }
}

impl Drop for DesktopRdpController {
    fn drop(&mut self) {
        self.shared.stop.store(true, Ordering::SeqCst);
        self.frame_timer.stop();
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
        self.shared.perf.report_if_due(true);
        #[cfg(windows)]
        if let Some(presentmon) = self.presentmon.take() {
            presentmon.borrow_mut().finish();
        }
    }
}

fn performance_session_id() -> String {
    let micros = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|elapsed| elapsed.as_micros())
        .unwrap_or_default();
    format!("{}-{}", micros, std::process::id())
}

#[cfg(windows)]
struct PresentMonCapture {
    child: Option<std::process::Child>,
    csv_path: PathBuf,
    delete_csv_on_drop: bool,
    reader: Option<csv::Reader<File>>,
    present_interval_column: Option<usize>,
    dropped_column: Option<usize>,
    recent_intervals: std::collections::VecDeque<(Instant, f64)>,
    dropped_presents: u64,
    status: String,
    finished: bool,
}

#[cfg(windows)]
impl PresentMonCapture {
    fn start(executable: PathBuf, csv_path: PathBuf, delete_csv_on_drop: bool) -> Self {
        use std::os::windows::process::CommandExt;
        use std::process::{Command, Stdio};

        let (child, status) = if !executable.is_file() {
            (None, "PresentMon missing from CorePayload".to_owned())
        } else {
            match Command::new(&executable)
                .arg("--process_name")
                .arg("Chuzi.Native.Windows.exe")
                .arg("--output_file")
                .arg(&csv_path)
                .stdin(Stdio::null())
                .stdout(Stdio::null())
                .stderr(Stdio::null())
                .creation_flags(0x08000000)
                .spawn()
            {
                Ok(child) => (Some(child), "PresentMon starting".to_owned()),
                Err(error) => (None, format!("PresentMon could not start: {error}")),
            }
        };
        Self {
            child,
            csv_path,
            delete_csv_on_drop,
            reader: None,
            present_interval_column: None,
            dropped_column: None,
            recent_intervals: std::collections::VecDeque::new(),
            dropped_presents: 0,
            status,
            finished: false,
        }
    }

    fn refresh(&mut self) -> String {
        let mut exited = None;
        if let Some(child) = self.child.as_mut() {
            match child.try_wait() {
                Ok(Some(status)) => exited = Some(status),
                Ok(None) => self.status = "PresentMon running".to_owned(),
                Err(_) => self.status = "PresentMon status unavailable".to_owned(),
            }
        }
        if let Some(status) = exited {
            self.status = if status.success() {
                format!("PresentMon stopped ({})", status.code().unwrap_or(0))
            } else {
                format!(
                    "PresentMon failed ({}); ETW access requires Administrator or Performance Log Users membership",
                    status.code().unwrap_or(-1)
                )
            };
            self.child.take();
        }
        self.read_available_rows();
        let now = Instant::now();
        while self
            .recent_intervals
            .front()
            .map(|(time, _)| now.duration_since(*time) > Duration::from_secs(3))
            .unwrap_or(false)
        {
            self.recent_intervals.pop_front();
        }
        if self.recent_intervals.is_empty() {
            return format!("{} | waiting for presentation samples", self.status);
        }
        let average_ms = self
            .recent_intervals
            .iter()
            .map(|(_, interval)| interval)
            .sum::<f64>()
            / self.recent_intervals.len() as f64;
        format!(
            "PresentMon {:.1} FPS | dropped {} | {}",
            1000.0 / average_ms.max(0.001),
            self.dropped_presents,
            self.status
        )
    }

    fn finish(&mut self) {
        if self.finished {
            return;
        }
        if let Some(mut child) = self.child.take() {
            let _ = child.kill();
            let _ = child.wait();
            if self.status == "PresentMon running" || self.status == "PresentMon starting" {
                self.status = "PresentMon stopped with RDP session".to_owned();
            }
        }
        self.reader.take();
        if self.delete_csv_on_drop {
            let _ = std::fs::remove_file(&self.csv_path);
        }
        self.finished = true;
    }

    fn read_available_rows(&mut self) {
        if self.reader.is_none() {
            let Ok(file) = File::open(&self.csv_path) else {
                return;
            };
            let mut reader = csv::ReaderBuilder::new().flexible(true).from_reader(file);
            let Ok(headers) = reader.headers() else {
                return;
            };
            let column = |names: &[&str]| {
                headers.iter().position(|header| {
                    let header = header.trim_start_matches('\u{feff}').trim();
                    names.iter().any(|name| header.eq_ignore_ascii_case(name))
                })
            };
            self.present_interval_column = column(&["MsBetweenPresents"]);
            self.dropped_column = column(&["Dropped"]);
            self.reader = Some(reader);
        }
        let Some(reader) = self.reader.as_mut() else {
            return;
        };
        let now = Instant::now();
        let interval_column = self.present_interval_column;
        let dropped_column = self.dropped_column;
        let intervals = &mut self.recent_intervals;
        let dropped_presents = &mut self.dropped_presents;
        let _ = read_available_csv_records(reader, |record| {
            if let Some(index) = interval_column {
                if let Some(interval) = record
                    .get(index)
                    .and_then(|value| value.trim().parse::<f64>().ok())
                    .filter(|value| *value > 0.0)
                {
                    intervals.push_back((now, interval));
                }
            }
            if let Some(index) = dropped_column {
                if record
                    .get(index)
                    .map(|value| {
                        matches!(
                            value.trim().to_ascii_lowercase().as_str(),
                            "1" | "true" | "yes"
                        )
                    })
                    .unwrap_or(false)
                {
                    *dropped_presents += 1;
                }
            }
        });
    }
}

#[cfg(windows)]
impl Drop for PresentMonCapture {
    fn drop(&mut self) {
        self.finish();
    }
}

#[cfg(windows)]
fn apply_pending_update(window: &slint::Weak<DesktopRdpWindow>, shared: &SharedState) -> bool {
    let Some(window) = window.upgrade() else {
        return false;
    };

    let Some(update) = shared.poll() else {
        return false;
    };
    window.set_state(update.state.into());
    window.set_status(update.status.into());

    if let Some(frame) = update.frame {
        let started = Instant::now();
        let image = frame_to_image(frame);
        match image {
            Some(image) => {
                window.set_frame(image);
                shared.perf.record_ui_handoff(started.elapsed());
                true
            }
            None => {
                shared.set_state("failed", "收到的 RDP framebuffer 尺寸无效。");
                false
            }
        }
    } else {
        false
    }
}

#[cfg(windows)]
fn frame_to_image(frame: RdpFrame) -> Option<Image> {
    if frame.width == 0
        || frame.height == 0
        || frame.pixels.width() != frame.width
        || frame.pixels.height() != frame.height
    {
        return None;
    }
    Some(Image::from_rgba8(frame.pixels))
}

#[cfg(windows)]
mod freerdp {
    use super::{
        coalesce_dirty_rects, copy_dirty_rects, wheel_delta_to_rdp, DirtyRect, KeyAction,
        PointerAction, PointerButton, RdpFrame, RdpInput, RdpTarget, RemoteKey, SharedState,
        BYTES_PER_PIXEL,
    };
    use slint::{Rgba8Pixel, SharedPixelBuffer};
    use std::collections::HashSet;
    use std::ffi::{c_char, CStr, CString};
    use std::mem::{size_of, zeroed};
    use std::sync::mpsc::Receiver;
    use std::sync::Arc;
    use windows_sys::Win32::Foundation::{BOOL, HANDLE, WAIT_FAILED, WAIT_TIMEOUT};
    use windows_sys::Win32::Networking::WinSock::{WSACleanup, WSAStartup, WSADATA};
    use windows_sys::Win32::System::Threading::WaitForMultipleObjects;

    #[allow(
        non_camel_case_types,
        non_snake_case,
        non_upper_case_globals,
        dead_code
    )]
    mod ffi {
        include!(concat!(env!("OUT_DIR"), "/freerdp_bindings.rs"));
    }

    #[repr(C)]
    struct AppContext {
        base: ffi::rdpContext,
        shared: *const SharedState,
        allow_untrusted_certificate: u8,
    }

    pub fn run(target: RdpTarget, shared: Arc<SharedState>, input_rx: Receiver<RdpInput>) {
        shared.set_state("connecting", "正在准备 FreeRDP 连接…");
        let result = unsafe { run_session(&target, &shared, &input_rx) };
        if shared.stop.load(std::sync::atomic::Ordering::SeqCst) {
            shared.set_state("closed", "RDP 连接已关闭。\n");
        } else if let Err(error) = result {
            let error = match shared.certificate_failure() {
                Some(certificate_failure) => format!("{certificate_failure}\n{error}"),
                None => error,
            };
            shared.set_state("failed", error);
        } else {
            shared.set_state("closed", "RDP 连接已断开。\n");
        }
        shared.perf.report_if_due(true);
    }

    unsafe fn run_session(
        target: &RdpTarget,
        shared: &Arc<SharedState>,
        input_rx: &Receiver<RdpInput>,
    ) -> Result<(), String> {
        // FreeRDP's standalone Windows clients initialize Winsock in their
        // process-level startup hook. Embedded clients must do that
        // explicitly before FreeRDP calls getaddrinfo or creates sockets.
        let _winsock = WinsockGuard::initialize()?;

        let instance = ffi::freerdp_new();
        if instance.is_null() {
            return Err("FreeRDP instance allocation failed".to_owned());
        }

        (*instance).ContextSize = size_of::<AppContext>();
        (*instance).ContextNew = Some(context_new);
        (*instance).ContextFree = Some(context_free);
        (*instance).PostConnect = Some(post_connect);
        (*instance).PostDisconnect = Some(post_disconnect);

        if ffi::freerdp_context_new(instance) == 0 {
            ffi::freerdp_free(instance);
            return Err("FreeRDP context initialization failed".to_owned());
        }

        let context = (*instance).context;
        if context.is_null() {
            ffi::freerdp_free(instance);
            return Err("FreeRDP returned a null context".to_owned());
        }
        let app_context = context.cast::<AppContext>();
        (*app_context).shared = Arc::as_ptr(shared);
        (*app_context).allow_untrusted_certificate = u8::from(target.allow_untrusted_certificate);

        // FreeRDP invokes these callbacks when its normal certificate store
        // cannot validate a certificate. Returning 2 from the callbacks means
        // accept for this connection only; returning 1 would persist trust.
        (*instance).VerifyCertificateEx = Some(verify_certificate);
        (*instance).VerifyChangedCertificateEx = Some(verify_changed_certificate);

        if let Err(error) = configure(instance, target) {
            ffi::freerdp_context_free(instance);
            ffi::freerdp_free(instance);
            return Err(error);
        }

        if ffi::freerdp_connect(instance) == 0 {
            let error = last_error(instance, "FreeRDP connection failed");
            ffi::freerdp_context_free(instance);
            ffi::freerdp_free(instance);
            return Err(error);
        }

        let event_result = event_loop(instance, shared, input_rx);
        ffi::freerdp_disconnect(instance);
        ffi::freerdp_context_free(instance);
        ffi::freerdp_free(instance);
        event_result
    }

    unsafe fn configure(instance: *mut ffi::freerdp, target: &RdpTarget) -> Result<(), String> {
        let context = (*instance).context;
        if context.is_null() || (*context).settings.is_null() {
            return Err("FreeRDP settings are unavailable".to_owned());
        }
        let settings = (*context).settings;
        let host =
            CString::new(target.host.as_str()).map_err(|_| "RDP host is invalid".to_owned())?;
        let username = CString::new(target.username.as_deref().unwrap_or_default())
            .map_err(|_| "RDP username is invalid".to_owned())?;
        let password = CString::new(target.password.as_deref().unwrap_or_default())
            .map_err(|_| "RDP password is invalid".to_owned())?;
        let domain = CString::new(target.domain.as_deref().unwrap_or_default())
            .map_err(|_| "RDP domain is invalid".to_owned())?;

        if ffi::freerdp_settings_set_string(
            settings,
            ffi::CHUZI_FREERDP_SERVER_HOSTNAME as _,
            host.as_ptr(),
        ) == 0
            || ffi::freerdp_settings_set_uint32(
                settings,
                ffi::CHUZI_FREERDP_SERVER_PORT as _,
                u32::from(target.port),
            ) == 0
            || ffi::freerdp_settings_set_string(
                settings,
                ffi::CHUZI_FREERDP_USERNAME as _,
                username.as_ptr(),
            ) == 0
            || ffi::freerdp_settings_set_string(
                settings,
                ffi::CHUZI_FREERDP_PASSWORD as _,
                password.as_ptr(),
            ) == 0
            || ffi::freerdp_settings_set_string(
                settings,
                ffi::CHUZI_FREERDP_DOMAIN as _,
                domain.as_ptr(),
            ) == 0
            || ffi::freerdp_settings_set_uint32(
                settings,
                ffi::CHUZI_FREERDP_DESKTOP_WIDTH as _,
                target.width,
            ) == 0
            || ffi::freerdp_settings_set_uint32(
                settings,
                ffi::CHUZI_FREERDP_DESKTOP_HEIGHT as _,
                target.height,
            ) == 0
            || ffi::freerdp_settings_set_uint32(
                settings,
                ffi::CHUZI_FREERDP_TCP_CONNECT_TIMEOUT as _,
                10_000,
            ) == 0
        {
            return Err("FreeRDP settings initialization failed".to_owned());
        }
        Ok(())
    }

    unsafe fn event_loop(
        instance: *mut ffi::freerdp,
        shared: &SharedState,
        input_rx: &Receiver<RdpInput>,
    ) -> Result<(), String> {
        let context = (*instance).context;
        if context.is_null() {
            return Err("FreeRDP context disappeared".to_owned());
        }

        let mut pressed_scancodes = HashSet::new();
        loop {
            drain_input(instance, input_rx, &mut pressed_scancodes);
            if shared.stop.load(std::sync::atomic::Ordering::SeqCst)
                || ffi::freerdp_shall_disconnect_context(context) != 0
            {
                return Ok(());
            }

            let mut handles: [HANDLE; 64] = zeroed();
            let count =
                ffi::freerdp_get_event_handles(context, handles.as_mut_ptr(), handles.len() as u32);
            if count == 0 || count > handles.len() as u32 {
                return Err(last_error(
                    instance,
                    "FreeRDP returned invalid event handles",
                ));
            }

            let wait_result = WaitForMultipleObjects(count, handles.as_ptr(), 0, 16);
            if wait_result == WAIT_FAILED {
                return Err("Windows event wait for the RDP connection failed".to_owned());
            }
            if wait_result != WAIT_TIMEOUT && ffi::freerdp_check_event_handles(context) == 0 {
                return Err(last_error(instance, "FreeRDP event processing failed"));
            }
            drain_input(instance, input_rx, &mut pressed_scancodes);
        }
    }

    unsafe fn drain_input(
        instance: *mut ffi::freerdp,
        input_rx: &Receiver<RdpInput>,
        pressed_scancodes: &mut HashSet<(u8, bool)>,
    ) {
        let Some(context) = instance.as_ref().map(|instance| instance.context) else {
            return;
        };
        let Some(context) = context.as_ref() else {
            return;
        };
        let Some(input) = context.input.as_mut() else {
            return;
        };
        let Some(gdi) = context.gdi.as_ref() else {
            return;
        };
        let Some(width) = u32::try_from(gdi.width).ok().filter(|width| *width > 0) else {
            return;
        };
        let Some(height) = u32::try_from(gdi.height).ok().filter(|height| *height > 0) else {
            return;
        };

        while let Ok(event) = input_rx.try_recv() {
            match event {
                RdpInput::Pointer {
                    action,
                    button,
                    x,
                    y,
                    viewport_width,
                    viewport_height,
                } => {
                    let Some((x, y)) =
                        map_pointer(x, y, viewport_width, viewport_height, width, height)
                    else {
                        continue;
                    };
                    let flags = match action {
                        PointerAction::Move => ffi::CHUZI_PTR_FLAGS_MOVE as u16,
                        PointerAction::Down => {
                            let Some(button_flag) = pointer_button_flag(button) else {
                                continue;
                            };
                            button_flag | ffi::CHUZI_PTR_FLAGS_DOWN as u16
                        }
                        PointerAction::Up => {
                            let Some(button_flag) = pointer_button_flag(button) else {
                                continue;
                            };
                            button_flag
                        }
                        PointerAction::Cancel => continue,
                    };
                    let _ = ffi::freerdp_input_send_mouse_event(input, flags, x, y);
                }
                RdpInput::Wheel {
                    delta_x,
                    delta_y,
                    x,
                    y,
                    viewport_width,
                    viewport_height,
                } => {
                    let Some((x, y)) =
                        map_pointer(x, y, viewport_width, viewport_height, width, height)
                    else {
                        continue;
                    };
                    send_wheel(input, delta_y, false, x, y);
                    send_wheel(input, delta_x, true, x, y);
                }
                RdpInput::Key { action, keys } => {
                    for key in keys {
                        send_key(input, action, key, pressed_scancodes);
                    }
                }
                RdpInput::ReleaseAllKeys => release_pressed_keys(input, pressed_scancodes),
            }
        }
    }

    unsafe fn send_wheel(input: *mut ffi::rdpInput, delta: f32, horizontal: bool, x: u16, y: u16) {
        let Some((negative, units)) = wheel_delta_to_rdp(delta) else {
            return;
        };
        let mut flags = if horizontal {
            ffi::CHUZI_PTR_FLAGS_HWHEEL as u16
        } else {
            ffi::CHUZI_PTR_FLAGS_WHEEL as u16
        } | units;
        if negative {
            flags |= ffi::CHUZI_PTR_FLAGS_WHEEL_NEGATIVE as u16;
        }
        let _ = ffi::freerdp_input_send_mouse_event(input, flags, x, y);
    }

    unsafe fn send_key(
        input: *mut ffi::rdpInput,
        action: KeyAction,
        key: RemoteKey,
        pressed_scancodes: &mut HashSet<(u8, bool)>,
    ) {
        match key {
            RemoteKey::ScanCode { code, extended } => {
                let identity = (code, extended);
                let flags = match action {
                    KeyAction::Down { repeat } if repeat => ffi::CHUZI_KBD_FLAGS_DOWN as u16,
                    KeyAction::Down { .. } if pressed_scancodes.insert(identity) => 0,
                    KeyAction::Up if pressed_scancodes.remove(&identity) => {
                        ffi::CHUZI_KBD_FLAGS_RELEASE as u16
                    }
                    _ => return,
                } | if extended {
                    ffi::CHUZI_KBD_FLAGS_EXTENDED as u16
                } else {
                    0
                };
                let _ = ffi::freerdp_input_send_keyboard_event(input, flags, code);
            }
            RemoteKey::Unicode(unit) if matches!(action, KeyAction::Down { .. }) => {
                let _ = ffi::freerdp_input_send_unicode_keyboard_event(input, 0, unit);
                let _ = ffi::freerdp_input_send_unicode_keyboard_event(
                    input,
                    ffi::CHUZI_KBD_FLAGS_RELEASE as u16,
                    unit,
                );
            }
            RemoteKey::Unicode(_) => {}
        }
    }

    unsafe fn release_pressed_keys(
        input: *mut ffi::rdpInput,
        pressed_scancodes: &mut HashSet<(u8, bool)>,
    ) {
        for (code, extended) in pressed_scancodes.drain() {
            let flags = ffi::CHUZI_KBD_FLAGS_RELEASE as u16
                | if extended {
                    ffi::CHUZI_KBD_FLAGS_EXTENDED as u16
                } else {
                    0
                };
            let _ = ffi::freerdp_input_send_keyboard_event(input, flags, code);
        }
    }

    fn pointer_button_flag(button: PointerButton) -> Option<u16> {
        match button {
            PointerButton::Left => Some(ffi::CHUZI_PTR_FLAGS_BUTTON1 as u16),
            PointerButton::Right => Some(ffi::CHUZI_PTR_FLAGS_BUTTON2 as u16),
            PointerButton::Middle => Some(ffi::CHUZI_PTR_FLAGS_BUTTON3 as u16),
            PointerButton::Other => None,
        }
    }

    fn map_pointer(
        x: f32,
        y: f32,
        viewport_width: f32,
        viewport_height: f32,
        frame_width: u32,
        frame_height: u32,
    ) -> Option<(u16, u16)> {
        if !x.is_finite()
            || !y.is_finite()
            || !viewport_width.is_finite()
            || !viewport_height.is_finite()
            || viewport_width <= 0.0
            || viewport_height <= 0.0
            || frame_width == 0
            || frame_height == 0
        {
            return None;
        }
        let frame_width = frame_width as f32;
        let frame_height = frame_height as f32;
        let scale = (viewport_width / frame_width).min(viewport_height / frame_height);
        if !scale.is_finite() || scale <= 0.0 {
            return None;
        }
        let rendered_width = frame_width * scale;
        let rendered_height = frame_height * scale;
        let offset_x = (viewport_width - rendered_width) / 2.0;
        let offset_y = (viewport_height - rendered_height) / 2.0;
        if x < offset_x
            || y < offset_y
            || x >= offset_x + rendered_width
            || y >= offset_y + rendered_height
        {
            return None;
        }
        let mapped_x = ((x - offset_x) / scale)
            .floor()
            .clamp(0.0, frame_width - 1.0);
        let mapped_y = ((y - offset_y) / scale)
            .floor()
            .clamp(0.0, frame_height - 1.0);
        Some((mapped_x as u16, mapped_y as u16))
    }

    unsafe extern "C" fn context_new(
        _instance: *mut ffi::freerdp,
        _context: *mut ffi::rdpContext,
    ) -> BOOL {
        1
    }

    unsafe extern "C" fn context_free(
        _instance: *mut ffi::freerdp,
        _context: *mut ffi::rdpContext,
    ) {
    }

    unsafe extern "C" fn verify_certificate(
        instance: *mut ffi::freerdp,
        host: *const c_char,
        port: u16,
        _common_name: *const c_char,
        _subject: *const c_char,
        _issuer: *const c_char,
        fingerprint: *const c_char,
        _flags: u32,
    ) -> u32 {
        handle_certificate_verification(instance, host, port, fingerprint, false)
    }

    unsafe extern "C" fn verify_changed_certificate(
        instance: *mut ffi::freerdp,
        host: *const c_char,
        port: u16,
        _common_name: *const c_char,
        _subject: *const c_char,
        _issuer: *const c_char,
        fingerprint: *const c_char,
        _old_subject: *const c_char,
        _old_issuer: *const c_char,
        _old_fingerprint: *const c_char,
        _flags: u32,
    ) -> u32 {
        handle_certificate_verification(instance, host, port, fingerprint, true)
    }

    unsafe fn handle_certificate_verification(
        instance: *mut ffi::freerdp,
        host: *const c_char,
        port: u16,
        fingerprint: *const c_char,
        changed: bool,
    ) -> u32 {
        let Some(app_context) = app_context_from_instance(instance) else {
            return 0;
        };
        let app_context = &*app_context;
        let Some(shared) = app_context.shared.as_ref() else {
            return 0;
        };

        let host = c_string_or_unknown(host);
        if app_context.allow_untrusted_certificate != 0 {
            let certificate_kind = if changed {
                "已变化或名称不匹配的"
            } else {
                "未受信任的"
            };
            shared.set_state(
                "connecting",
                format!("正在接受{certificate_kind} RDP 证书（仅本次连接）：{host}:{port}…"),
            );
            return 2;
        }

        let certificate_kind = if changed {
            "服务器证书已变化或名称不匹配"
        } else {
            "服务器证书未受信任或名称不匹配"
        };
        let mut message = format!("RDP {certificate_kind}，已拒绝连接（目标：{host}:{port}）。");
        if let Some(fingerprint) = certificate_value(fingerprint) {
            message.push_str(&format!("\n证书指纹：{fingerprint}"));
        }
        message.push_str("\n请先核对指纹；确认目标可信后，勾选“仅本次接受不受信任证书”再重试。");
        shared.set_certificate_failure(message);
        0
    }

    unsafe fn app_context_from_instance(instance: *mut ffi::freerdp) -> Option<*const AppContext> {
        let context = instance.as_ref()?.context;
        (!context.is_null()).then(|| context.cast::<AppContext>().cast_const())
    }

    unsafe fn c_string_or_unknown(value: *const c_char) -> String {
        if value.is_null() {
            return "<unknown>".to_owned();
        }
        CStr::from_ptr(value).to_string_lossy().into_owned()
    }

    unsafe fn certificate_value(value: *const c_char) -> Option<String> {
        if value.is_null() {
            return None;
        }
        let value = CStr::from_ptr(value).to_string_lossy();
        let value = value.trim();
        if value.is_empty() || value.contains("BEGIN CERTIFICATE") {
            return None;
        }
        let mut value = value.chars().take(256).collect::<String>();
        if value.chars().count() == 256 {
            value.push('…');
        }
        Some(value)
    }

    struct WinsockGuard;

    impl WinsockGuard {
        unsafe fn initialize() -> Result<Self, String> {
            let mut data: WSADATA = zeroed();
            let result = WSAStartup(0x0202, &mut data);
            if result != 0 {
                return Err(format!("Winsock initialization failed ({result})"));
            }
            Ok(Self)
        }
    }

    impl Drop for WinsockGuard {
        fn drop(&mut self) {
            unsafe {
                let _ = WSACleanup();
            }
        }
    }

    unsafe extern "C" fn post_connect(instance: *mut ffi::freerdp) -> BOOL {
        if instance.is_null() || (*instance).context.is_null() {
            return 0;
        }
        let context = (*instance).context;
        if ffi::gdi_init(instance, ffi::CHUZI_PIXEL_FORMAT_RGBA32 as u32) == 0 {
            return 0;
        }
        if ffi::freerdp_settings_set_bool(
            (*context).settings,
            ffi::CHUZI_FREERDP_DEACTIVATE_CLIENT_DECODING as _,
            0,
        ) == 0
        {
            return 0;
        }
        let Some(update) = (*context).update.as_mut() else {
            return 0;
        };
        update.BeginPaint = Some(begin_paint);
        update.EndPaint = Some(end_paint);
        update.DesktopResize = Some(desktop_resize);
        if let Some(shared) = shared_from_context(&*context) {
            shared.set_state("connected", "RDP 已连接，正在接收远程桌面画面…");
        }
        1
    }

    unsafe extern "C" fn post_disconnect(instance: *mut ffi::freerdp) {
        if !instance.is_null() {
            ffi::gdi_free(instance);
        }
    }

    unsafe extern "C" fn begin_paint(context: *mut ffi::rdpContext) -> BOOL {
        if let Some(gdi) = context.as_ref().and_then(|context| context.gdi.as_ref()) {
            // Start a fresh invalidation batch. FreeRDP accumulates dirty
            // rectangles between BeginPaint and EndPaint.
            reset_dirty_region(gdi);
        }
        1
    }

    unsafe extern "C" fn end_paint(context: *mut ffi::rdpContext) -> BOOL {
        let Some(context) = context.as_ref() else {
            return 0;
        };
        let Some(gdi) = context.gdi.as_ref() else {
            return 0;
        };
        let dirty_rects = dirty_rects_from_gdi(gdi);
        if dirty_rects.is_empty() {
            reset_dirty_region(gdi);
            return 1;
        }
        if gdi.width <= 0 || gdi.height <= 0 || gdi.stride == 0 || gdi.primary_buffer.is_null() {
            reset_dirty_region(gdi);
            return 0;
        }

        let Ok(width) = usize::try_from(gdi.width) else {
            reset_dirty_region(gdi);
            return 0;
        };
        let Ok(height) = usize::try_from(gdi.height) else {
            reset_dirty_region(gdi);
            return 0;
        };
        let Ok(stride) = usize::try_from(gdi.stride) else {
            reset_dirty_region(gdi);
            return 0;
        };
        let Some(row_bytes) = width.checked_mul(BYTES_PER_PIXEL) else {
            reset_dirty_region(gdi);
            return 0;
        };
        if stride < row_bytes {
            reset_dirty_region(gdi);
            return 0;
        }
        let Some(bytes) = stride.checked_mul(height) else {
            reset_dirty_region(gdi);
            return 0;
        };
        let Some(full_frame_bytes) = row_bytes.checked_mul(height) else {
            reset_dirty_region(gdi);
            return 0;
        };
        let source = std::slice::from_raw_parts(gdi.primary_buffer, bytes);
        if let Some(shared) = shared_from_context(context) {
            match shared.mode() {
                super::DirtyFrameMode::Optimized => {
                    if shared
                        .update_framebuffer(
                            width as u32,
                            height as u32,
                            source,
                            stride,
                            &dirty_rects,
                        )
                        .is_err()
                    {
                        shared.set_state("failed", "RDP framebuffer 更新失败。");
                        reset_dirty_region(gdi);
                        return 0;
                    }
                }
                super::DirtyFrameMode::Legacy => {
                    let mut pixels =
                        SharedPixelBuffer::<Rgba8Pixel>::new(width as u32, height as u32);
                    let copy_started = std::time::Instant::now();
                    let full_rect = DirtyRect {
                        x: 0,
                        y: 0,
                        width: width as i32,
                        height: height as i32,
                    };
                    if copy_dirty_rects(
                        pixels.make_mut_bytes(),
                        source,
                        stride,
                        row_bytes,
                        &[full_rect],
                    )
                    .is_err()
                    {
                        shared.set_state("failed", "RDP framebuffer 更新失败。");
                        reset_dirty_region(gdi);
                        return 0;
                    }
                    let union_rects = coalesce_dirty_rects(&dirty_rects, width, height);
                    shared
                        .perf
                        .record_rdp_update(width, height, &dirty_rects, &union_rects);
                    shared.perf.record_copy(
                        full_frame_bytes,
                        full_frame_bytes,
                        copy_started.elapsed(),
                        false,
                    );
                    shared.set_frame(RdpFrame {
                        width: width as u32,
                        height: height as u32,
                        pixels,
                    });
                }
            }
        }
        reset_dirty_region(gdi);
        1
    }

    unsafe fn dirty_rects_from_gdi(gdi: &ffi::rdpGdi) -> Vec<DirtyRect> {
        let Some(hwnd) = gdi
            .primary
            .as_ref()
            .and_then(|primary| primary.hdc.as_ref())
            .and_then(|hdc| hdc.hwnd.as_ref())
        else {
            return Vec::new();
        };
        let Some(invalid) = hwnd.invalid.as_ref() else {
            return Vec::new();
        };
        if invalid.null != 0 {
            return Vec::new();
        }

        let count = usize::try_from(hwnd.ninvalid).unwrap_or(0);
        let capacity = usize::try_from(hwnd.count).unwrap_or(0);
        if count > 0 && count <= capacity && !hwnd.cinvalid.is_null() {
            let rectangles = std::slice::from_raw_parts(hwnd.cinvalid, count);
            let rectangles: Vec<_> = rectangles
                .iter()
                .filter(|rect| rect.null == 0)
                .map(|rect| DirtyRect {
                    x: rect.x,
                    y: rect.y,
                    width: rect.w,
                    height: rect.h,
                })
                .collect();
            if !rectangles.is_empty() {
                return rectangles;
            }
        }

        vec![DirtyRect {
            x: invalid.x,
            y: invalid.y,
            width: invalid.w,
            height: invalid.h,
        }]
    }

    unsafe fn reset_dirty_region(gdi: &ffi::rdpGdi) {
        let Some(primary) = gdi.primary.as_ref() else {
            return;
        };
        let Some(hdc) = primary.hdc.as_ref() else {
            return;
        };
        let Some(hwnd) = hdc.hwnd.as_mut() else {
            return;
        };
        if let Some(invalid) = hwnd.invalid.as_mut() {
            invalid.null = 1;
        }
        hwnd.ninvalid = 0;
    }

    unsafe extern "C" fn desktop_resize(context: *mut ffi::rdpContext) -> BOOL {
        let Some(context) = context.as_ref() else {
            return 0;
        };
        let gdi = context.gdi;
        if gdi.is_null() {
            return 0;
        }
        let settings = context.settings;
        if settings.is_null() {
            return 0;
        }
        let width =
            ffi::freerdp_settings_get_uint32(settings, ffi::CHUZI_FREERDP_DESKTOP_WIDTH as _);
        let height =
            ffi::freerdp_settings_get_uint32(settings, ffi::CHUZI_FREERDP_DESKTOP_HEIGHT as _);
        ffi::gdi_resize(gdi, width, height)
    }

    unsafe fn shared_from_context(context: &ffi::rdpContext) -> Option<&SharedState> {
        let app_context = (context as *const ffi::rdpContext).cast::<AppContext>();
        app_context.as_ref()?.shared.as_ref()
    }

    unsafe fn last_error(instance: *mut ffi::freerdp, prefix: &str) -> String {
        let Some(context) = instance
            .as_ref()
            .and_then(|instance| instance.context.as_ref())
        else {
            return prefix.to_owned();
        };
        let context = context as *const ffi::rdpContext as *mut ffi::rdpContext;
        let code = ffi::freerdp_get_last_error(context);
        let name = ffi::freerdp_get_last_error_name(code);
        if name.is_null() {
            return format!("{prefix} (error code {code})");
        }
        let name = CStr::from_ptr(name).to_string_lossy();
        format!("{prefix} ({name})")
    }
}

#[cfg(windows)]
fn apply_windows_window_chrome(window: &DesktopRdpWindow) {
    use raw_window_handle::{HasWindowHandle, RawWindowHandle};
    use windows_sys::Win32::Graphics::Dwm::{
        DwmSetWindowAttribute, DWMWA_BORDER_COLOR, DWMWA_USE_IMMERSIVE_DARK_MODE,
        DWMWA_WINDOW_CORNER_PREFERENCE, DWMWCP_ROUND,
    };

    let handle = window.window().window_handle();
    let Ok(handle) = handle.window_handle() else {
        return;
    };
    let RawWindowHandle::Win32(win32) = handle.as_raw() else {
        return;
    };

    let hwnd = win32.hwnd.get() as *mut c_void;
    let corner_preference: u32 = DWMWCP_ROUND as u32;
    let dark_mode: u32 = 1;
    let border_color: u32 = 0x00141414;

    unsafe {
        let _ = DwmSetWindowAttribute(
            hwnd,
            DWMWA_WINDOW_CORNER_PREFERENCE as u32,
            (&corner_preference as *const u32).cast::<c_void>(),
            size_of_val(&corner_preference) as u32,
        );
        let _ = DwmSetWindowAttribute(
            hwnd,
            DWMWA_USE_IMMERSIVE_DARK_MODE as u32,
            (&dark_mode as *const u32).cast::<c_void>(),
            size_of_val(&dark_mode) as u32,
        );
        let _ = DwmSetWindowAttribute(
            hwnd,
            DWMWA_BORDER_COLOR as u32,
            (&border_color as *const u32).cast::<c_void>(),
            size_of_val(&border_color) as u32,
        );
    }
}

#[cfg(windows)]
use std::ffi::c_void;
#[cfg(windows)]
use std::mem::size_of_val;

fn wipe_string(value: &mut String) {
    for index in 0..value.len() {
        unsafe {
            std::ptr::write_volatile(value.as_mut_ptr().add(index), 0);
        }
    }
    value.clear();
}

#[cfg(test)]
mod tests {
    use super::{
        coalesce_dirty_rects, read_available_csv_records, special_key_virtual_code,
        take_pressed_key, wheel_delta_to_rdp, DirtyFrameMode, DirtyRect, Framebuffers, RdpFrame,
        RdpPerfStats, RemoteKey, SharedState,
    };
    use std::collections::HashMap;
    use std::fs::{File, OpenOptions};
    use std::io::Write;

    #[cfg(windows)]
    use slint::{Rgba8Pixel, SharedPixelBuffer};

    fn test_frame(pixels: &[u8]) -> RdpFrame {
        #[cfg(windows)]
        let pixels = SharedPixelBuffer::<Rgba8Pixel>::clone_from_slice(pixels, 1, 1);
        #[cfg(not(windows))]
        let pixels = pixels.to_vec();
        RdpFrame {
            width: 1,
            height: 1,
            pixels,
        }
    }

    fn frame_bytes(frame: &RdpFrame) -> Vec<u8> {
        #[cfg(windows)]
        {
            frame.pixels.as_bytes().to_vec()
        }
        #[cfg(not(windows))]
        {
            frame.pixels.clone()
        }
    }

    fn source(width: usize, height: usize, seed: u8) -> Vec<u8> {
        (0..width * height * super::BYTES_PER_PIXEL)
            .map(|index| seed.wrapping_add(index as u8))
            .collect()
    }

    #[test]
    fn wheel_delta_preserves_direction_and_bounds_rdp_rotation() {
        assert_eq!(wheel_delta_to_rdp(60.0), Some((false, 120)));
        assert_eq!(wheel_delta_to_rdp(-30.0), Some((true, 60)));
        assert_eq!(wheel_delta_to_rdp(0.1), Some((false, 1)));
        assert_eq!(wheel_delta_to_rdp(-10_000.0), Some((true, 255)));
        assert_eq!(wheel_delta_to_rdp(0.0), None);
        assert_eq!(wheel_delta_to_rdp(f32::NAN), None);
    }

    #[test]
    fn recorded_performance_report_is_flushed_as_redacted_json() {
        let path = std::env::temp_dir().join(format!(
            "chuzi-rdp-perf-test-{}-{}.log",
            std::process::id(),
            super::performance_session_id()
        ));
        let stats = RdpPerfStats::configured(
            DirtyFrameMode::Optimized,
            true,
            Some(File::create(&path).expect("test performance log should open")),
            false,
        );
        stats.report_if_due(true);
        drop(stats);

        let output = std::fs::read_to_string(&path).expect("performance record should flush");
        let _ = std::fs::remove_file(path);
        let json = output
            .strip_prefix("rdp_perf ")
            .expect("record should retain analyzer prefix")
            .trim();
        let record: serde_json::Value =
            serde_json::from_str(json).expect("performance record should be valid JSON");
        assert_eq!(record["dirty_frame_mode"], "optimized");
        assert!(record.get("actual_copy_bytes").is_some());
        assert!(record.get("host").is_none());
        assert!(record.get("password").is_none());
    }

    #[test]
    fn performance_csv_reader_follows_appended_rows_after_eof() {
        let path = std::env::temp_dir().join(format!(
            "chuzi-rdp-perf-csv-test-{}-{}.csv",
            std::process::id(),
            super::performance_session_id()
        ));
        std::fs::write(&path, "value\nfirst\n").expect("test CSV should be created");
        let file = File::open(&path).expect("test CSV should open");
        let mut reader = csv::Reader::from_reader(file);
        let _ = reader.headers().expect("test CSV header should parse");
        let mut values = Vec::new();
        read_available_csv_records(&mut reader, |record| {
            values.push(record.get(0).unwrap_or_default().to_owned());
        })
        .expect("initial CSV rows should parse");

        OpenOptions::new()
            .append(true)
            .open(&path)
            .expect("test CSV should reopen for append")
            .write_all(b"second\n")
            .expect("test CSV row should append");
        read_available_csv_records(&mut reader, |record| {
            values.push(record.get(0).unwrap_or_default().to_owned());
        })
        .expect("appended CSV rows should parse");
        drop(reader);
        let _ = std::fs::remove_file(path);

        assert_eq!(values, ["first", "second"]);
    }

    #[test]
    fn slint_special_keys_map_to_windows_virtual_keys() {
        assert_eq!(special_key_virtual_code('\u{0008}'), Some(0x08));
        assert_eq!(special_key_virtual_code('\u{0016}'), Some(0xa3));
        assert_eq!(special_key_virtual_code('\u{f700}'), Some(0x26));
        assert_eq!(special_key_virtual_code('\u{f71b}'), Some(0x87));
        assert_eq!(special_key_virtual_code('\u{f72c}'), Some(0x21));
        assert_eq!(special_key_virtual_code('a'), None);
    }

    #[test]
    fn pressed_key_release_matches_same_physical_key_after_modifier_changes() {
        let scan_code = RemoteKey::ScanCode {
            code: 0x02,
            extended: false,
        };
        let mut pressed = HashMap::from([("!".to_owned(), vec![scan_code])]);

        assert_eq!(
            take_pressed_key(&mut pressed, "1", &[scan_code]),
            Some(vec![scan_code])
        );
        assert!(pressed.is_empty());
    }

    #[test]
    fn shared_state_replaces_stale_frame_with_latest_frame() {
        let shared = SharedState::new();
        shared.set_frame(test_frame(&[1, 2, 3, 4]));
        shared.set_frame(test_frame(&[5, 6, 7, 8]));

        let update = shared.poll().expect("latest update should be available");
        let frame = update.frame.expect("latest frame should be available");
        assert_eq!(frame_bytes(&frame), vec![5, 6, 7, 8]);
        assert!(shared.poll().is_none());
    }

    #[test]
    fn dirty_rectangles_are_clipped_and_union_is_non_overlapping() {
        let rects = coalesce_dirty_rects(
            &[
                DirtyRect {
                    x: 0,
                    y: 0,
                    width: 3,
                    height: 3,
                },
                DirtyRect {
                    x: 2,
                    y: 1,
                    width: 4,
                    height: 3,
                },
                DirtyRect {
                    x: -2,
                    y: -2,
                    width: 3,
                    height: 3,
                },
            ],
            8,
            8,
        );
        assert_eq!(rects.iter().map(|rect| rect.area_pixels()).sum::<u64>(), 19);
        assert!(rects.iter().all(|rect| rect.x >= 0 && rect.y >= 0));
        assert!(coalesce_dirty_rects(
            &[DirtyRect {
                x: 20,
                y: 20,
                width: 2,
                height: 2,
            }],
            8,
            8,
        )
        .is_empty());
    }

    #[test]
    fn partial_copy_is_bounded_and_preserves_previous_pixels() {
        let mut framebuffers = Framebuffers::default();
        let initial = source(4, 4, 0);
        let full = [DirtyRect {
            x: 0,
            y: 0,
            width: 4,
            height: 4,
        }];
        framebuffers
            .update(4, 4, &initial, 16, &full)
            .expect("initial framebuffer should copy");
        let _ = framebuffers.snapshot_latest();

        let changed = source(4, 4, 100);
        let partial = [DirtyRect {
            x: 1,
            y: 2,
            width: 1,
            height: 1,
        }];
        let result = framebuffers
            .update(4, 4, &changed, 16, &partial)
            .expect("partial framebuffer should copy");
        assert_eq!(result.actual_copy_bytes, 4);
        let snapshot = framebuffers
            .snapshot_latest()
            .expect("new generation should be available");
        let bytes = frame_bytes(&snapshot);
        assert_eq!(&bytes[0..4], &initial[0..4]);
        let changed_offset = (2 * 4 + 1) * 4;
        assert_eq!(
            &bytes[changed_offset..changed_offset + 4],
            &changed[changed_offset..changed_offset + 4]
        );
    }

    #[test]
    fn optimized_state_only_exposes_latest_owned_snapshot() {
        let shared = SharedState::with_mode(DirtyFrameMode::Optimized);
        let full = [DirtyRect {
            x: 0,
            y: 0,
            width: 2,
            height: 2,
        }];
        let first_source = source(2, 2, 1);
        shared
            .update_framebuffer(2, 2, &first_source, 8, &full)
            .expect("first update should be accepted");
        let first = shared
            .poll()
            .and_then(|update| update.frame)
            .expect("first snapshot should be available");
        let second_source = source(2, 2, 90);
        shared
            .update_framebuffer(2, 2, &second_source, 8, &full)
            .expect("second update should be accepted");
        shared
            .update_framebuffer(2, 2, &source(2, 2, 120), 8, &full)
            .expect("third update should be accepted");
        let latest = shared
            .poll()
            .and_then(|update| update.frame)
            .expect("latest snapshot should be available");
        assert_eq!(frame_bytes(&first), first_source);
        assert_eq!(frame_bytes(&latest), source(2, 2, 120));
        assert!(shared.poll().is_none());
    }

    #[test]
    fn shared_state_only_marks_changed_state_as_pending() {
        let shared = SharedState::new();
        assert!(!shared.has_pending());
        assert!(shared.poll().is_none());

        shared.set_state("connecting", "正在连接 RDP…");
        assert!(!shared.has_pending());

        shared.set_state("connected", "RDP 已连接");
        assert!(shared.has_pending());
        let update = shared.poll().expect("changed state should be available");
        assert_eq!(update.state, "connected");
        assert_eq!(update.status, "RDP 已连接");
        assert!(!shared.has_pending());
    }
}
