---
name: my-skill
description: Frontend Skills & Guidelines for crypto project. Use when you need to do front task.
---

This document describes the core skills, rules, and constraints for the frontend development of this project. 
The AI assistant must strictly follow these guidelines when writing or refactoring code.

## 🚫 Constraints (What to Avoid)
- **NO UI Frameworks**: Bootstrap, Tailwind, Foundation, and similar layout tools are strictly prohibited.
- **NO JS Frameworks**: React, Vue, Angular, Svelte, jQuery, and any other wrappers are strictly prohibited.
- **NO Heavy Dependencies**: The site must load instantly. Keep external dependencies to an absolute minimum.
- **Small Libraries**: You can use small stable libraies.

## 🛠 Tech Stack
- **Native JavaScript (Vanilla JS)**: Try to use pure, modern JS (ES6+).
- **Native CSS**: Use only pure CSS without preprocessors. Modern features such as CSS Variables are encouraged.
- **Backend Communication**: Use the binary format **[Protobuf](https://github.com/protocolbuffers/protobuf)** instead of JSON for maximum data transfer speed and efficiency.
- **PNPM**: use pnpm instead of npm

## 📱 Layout & Design (Mobile First)
- The project is **strictly Mobile First**. The interface and UX must be designed and optimized primarily for smartphones.
- Use **CSS Grid** extensively for layouts and element positioning:
  - `display: grid;`
  - `grid-template-columns`
  - `gap`
- Make broad use of CSS mathematical functions, especially **`calc()`**, for flexible, responsive, and precise sizing.

## ⚡ Performance
- Primary Goal: **Maximum speed and lightweight footprint**.
- Interact directly with the DOM with minimal overhead.
- Optimize the loading of all resources (scripts, styles, assets).
