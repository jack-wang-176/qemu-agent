---
name: peripheral-modeling
description: Plan, implement, and verify QEMU peripheral models from hardware documentation.
version: "1"
tags:
  - qemu
  - modeling
required_tools:
  - read
  - write
  - bash
---

# Peripheral modeling workflow

1. Locate the SoC integration point before writing a device.
2. Extract register reset values and access semantics into a table.
3. Implement the smallest vertical slice.
4. Add qtest or equivalent evidence before claiming completion.
