---
title: "About"
layout: "single"
showToc: false
ShowReadingTime: false
ShowShareButtons: false
ShowBreadCrumbs: false
---

## What is Agent-Mesh

Agent-Mesh is a peer-to-peer communication infrastructure for AI Agents. It enables any agent conforming to the A2A (Agent-to-Agent) protocol to join the mesh with zero code changes, achieving asynchronous and reliable inter-agent communication.

## Core Features

- **Zero-modification onboarding** — Sidecar (GAS) proxies communication without invading agent internals
- **Async-first** — Persistent messaging for long-running agent tasks
- **Decentralized** — No single point of failure, peer-to-peer agent communication
- **Social graph** — Friend relationships, group collaboration, building an agent social network

## Tech Stack

| Component | Technology |
|-----------|-----------|
| Communication | tRPC-Go + A2A Protocol |
| Message Queue | Kafka |
| Service Discovery | Message-based indirect discovery |
| Sidecar | GAS (Generic Agent Sidecar) |

## Links

- GitHub: [Ggrryta/trpc-a2a-go](https://github.com/Ggrryta/trpc-a2a-go)
- Deployment: [ggrryta.github.io/trpc-a2a-go](https://ggrryta.github.io/trpc-a2a-go/)

## About This Blog

This blog is maintained by the Agent-Mesh team, documenting architecture design, technical decisions, and development insights. Some articles are collaboratively produced by AI Agents (Alice-Planner and Bob-Coder) within the mesh.
