# Security Policy

Report vulnerabilities to **vikoit07@gmail.com**. Please do not open a public
issue for anything in the "in scope" list below.

Expect an acknowledgement within **72 hours** and an assessment within **7 days**.
If you have not heard back in a week, assume the mail was lost and send it again
— silence is a failure on this end, not a decision.

## Supported versions

| Version | Supported |
| --- | --- |
| `main` | Yes |
| 0.1.x | Yes |
| anything older | No — there is nothing older |

This project is pre-1.0. Fixes land on `main`; there is no backport branch yet.

## What to include

A report is actionable when it contains:

1. **What an attacker gains.** "Unauthenticated remote crash", "signature bypass",
   "arbitrary file read" — the impact, stated plainly.
2. **How to reproduce it.** A migration, a manifest, a webhook payload, or a
   `curl` invocation. A failing fixture directory is ideal: this repository is
   built around fixtures, and one that reproduces your finding can go straight
   into the regression suite.
3. **Which component.** `revsrv`, `revctl`, the engine, or an analyzer.
4. **Version or commit SHA.**

Encrypted mail is fine — ask for a key. If you prefer GitHub's private advisory
flow, open one from the Security tab and it will be picked up.

## Scope

`revsrv` is the part of this project exposed to the open internet, so it carries
most of the risk. It accepts unauthenticated HTTP from anyone who finds it, holds
GitHub App credentials able to read private repositories and write pull request
comments, and feeds attacker-controlled files to a cgo parser.

### In scope — please report

**Webhook authentication.** Anything that reaches the processor without a
signature computed with the real secret: an HMAC bypass, a timing oracle in the
comparison, a downgrade to the legacy SHA-1 header, replay of a captured
delivery, or a payload parsed before verification.

**Denial of service.** Any input that crashes, hangs, or exhausts memory in
`revsrv`. This is a live concern rather than a hypothetical: a chained SQL
expression previously overflowed the C parser's stack and killed the process,
which no `recover()` can catch. A guard now refuses such input
([ADR/0001](ADR/0001-parser-choice.md)), and a way around that guard is a valid
report.

**Credential exposure.** Installation tokens, the App private key, or the webhook
secret appearing in logs, error messages, pull request comments, or certificates.

**Cross-installation access.** Any path where one GitHub App installation's token
is used against another installation's repository.

**Certificate integrity.** A crafted changeset that makes the engine report a
passing grade for a change that is not reversible. **This is the most serious
class of bug in the project.** A wrong "safe" verdict is worse than a crash: a
crash is visible, and a false pass gets merged. Also in scope: making two
different changesets produce the same `InputDigest`, since the digest is what
attributes a certificate to an exact input.

**Comment injection.** Markdown or HTML in a migration, manifest, or filename
that escapes its code span and executes or renders as markup in a pull request
comment.

**Supply chain.** A dependency confusion, typosquat, or compromised module in
`go.mod`.

**CLA workflow.** Anything that lets a fork's pull request gain write access
through `pull_request_target` in `.github/workflows/cla.yml`.

### Out of scope

- Missing hardening headers on `/healthz`, which returns a fixed string.
- Reports that require an attacker to already hold the webhook secret, the App
  private key, or push access to the repository.
- Denial of service through sheer request volume against an unprotected
  deployment. Rate limiting belongs to your ingress, and the docs say so.
- Findings against a deployment that disabled signature verification. The server
  refuses to start without a secret; if you patched that out, the resulting
  exposure is yours.
- Automated scanner output with no demonstrated impact.
- The AGPL's absence of warranty. That is a licensing term, not a vulnerability.

## Disclosure

Coordinated disclosure, with a **90-day** default from acknowledgement to public
advisory. If a fix ships earlier, the advisory publishes earlier. If a fix needs
longer, you will be told why rather than asked to wait in silence.

You will be credited in the advisory and the changelog unless you prefer
otherwise. There is no bug bounty — this is a solo project. What is offered
instead is a fast, honest response and public credit.

## What happens to your report

1. **Acknowledged** within 72 hours.
2. **Assessed** within 7 days: reproduced, scoped, severity agreed with you.
3. **Fixed** on `main`, with a regression fixture so the bug cannot return
   silently. Every rule and every past security fix in this repository has one.
4. **Released** as a patch version, with an advisory naming the affected versions.
5. **Credited**, unless you ask otherwise.

If a report turns out to be out of scope or not a vulnerability, you will get a
clear explanation of why rather than a closed ticket.

## For operators

If you run `revsrv`:

- Terminate TLS in front of it. It speaks plain HTTP by design.
- Keep `GITHUB_WEBHOOK_SECRET` long and random. It is the only thing separating
  your installation from anyone who can reach the port.
- Prefer GitHub App credentials over a static `GITHUB_TOKEN`: App tokens are
  installation-scoped and expire hourly, and a leaked one has a much smaller
  blast radius.
- Run on a **glibc** base image, not Alpine — see
  [ADR/0001](ADR/0001-parser-choice.md).
- Do not add a checkout step to `.github/workflows/cla.yml`. It runs on
  `pull_request_target` with write access, and is safe only because it never
  executes contributor code.
