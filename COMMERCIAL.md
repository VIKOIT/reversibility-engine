# Commercial License

Copyright (c) 2026 Abdul Ghani (VIKOIT)

The Reversibility Engine is dual-licensed. You may use it under **either**:

1. the **GNU Affero General Public License v3.0 only** (AGPL-3.0-only), the terms
   of which are in [LICENSE](LICENSE) — at no cost; **or**
2. a **commercial license** from the copyright holder, on the terms below.

You only need a commercial license if the AGPL does not work for you. This page
explains when that is the case.

---

## Do you need a commercial license?

The AGPL is a strong copyleft license. Its distinguishing feature is **section 13**:
if you modify this software and let users interact with it **over a network**, you
must offer those users the complete corresponding source of your modified version
— even though you never distributed a binary to them.

That clause is the one that matters here, because this project ships a webhook
server (`revsrv`) that is designed to be run as a network service.

**The AGPL is almost certainly fine if you:**

- run `revctl` or `revsrv` unmodified, internally or publicly;
- modify it for purely internal use and never expose it to users over a network;
- build on it and are willing to release your changes under the AGPL;
- evaluate, test, or research it.

**You likely need a commercial license if you:**

- modify the engine and offer it — or a product containing it — to third parties
  as a hosted or SaaS service, without releasing your modifications;
- embed it in a proprietary product you distribute to customers;
- link it into a closed-source codebase that you cannot or will not release
  under the AGPL;
- redistribute it under different terms, or with your own branding, as part of a
  commercial offering;
- have a corporate policy, customer contract, or procurement requirement that
  prohibits AGPL-licensed code.

If you are unsure which column you fall into, ask. Clarifying it costs an email
and removes a risk that is otherwise easy to discover late.

---

## What the commercial license grants

A commercial license is negotiated per organization. Terms are tailored to the
deployment, but a standard agreement grants a **non-exclusive, non-transferable,
worldwide** right to:

- use, modify, and integrate the software **without** the AGPL's source-disclosure
  obligations, including section 13's network-use clause;
- distribute it in binary or source form as part of your own product, under your
  own license terms;
- operate it as a hosted service without publishing your modifications;
- remove the AGPL attribution requirements that would otherwise apply to your
  distribution (the copyright notice itself remains).

Commercial terms typically also cover the things the AGPL explicitly disclaims:

- a defined support and response commitment;
- a warranty and an indemnity, in place of the "AS IS" disclaimer in section 15
  and 16 of the AGPL;
- a named contact for security disclosures, with an agreed embargo window;
- optional priority on roadmap items and rule-table extensions.

The AGPL option always remains available. Buying a commercial license adds
permissions; it never removes the ones the AGPL already gives you.

---

## Scope and pricing

Pricing depends on how the software is used. The usual variables are:

| Factor | Typical basis |
| --- | --- |
| Deployment model | internal use, distributed product, or hosted service |
| Scale | number of repositories, developers, or analyzed pull requests |
| Term | annual subscription or perpetual with a support period |
| Support level | best-effort, business hours, or a contractual SLA |
| Redistribution | whether the software reaches your customers |

There is no public price list, because the terms that matter differ too much
between an internal deployment and an embedded redistribution. Send a short
description of your use case and you will get a concrete quote rather than a
range.

---

## How to enquire

Email **vikoit07@gmail.com** with:

1. your organization's name and country of registration;
2. what you intend to build with the engine;
3. whether it will be internal, distributed to customers, or offered as a
   hosted service;
4. rough scale — repositories, developers, or pull requests per month;
5. any procurement, security review, or legal requirements you already know about.

Points 1–3 are enough to start. The rest only shapes the quote.

---

## Compliance and enforcement

Copyright in this software is held by Abdul Ghani (VIKOIT). Use outside the AGPL
without a commercial license is copyright infringement.

If you believe you may be out of compliance, contacting **vikoit07@gmail.com**
first is always the better path. The intent of this dual license is to fund the
project's maintenance, not to generate disputes — good-faith enquiries are met
with a license, not a demand.

---

## A note on the AGPL choice

The AGPL was chosen deliberately rather than defensively. This engine exists to be
a merge gate people can trust, and a gate whose behaviour cannot be inspected is a
gate that should not be trusted. AGPL section 13 guarantees that anyone whose pull
requests are being graded by a modified copy of this engine can read the rules it
is grading them by.

The commercial license exists so that organizations who genuinely cannot meet that
obligation still have a supported, lawful path — not to make the open-source
option second-class.

---

**This document is a summary, not the contract.** It describes what a commercial
license generally covers; the binding terms are those in the signed agreement.
Nothing here is legal advice. If you are evaluating your obligations under the
AGPL, consult your own counsel.
