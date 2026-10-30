# Project Context

Project Context answers:

> What is this Project and how is it organized?

It is long-lived and reusable. Dev Bridge establishes or reuses it after you
explicitly select a Project.

Project Context contains the project's guidance, major production modules and
their responsibilities, production roots and entrypoints, and major
relationships. It produces a reusable Project Module Map that helps later work
begin from sensible parts of the codebase.

When a root `AGENTS.md` exists, Dev Bridge uses it as guidance and validates the
relevant production structure. When it does not exist, Dev Bridge performs
lightweight production-architecture discovery. The absence of `AGENTS.md` does
not mean there is no Project understanding.

Project Context is deliberately not an exhaustive repository audit and does
not try to solve a particular engineering goal.

## Project Context and Feature Area Review

| | Project Context | Feature Area Review |
|---|---|---|
| Main question | What is this Project and how is it organized? | What production implementation matters for this engineering goal? |
| Lifetime | Long-lived and reusable | Per Feature Area and per Review |
| Starting point | Project guidance and production structure | Feature Area-relevant modules and production areas |
| Depth | Lightweight architecture understanding | Focused implementation investigation |
| Breadth | Major modules, roots, entrypoints, and relationships | Relevant code and any materially necessary production dependencies |
| Goal | Produce or reuse the Project Module Map | Gather enough understanding to support an engineering decision and Task |

A focused Feature Area Review does not claim that unselected code can never matter.
It prunes unrelated information for efficiency and can follow materially
relevant production dependencies where necessary. It does not aim to understand
the entire repository merely for completeness.

## What `AGENTS.md` means

`AGENTS.md` is repository or project guidance. It can describe long-lived
architecture constraints, coding conventions, forbidden practices, and other
project-specific development rules.

It is not Feature Area state, Task state, a Codex Skill, or a replacement for Project
Context. Do not create one `AGENTS.md` for each Feature Area. Feature Area-specific intent
belongs in the Feature Area, Review, and Task instead.
