//! Быстрый проход перебора на GPU (`scout.wgsl`) через wgpu: Metal на macOS.
//!
//! GPU не умеет f64, поэтому он только отбирает кандидатов: пары × TP/SL, у которых верхняя
//! оценка weighted pr/dd с запасом на погрешность f32 больше порога. Точный расчёт кандидатов
//! делает CPU (`evaluate_trades`), так что результат тот же, что у CPU-перебора.

use std::borrow::Cow;

use anyhow::{bail, Context, Result};
use bytemuck::{Pod, Zeroable};
use wgpu::util::DeviceExt;

use super::Grid;

/// Сколько пар обрабатывается за один запуск: ~0.1–0.5 с работы GPU.
pub const PAIRS_PER_BATCH: usize = 8192;

/// Сколько кандидатов помещается в буфер за запуск; при переполнении партию считает CPU.
const CAPACITY: u32 = 1 << 20;

const WORKGROUP: u32 = 64;

#[repr(C)]
#[derive(Clone, Copy, Pod, Zeroable)]
struct GpuHot {
    ln_coef: f32,
    drawdown: f32,
    close: u32,
}

#[repr(C)]
#[derive(Clone, Copy, Pod, Zeroable)]
struct Params {
    pairs: u32,
    tp_sl: u32,
    len: u32,
    words: u32,
    min_tp: u32,
    capacity: u32,
    threshold: f32,
    pad: u32,
}

pub struct GpuScout {
    device: wgpu::Device,
    queue: wgpu::Queue,
    pipeline: wgpu::ComputePipeline,
    bind_group: wgpu::BindGroup,
    params: wgpu::Buffer,
    bits: wgpu::Buffer,
    counter: wgpu::Buffer,
    found: wgpu::Buffer,
    counter_read: wgpu::Buffer,
    found_read: wgpu::Buffer,
    tp_sl: u32,
    len: u32,
    /// u32-слов маски на пару.
    words: u32,
    pub adapter: String,
}

impl GpuScout {
    /// `words64` — длина маски сигналов пары в u64 (см. `signal_words`).
    pub fn new(grid: &Grid, len: usize, words64: usize) -> Result<Self> {
        let tp_sl = grid.tp_sl.len();
        if tp_sl.div_ceil(WORKGROUP as usize) > u16::MAX as usize {
            bail!("too many TP/SL combinations for one GPU dispatch: {tp_sl}");
        }

        let instance = wgpu::Instance::new(&wgpu::InstanceDescriptor::default());
        let adapter = pollster::block_on(instance.request_adapter(&wgpu::RequestAdapterOptions {
            power_preference: wgpu::PowerPreference::HighPerformance,
            ..Default::default()
        }))
        .context("no GPU adapter")?;
        let info = adapter.get_info();

        let hot: Vec<GpuHot> = grid
            .tp_sl
            .iter()
            .flat_map(|(_, o)| o.hot.iter())
            .map(|h| GpuHot {
                ln_coef: h.ln_coef as f32,
                drawdown: h.drawdown_floor,
                close: h.close,
            })
            .collect();
        let hot_bytes = (hot.len() * std::mem::size_of::<GpuHot>()) as u64;

        let mut limits = wgpu::Limits::default();
        let available = adapter.limits();
        limits.max_storage_buffer_binding_size = available.max_storage_buffer_binding_size;
        limits.max_buffer_size = available.max_buffer_size;
        if hot_bytes > limits.max_storage_buffer_binding_size as u64 {
            bail!(
                "TP/SL outcomes take {hot_bytes} bytes, GPU binding limit is {}",
                limits.max_storage_buffer_binding_size
            );
        }

        let (device, queue) = pollster::block_on(adapter.request_device(&wgpu::DeviceDescriptor {
            label: Some("scout"),
            required_features: wgpu::Features::empty(),
            required_limits: limits,
            memory_hints: wgpu::MemoryHints::Performance,
            trace: wgpu::Trace::Off,
        }))
        .context("failed to open GPU device")?;

        let module = device.create_shader_module(wgpu::ShaderModuleDescriptor {
            label: Some("scout.wgsl"),
            source: wgpu::ShaderSource::Wgsl(Cow::Borrowed(include_str!("scout.wgsl"))),
        });
        let pipeline = device.create_compute_pipeline(&wgpu::ComputePipelineDescriptor {
            label: Some("scout"),
            layout: None,
            module: &module,
            entry_point: Some("main"),
            compilation_options: Default::default(),
            cache: None,
        });

        let words = (words64 * 2) as u32;
        let storage = |label, size: u64, usage| {
            device.create_buffer(&wgpu::BufferDescriptor {
                label: Some(label),
                size,
                usage,
                mapped_at_creation: false,
            })
        };
        let rw = wgpu::BufferUsages::STORAGE
            | wgpu::BufferUsages::COPY_SRC
            | wgpu::BufferUsages::COPY_DST;
        let read = wgpu::BufferUsages::MAP_READ | wgpu::BufferUsages::COPY_DST;

        let params = storage(
            "params",
            std::mem::size_of::<Params>() as u64,
            wgpu::BufferUsages::UNIFORM | wgpu::BufferUsages::COPY_DST,
        );
        let bits = storage(
            "bits",
            (PAIRS_PER_BATCH * words as usize * 4) as u64,
            wgpu::BufferUsages::STORAGE | wgpu::BufferUsages::COPY_DST,
        );
        let hot = device.create_buffer_init(&wgpu::util::BufferInitDescriptor {
            label: Some("hot"),
            contents: bytemuck::cast_slice(&hot),
            usage: wgpu::BufferUsages::STORAGE,
        });
        let weights: Vec<f32> = grid.weights.iter().map(|&w| w as f32).collect();
        let weights = device.create_buffer_init(&wgpu::util::BufferInitDescriptor {
            label: Some("weights"),
            contents: bytemuck::cast_slice(&weights),
            usage: wgpu::BufferUsages::STORAGE,
        });
        let counter = storage("counter", 4, rw);
        let found = storage("found", CAPACITY as u64 * 8, rw);
        let counter_read = storage("counter_read", 4, read);
        let found_read = storage("found_read", CAPACITY as u64 * 8, read);

        let bind_group = device.create_bind_group(&wgpu::BindGroupDescriptor {
            label: Some("scout"),
            layout: &pipeline.get_bind_group_layout(0),
            entries: &[
                wgpu::BindGroupEntry {
                    binding: 0,
                    resource: params.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 1,
                    resource: bits.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 2,
                    resource: hot.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 3,
                    resource: weights.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 4,
                    resource: counter.as_entire_binding(),
                },
                wgpu::BindGroupEntry {
                    binding: 5,
                    resource: found.as_entire_binding(),
                },
            ],
        });

        Ok(Self {
            device,
            queue,
            pipeline,
            bind_group,
            params,
            bits,
            counter,
            found,
            counter_read,
            found_read,
            tp_sl: tp_sl as u32,
            len: len as u32,
            words,
            adapter: format!("{} ({:?})", info.name, info.backend),
        })
    }

    /// Кандидаты `(пара в партии, индекс TP/SL)` для партии масок `bits` (u64-слова подряд,
    /// `signal_words` на пару). `None` — кандидатов больше, чем помещается в буфер.
    pub fn candidates(
        &self,
        bits: &[u64],
        pairs: usize,
        threshold: f64,
        min_take_profits: usize,
    ) -> Result<Option<Vec<(u32, u32)>>> {
        assert!(pairs <= PAIRS_PER_BATCH);
        assert_eq!(bits.len() * 2, pairs * self.words as usize);

        // На little-endian u64 — это два u32: младшие 32 свечи, потом старшие.
        self.queue
            .write_buffer(&self.bits, 0, bytemuck::cast_slice(bits));
        self.queue
            .write_buffer(&self.counter, 0, bytemuck::bytes_of(&0u32));
        let params = Params {
            pairs: pairs as u32,
            tp_sl: self.tp_sl,
            len: self.len,
            words: self.words,
            min_tp: min_take_profits as u32,
            capacity: CAPACITY,
            threshold: threshold as f32,
            pad: 0,
        };
        self.queue
            .write_buffer(&self.params, 0, bytemuck::bytes_of(&params));

        let mut encoder = self.device.create_command_encoder(&Default::default());
        {
            let mut pass = encoder.begin_compute_pass(&Default::default());
            pass.set_pipeline(&self.pipeline);
            pass.set_bind_group(0, &self.bind_group, &[]);
            // x — TP/SL (группа из WORKGROUP соседних), y — пара партии.
            pass.dispatch_workgroups(self.tp_sl.div_ceil(WORKGROUP), pairs as u32, 1);
        }
        encoder.copy_buffer_to_buffer(&self.counter, 0, &self.counter_read, 0, 4);
        self.queue.submit([encoder.finish()]);

        let count = self.read(&self.counter_read, 4, |b| {
            bytemuck::pod_read_unaligned::<u32>(b)
        })?;
        if count > CAPACITY {
            return Ok(None);
        }
        if count == 0 {
            return Ok(Some(Vec::new()));
        }

        let size = count as u64 * 8;
        let mut encoder = self.device.create_command_encoder(&Default::default());
        encoder.copy_buffer_to_buffer(&self.found, 0, &self.found_read, 0, size);
        self.queue.submit([encoder.finish()]);
        let found = self.read(&self.found_read, size, |b| {
            bytemuck::cast_slice::<u8, [u32; 2]>(b)
                .iter()
                .map(|&[pair, t]| (pair, t))
                .collect()
        })?;
        Ok(Some(found))
    }

    fn read<T>(&self, buffer: &wgpu::Buffer, size: u64, f: impl FnOnce(&[u8]) -> T) -> Result<T> {
        let slice = buffer.slice(..size);
        slice.map_async(wgpu::MapMode::Read, |_| {});
        self.device
            .poll(wgpu::PollType::Wait)
            .context("GPU poll failed")?;
        let value = f(&slice.get_mapped_range());
        buffer.unmap();
        Ok(value)
    }
}
