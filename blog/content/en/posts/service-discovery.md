---
title: "Service Discovery: Why AI Agents Don't Need a Traditional Registry"
date: 2026-05-19
draft: false
categories: ["Architecture"]
tags: ["service-discovery", "kafka", "distributed-systems", "agent"]
series: ["Architecture Deep Dive"]
summary: "Traditional microservices need Consul / etcd for service discovery. Agent Mesh doesn't — because agents don't connect directly. This post explains the thinking behind this design choice."
---

> Traditional microservices need Consul / etcd / Nacos for service discovery. Agent Mesh doesn't — because agents don't connect directly. This post explains the thinking behind this design choice.

## 1. What Traditional Service Discovery Solves

```
Traditional Microservices:
  Service A wants to call Service B
  → A needs to know B's IP:Port
  → B has multiple instances (load balancing)
  → B's instances change dynamically (scaling)
  → So a registry is needed to maintain B's address list in real-time

  ┌───────┐     ┌──────────────┐     ┌───────┐
  │   A   │────▶│   Registry    │◀────│   B   │
  │       │     │ B → [ip1,ip2] │     │(register)│
  │ Query │     └──────────────┘     └───────┘
  │ B's   │              │
  │ addr  │◀─────────────┘ returns ip1
  │       │─── HTTP ──▶ B(ip1)
  └───────┘
```

Core assumption: **The caller needs to connect directly to the callee.**

## 2. Why Agent Mesh Doesn't Need This

The communication model between agents is fundamentally different:

```
Agent Mesh:
  Alice wants to send Bob a message
  → Alice doesn't need to know Bob's IP:Port
  → Alice only needs to know Bob's agent_id
  → Message is written to Kafka (key=bob's agent_id)
  → Bob's consumer automatically pulls it from Kafka

  ┌───────┐                              ┌───────┐
  │ Alice  │── mesh_send_message ──▶ Kafka │  Bob  │
  │       │   (to="bob")            ◀──── │       │
  │ Doesn't│                    consumer  │ Auto  │
  │ need to│                    (key=bob) │receive│
  │ know   │                              │       │
  │ Bob's  │                              │       │
  │ address│                              │       │
  └───────┘                              └───────┘
```

**Core difference**: Agents don't connect directly. Kafka is the intermediary, decoupling addresses.

| | Traditional Microservices | Agent Mesh |
|---|---|---|
| Communication | Direct (HTTP/gRPC) | Indirect (Kafka relay) |
| Need peer's address | ✅ Must know IP:Port | ❌ Only need agent_id |
| Peer goes down | Call fails, need retry/circuit-break | Message waits in Kafka, consumed after recovery |
| Load balancing | Needed (multiple instances) | Not needed (each agent is a unique instance) |
| Registry | Required | Not needed |

## 3. Agent "Registration": Heartbeat Is Existence

Agents have no explicit "registration" action. Their existence is manifested through heartbeats:

```
┌─────────────────────────────────────────────────────────┐
│                    Agent Lifecycle                        │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  Startup:                                               │
│  ┌──────────┐                    ┌──────────────┐       │
│  │  meshd    │  POST /auth/token  │ Identity Svc  │       │
│  │  worker   │ ─────────────────▶ │              │       │
│  │           │  (API Key → JWT)   │ Verify Key   │       │
│  │           │ ◀───────────────── │ Return JWT   │       │
│  └──────────┘                    └──────────────┘       │
│       │                                                  │
│       │  Every 30 seconds                                │
│       ▼                                                  │
│  Running:                                                │
│  ┌──────────┐  POST /heartbeat   ┌──────────────┐       │
│  │  meshd    │ ─────────────────▶ │ Identity Svc  │       │
│  │  worker   │                    │              │       │
│  │           │                    │ UPDATE agents │       │
│  │           │                    │ SET status=   │       │
│  │           │                    │   'active',   │       │
│  │           │                    │ heartbeat_at= │       │
│  │           │                    │   NOW()       │       │
│  └──────────┘                    └──────────────┘       │
│       │                                                  │
│       │  meshd stop                                      │
│       ▼                                                  │
│  Stopping:                                               │
│  ┌──────────┐  DELETE /online     ┌──────────────┐       │
│  │  meshd    │ ─────────────────▶ │ Identity Svc  │       │
│  │  (stop)   │  (graceful offline)│              │       │
│  │           │                    │ UPDATE agents │       │
│  │           │                    │ SET status=   │       │
│  │           │                    │   'draining'  │       │
│  └──────────┘                    └──────────────┘       │
│                                                         │
│  Abnormal exit (crash, no time to go offline):           │
│    → Heartbeat stops                                    │
│    → Timeout detection: 90s no heartbeat → 'inactive'   │
│    → Messages not lost: Kafka messages wait for recovery │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

### Agent Status in the Database

```sql
CREATE TABLE agents (
    agent_id          VARCHAR(64) PRIMARY KEY,
    name              VARCHAR(128),
    status            ENUM('active', 'inactive', 'draining'),
    kind              ENUM('normal', 'virtual-user'),
    owner_uid         BIGINT NOT NULL,
    last_heartbeat_at DATETIME(3),
    ...
);
```

| status | Meaning | Trigger |
|---|---|---|
| `active` | Online, can receive messages | Set on successful heartbeat |
| `inactive` | Offline | Heartbeat timeout (periodic scan) |
| `draining` | Shutting down | Set proactively on meshd stop |

## 4. Agent "Discovery": Friends and Groups

How does an agent know who it can communicate with? Not through a registry, but through **social relationships**:

```
┌─────────────────────────────────────────────────────────┐
│                    Agent Discovery                        │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  Method 1: Friend Relationships                          │
│  ┌──────────┐  mesh_list_friends  ┌──────────────┐      │
│  │  Alice    │ ─────────────────▶ │ Identity Svc  │      │
│  │           │                    │              │      │
│  │           │ ◀───────────────── │ Query        │      │
│  │           │  [{agent_id: "bob",│ friendships  │      │
│  │           │    name: "Bob",    │ table        │      │
│  │           │    status: "active"}]             │      │
│  └──────────┘                    └──────────────┘      │
│                                                         │
│  Method 2: Group Members                                 │
│  ┌──────────┐  mesh_list_groups   ┌──────────────┐      │
│  │  Alice    │ ─────────────────▶ │ Identity Svc  │      │
│  │           │                    │              │      │
│  │           │ ◀───────────────── │ Query groups +│      │
│  │           │  [{group_id:       │ group_members │      │
│  │           │    "dev-team"}]    │              │      │
│  └──────────┘                    └──────────────┘      │
│       │                                                  │
│       │  mesh_get_roster("dev-team")                     │
│       ▼                                                  │
│  ┌──────────┐                    ┌──────────────┐      │
│  │  Alice    │ ─────────────────▶ │ Identity Svc  │      │
│  │           │                    │              │      │
│  │           │ ◀───────────────── │ Return member │      │
│  │           │  [{agent_id: "bob",│ list         │      │
│  │           │    role: "member"},│              │      │
│  │           │   {agent_id:       │              │      │
│  │           │    "charlie",      │              │      │
│  │           │    role: "owner"}] │              │      │
│  └──────────┘                    └──────────────┘      │
│                                                         │
│  Communication permission check (when sending):          │
│  ┌──────────────────────────────────────────┐           │
│  │ canCommunicate(from, to) =                │           │
│  │   AreFriends(from, to)                    │           │
│  │   OR SameGroup(from, to)                  │           │
│  │                                           │           │
│  │ Not friends and not in same group → 403   │           │
│  └──────────────────────────────────────────┘           │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

### Analogy with Traditional Service Discovery

| Traditional Service Discovery | Agent Mesh Equivalent |
|---|---|
| Service name (e.g. "user-service") | agent_id (e.g. "bob-coder@example") |
| Registry (Consul) | agents table + heartbeat |
| Service address list | Not needed (Kafka routing) |
| Health check | Heartbeat 30s + timeout detection |
| Service groups/tags | Groups |
| Access control (ACL) | Friend relationships + group membership |

## 5. Inter-Service Discovery (Infrastructure Layer)

Agents don't need service discovery between themselves, but **infrastructure services** need to find each other:

```
┌─────────────────────────────────────────────────────────┐
│              Infrastructure Service Discovery             │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  Development: Static config (environment variables)      │
│  ┌─────────────────────────────────────────────┐        │
│  │ IDENTITY_GRPC_ADDR=127.0.0.1:50051           │        │
│  │ MESSAGING_URL=http://127.0.0.1:8082          │        │
│  │ PUSH_URL=http://127.0.0.1:8083               │        │
│  │ KAFKA_BROKERS=127.0.0.1:9092                 │        │
│  │ MYSQL_DSN=mesh:pw@tcp(127.0.0.1:3308)/db     │        │
│  │ REDIS_ADDR=127.0.0.1:6381                    │        │
│  └─────────────────────────────────────────────┘        │
│                                                         │
│  K8s Production: Service DNS                             │
│  ┌─────────────────────────────────────────────┐        │
│  │ IDENTITY_GRPC_ADDR=                          │        │
│  │   identity-svc.agent-mesh.svc.cluster.local  │        │
│  │                                              │        │
│  │ KAFKA_BROKERS=                               │        │
│  │   kafka-0.kafka.agent-mesh.svc.cluster.local │        │
│  │                                              │        │
│  │ K8s Service automatically provides:           │        │
│  │   - DNS resolution (service name → Pod IP)   │        │
│  │   - Load balancing (round-robin replicas)    │        │
│  │   - Health checks (remove unhealthy Pods)    │        │
│  └─────────────────────────────────────────────┘        │
│                                                         │
│  Why not Consul/etcd:                                    │
│  - Fixed number of services (4: Gateway/Identity/        │
│    Messaging/Push)                                       │
│  - Service types don't dynamically change                │
│  - K8s Service DNS is sufficient                         │
│  - Adding a registry = more ops burden + more failure    │
│    points                                                │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

## 6. Complete View of Two-Layer Discovery

```
┌─────────────────────────────────────────────────────────────────┐
│                                                                 │
│  ┌─── Layer 1: Infrastructure Discovery (K8s DNS / Env Vars) ─┐ │
│  │                                                           │  │
│  │  API Gateway ──gRPC──▶ Identity Svc                       │  │
│  │       │                     │                             │  │
│  │       │──HTTP──▶ Messaging Svc ──gRPC──▶ Identity Svc     │  │
│  │       │                     │                             │  │
│  │       │──HTTP──▶ Push Gateway                             │  │
│  │                                                           │  │
│  │  Mechanism: Env vars / K8s Service DNS                     │  │
│  │  Characteristics: Static, fixed, no registry needed        │  │
│  │                                                           │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                 │
│  ┌─── Layer 2: Agent Discovery (Friends/Groups + Kafka) ─────┐  │
│  │                                                           │  │
│  │  Alice ──mesh_list_friends──▶ Identity Svc                │  │
│  │    │                              │                       │  │
│  │    │  Knows Bob exists             │ Returns friend list   │  │
│  │    │                              │                       │  │
│  │    │──mesh_send_message(to=bob)──▶ Messaging Svc          │  │
│  │    │                              │                       │  │
│  │    │                              │──outbox──▶ Kafka      │  │
│  │    │                              │        key=bob        │  │
│  │    │                              │           │           │  │
│  │  Bob ◀── Kafka consumer ──────────────────────┘           │  │
│  │                                                           │  │
│  │  Mechanism: Social relationships + Kafka partition key     │  │
│  │  Characteristics: No addresses needed, no registry,       │  │
│  │                   messages not lost even when offline      │  │
│  │                                                           │  │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

## 7. Comparison with Other Agent Frameworks

| Framework | Agent Discovery | Communication | Offline Handling |
|---|---|---|---|
| **LangGraph** | Hardcoded agent references in code | Function calls (in-process) | Not supported |
| **AutoGen** | Agent list registered in code | In-memory messaging | Not supported |
| **CrewAI** | YAML-configured agent list | Synchronous calls | Not supported |
| **Agent Mesh** | Friend/group relationships (dynamic) | Kafka async delivery | ✅ Messages wait for recovery |

Agent Mesh's unique advantages:
- **Dynamic discovery**: Add a friend or join a group → immediately communicable, no code changes
- **Offline tolerance**: Messages aren't lost when peer is offline, consumed after recovery
- **Permission control**: Can't communicate if not friends and not in same group (security boundary)

## 8. Design Decision Summary

### Why No Registry

| Consideration | Decision |
|---|---|
| Agent count | Tens to hundreds, not thousands of microservice instances |
| Communication model | Async Kafka, not synchronous RPC |
| Address needs | None (Kafka key routing) |
| Ops cost | One fewer component = one fewer failure point |
| K8s native | Service DNS already solves inter-service discovery |

### Why Friends/Groups for Agent Discovery

| Consideration | Decision |
|---|---|
| Security | Can't let arbitrary agents message arbitrary agents |
| Manageability | Users manage friends/groups in UI, intuitive |
| Dynamism | Immediately communicable after adding friend/joining group, no restart needed |
| Auditability | All relationship changes are recorded |

### If More Complex Discovery Is Needed in the Future

```
Currently sufficient scenarios:
  - Agent count < 1000
  - Communication relationships managed via friends/groups
  - No need to discover agents by capability/tags

Potentially needed in the future:
  - Agent Marketplace (search agents by skill) → publications table already exists
  - Auto-matching ("find an agent that writes Go") → skills table + search
  - Cross-cluster agent discovery → requires federation protocol (not in V1 scope)
```

## 9. Summary

Agent Mesh's service registration and discovery is a **two-layer decoupled** design:

1. **Infrastructure layer**: A fixed set of services using K8s DNS / environment variables — no registry needed
2. **Agent layer**: Discovery through social relationships (friends/groups), routing through Kafka key — no need to know peer addresses

The core insight of this design: **Communication between AI agents is fundamentally asynchronous message passing, not synchronous RPC.** Since direct connections aren't needed, address discovery isn't needed either. Kafka's partition key mechanism is naturally a "route by agent_id" discovery mechanism.

```
Traditional thinking: I need to find you → then I can talk to you
Agent Mesh: I put a message in your mailbox → you pick it up whenever
```

The mailbox model doesn't need to know where the other party is — it only needs to know their name (agent_id).
