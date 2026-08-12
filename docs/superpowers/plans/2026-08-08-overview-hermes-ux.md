# Overview + Hermes UX Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix empty Overview TOP charts + densify layout; fix Hermes height and auto-collapse tool/reasoning logs.

**Architecture:** Shared `hostLatestMetrics` helper for Overview (+ Hosts list); CSS bar TOP panels; Hermes flex height + details collapse.

**Tech Stack:** Vue 3, Element Plus, existing alertsApi / hostsApi.

---

### Task 1: host metrics helper + Overview TOP densification

**Files:**
- Create: `frontend/src/shared/host-metrics.ts`
- Modify: `frontend/src/views/OverviewView.vue`
- Modify: i18n keys for new TOP labels if missing

### Task 2: Overview alerts ack/silence

**Files:**
- Modify: `frontend/src/views/OverviewView.vue`

### Task 3: HostsView metric field fix (P2)

**Files:**
- Modify: `frontend/src/views/HostsView.vue` (use shared helper)

### Task 4: Hermes collapse + height + density

**Files:**
- Modify: `frontend/src/features/hermes/HermesChat.vue`
- Modify: `frontend/src/views/HermesView.vue`
- Optionally: `frontend/src/components/AiChatWidgets.vue`

### Task 5: Verify

- `npm run check`
- Smoke briefingConsumerMatch still ok
