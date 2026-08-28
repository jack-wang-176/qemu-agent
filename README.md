# QEMU Modeling Agent

[README.zh-CN.md](README.zh-CN.md).

`qemu-agent` is a constrained engineering agent for turning peripheral documentation into reviewable QEMU device-model artifacts.

It is deliberately specialized. A conversation does not grant arbitrary shell access, invent a workflow, or write a QEMU source tree. The agent interprets a request, binds it to one modeling project, and advances a fixed evidence-producing pipeline.

```text
                         untrusted text
                              |
       +----------------------+----------------------+
       |                                             |
   CLI / Telegram                              slash commands
       |                                             |
       v                                             v
 Application.Handle -------------------------- CommandRouter
       |
       v
 modelingagent.Runner
       |
       +--> session clone / bounded history / event identity
       v
 modelingworkflow.Controller
       |
       +--> Dialogue: text -> constrained Intent
       +--> Binding: conversation -> active Project
       +--> Presenter: public result, never raw payload
       v
 modelingapi.Service
       |
       +--> CallContext, Scope, revision and authorization checks
       v
 pipelineapi.Engine
       |
       +--> RuntimePorts: repository, artifact, completion, effect, events
       +--> QueryAdapter: bounded reads with digest verification
       v
 plan -> extract -> infer -> emit -> awaiting_apply
                                      |
                                      +-- human review boundary
```

## Product boundary

The production entry point accepts ordinary text. `/model` and `/modeling` are not control planes. The modeling model is selected at startup and is not changed by a conversation. The interpreter may select only these intents:

```text
start | start_new | continue | inspect | provide_input
read_artifact | evidence
```

The controller, not the language model, chooses the next legal pipeline operation. A user can provide a requirement or a workspace-relative source, but cannot provide a project ID, revision, stage name, absolute path, approval token, or idempotency key as authority.

## Pipeline

```text
reference material
      |
      v
 plan      scope, acceptance goals and open questions
      |
      v
 extract   structured Reg-IR from audited source reads
      |
      v
 infer     documented reset, fields, effects and interrupts
      |
      v
 emit      deterministic C/build artifacts, manifest and diff
      |
      v
 awaiting_apply   no QEMU source-tree mutation
```

`Reg-IR` is a validated intermediate representation. Structural errors are rejected; unknown reset values and unsupported hardware are recorded as explicit questions. `emit` is deterministic for one IR, so the reviewed bytes are the bytes that a future approved apply operation would use.

The first production line stops at `awaiting_apply`. Apply and Verify are stable API concepts and remain available to lower-level tests and future adapters, but the production composition root does not construct an Apply executor. This keeps artifact generation separate from changing a QEMU checkout.

## State ownership

```text
Session  = conversation messages and token accounting
Binding  = conversation -> active project plus pending intake
Project  = pipeline state, revision and artifact references
Artifact = immutable content-addressed bytes
Event    = lossy progress notification, never recovery state
```

A successful request saves a cloned Session before replacing the live session. Project revision is the modeling transaction source of truth. Binding version protects only the conversation mapping. Artifact bodies are never placed in session history, event text, memory, or ordinary error messages.

## Security model

All filesystem and process effects pass through the audited security executor. The request identity travels in a private context value:

```text
Caller{TraceID, SessionID, SessionKey, Channel, Interactive}
```

Policy and approval remain separate decisions. A missing or invalid caller fails closed. Sources are logical workspace-relative references; adapters resolve them under the configured workspace root. Errors exposed to users are bounded public categories. Logs retain categories and correlation identifiers, not datasheet contents, model responses, or tool output.

## Build and run

Requirements: Go 1.26 or newer and an OpenAI-compatible provider. Ollama can be used locally.

```bash
cd qemu-agent
export QEMU_AGENT_PROVIDER=ollama
export QEMU_AGENT_MODEL=qwen2.5-coder:7b
export QEMU_AGENT_MODELING_ENABLED=true
export QEMU_AGENT_MODELING_AUTO_APPLY=false
export QEMU_AGENT_WORKSPACE=/path/to/modeling-workspace
go run ./cmd/qemu-agent
```

For OpenAI or OpenRouter, set the corresponding API key and provider variables. `QEMU_AGENT_MODELING_ENABLED=true` is an explicit product opt-in. The process refuses to start when it is absent or when `AUTO_APPLY=true`; it never silently falls back to a generic chat agent.

The workspace contains source material and agent-private files are placed under `QEMU_AGENT_DATA_DIR`. The QEMU source tree is not touched by the production workflow. Use normal conversation turns to describe the target, provide a source path, continue, inspect, and read the generated artifact.

## Repository map

```text
cmd/qemu-agent/        process entry and flag parsing
internal/app/          composition root and basic session commands
internal/modelingagent request runner, dialogue and event bridge
internal/modelingworkflow intent controller, binding and presentation
internal/modelingapi   stable modeling service contract and DTOs
internal/adapter/      Pipeline and Runtime port adapters
internal/pipelineapi   engine, snapshots, queries and effects
internal/modeling/     pipeline state machine, stages and artifact store
internal/session/      conversation registry and persistence
internal/tools/        tools, policy, approval and audit
internal/runstream/    bounded request event protocol
```

`internal/agent` remains as a shared boundary package for `RunInput` and event types. It is not constructed as the production runner.

## Engineering checks

```bash
gofmt -l cmd internal
go test ./...
go test -race ./internal/app ./internal/modelingagent ./internal/modelingworkflow
go vet ./...
go build ./...
```

Generated device code is an artifact for human review. Applying it to a QEMU tree requires a separate, explicitly approved integration layer.

For the complete Chinese description, see [README.zh-CN.md](README.zh-CN.md).

