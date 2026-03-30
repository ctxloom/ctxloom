---
slug: /
sidebar_position: 1
---

# SCM - Sophisticated Context Manager

A CLI tool for managing context fragments and prompts for AI coding assistants.

## The Problem

When working with AI coding assistants, you repeatedly provide the same context: coding standards, language patterns, security guidelines. This wastes tokens and creates inconsistency across projects and team members.

## The Solution

SCM organizes context into reusable **bundles** that can be:

- **Assembled on demand** - Combine bundles and fragments for different tasks
- **Grouped into profiles** - Switch contexts with a single flag (`-p developer`)
- **Shared across teams** - Pull bundles from remote repositories (GitHub/GitLab)
- **Token-optimized** - Distill content to minimal versions using AST-aware compression
- **Preserved across sessions** - Save and recover context with session memory

:::note Pre-release
This is a pre-release project. It works and is in active use, but architectural improvements and refactoring are ongoing.
:::

## Next Steps

- [Installation](/getting-started/installation) - Get SCM installed
- [Quick Start](/getting-started/quickstart) - Create your first bundle
- [Key Concepts](/concepts/bundles) - Understand bundles, fragments, profiles
