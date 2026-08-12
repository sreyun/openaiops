# Overview + Hermes UX densification — Design

Date: 2026-08-08  
Status: approved (approach 2)  
Scope: Vue homepage parity+surpass classic; Hermes height + collapse + density

## Goals

1. Homepage TOP rankings show real data (classic `latest.*`), no empty 180px shells, denser than classic.
2. Hermes page/dock fills main pane correctly; reasoning + tool logs collapse after stream (classic parity); chat denser than classic.

## Overview

- Metric helper: read `host.latest` (fallback flat) — cpu_percent, mem_percent, max disks%, disk_io_util, read+write IOPS, sent+recv net, load5, proc_count, max GPU util.
- TOP10 CSS bar panels in 3-col grid; hide empty (GPU only if any gpus); add disk IO% + process; click → host detail.
- Alerts mini-list: ack/silence when operator.
- Keep Activity (Vue-plus); tighten health strip.

## Hermes

- Height: flex chain from page (`height:100%` / `flex:1; min-height:0`), drop `100dvh−116px` and mobile `max:500px`.
- Reasoning: `<details :open="streaming">` — open while streaming, closed after.
- Tools: compact chips; raw log in `<details>` default closed after done.
- Bubble max-width ≥ min(1100px, 100%); tighten padding; charts full width of bubble.

## Non-goals

- Classic canvas rewrite; new overview APIs; Hermes backend protocol change.
