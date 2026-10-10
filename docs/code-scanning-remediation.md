# Code-scanning remediation record

Baseline inspected on 2026-10-10: nine open findings on `main` at
`3cfc259f96c78a27dd417afd8dd8c64206c752ca`, from zizmor 1.30.1 and
OpenSSF Scorecard v5.5.0. These are workflow, testing and governance findings,
not nine demonstrated application vulnerabilities. Alert state on the default
branch is authoritative; a proposed fix or a successful PR check is not closure.

The pinned Scorecard action's [policy](https://github.com/ossf/scorecard-action/blob/2d1146689b8cda280b9bc96326124645441f03bc/policies/template.yml)
requires scores of 10 for Branch-Protection, Code-Review, Security-Policy and
Fuzzing; 5 for CII-Best-Practices (a passing badge); and 1 for Maintained.
Partial score improvements do not necessarily clear an alert. Keep these
thresholds intact rather than weakening policy to make the dashboard green.

## Repository changes

| Alert | Cause | Remediation and verification |
| --- | --- | --- |
| [1](https://github.com/AviBackToBlack/container-bin/security/code-scanning/1) | GitHub Actions updater lacks a seven-day cooldown | Set `cooldown.default-days: 7`; verify zizmor on the PR and after merge. Dependabot security updates are not delayed. |
| [2](https://github.com/AviBackToBlack/container-bin/security/code-scanning/2) | Go updater lacks a seven-day cooldown | Apply the same setting to `gomod`; retain security-update groups and existing schedules. |
| [15](https://github.com/AviBackToBlack/container-bin/security/code-scanning/15) | `upload-artifact` SHA matches `v7.0.1`, not the moved `v7` tag in its comment | Correct only the version comment; preserve the full qualified SHA and all release gates. |
| [9](https://github.com/AviBackToBlack/container-bin/security/code-scanning/9) | Security policy lacks a direct reporting URL | Link to the private advisory reporting form. Private reporting was verified enabled; retain the seven-day response target and trust-boundary policy. Verify a fresh Scorecard result after merge. |
| [10](https://github.com/AviBackToBlack/container-bin/security/code-scanning/10) | No fuzz integration detected | Add native Go fuzz targets with semantic invariants and bounded Linux/Windows CI campaigns. Verify actual fuzz execution, regular seed tests and fresh Scorecard recognition after merge. |

The fuzz targets cover registry acceptance and tool identity, lockfile semantic
round-trips/version gating, exact checksum-manifest acceptance, Windows option
normalization and WSL workspace confinement. They use no real Docker, network,
installation or filesystem classifier. See [CONTRIBUTING.md](../CONTRIBUTING.md)
for campaigns and regression-corpus handling. Bounded fuzzing does not prove the
absence of vulnerabilities or replace real-platform release qualification.

## Review and protection decisions: owner approval required

[Alert 8](https://github.com/AviBackToBlack/container-bin/security/code-scanning/8)
reports branch protection at 3/10. Effective rulesets already protect `main`
against deletion/force push, require PRs and checks, and enforce CodeQL gates.
The classic branch-protection API returning 404 does not mean those rulesets
are absent. Remaining verified gaps are zero required approving reviews,
disabled latest-push approval, non-strict required checks, and administrator
PR bypass in `main-review` (not an unrestricted bypass of every ruleset).

[Alert 11](https://github.com/AviBackToBlack/container-bin/security/code-scanning/11)
reports 1/29 approved changesets. Recent independent review comments exist,
but their GitHub review state is `COMMENTED`, not `APPROVED`. AI/bot reviews do
not replace independent human review under Scorecard's criterion.

Before any settings change:

1. Identify two genuinely independent human reviewers with permissions sufficient
   to satisfy required GitHub approvals. The full 10/10 branch-protection policy
   requires at least two approving reviews, not just the first-tier improvement
   of one. A GitHub `User` account alone is not
   evidence that a review is human or eligible.
2. Arrange eligible independent CODEOWNER coverage with the owner's approval.
   `CODEOWNERS` currently names only the repository owner; removing the review
   bypass while requiring owner review can block owner-authored PRs. Do not
   disable CODEOWNER review to manufacture a better score.
3. Obtain explicit approval for the exact ruleset edits: require at least two
   approvals, latest-reviewable-push approval and up-to-date checks; remove the
   `main-review` administrator bypass once independent review is feasible.
   Preserve stale-review dismissal, thread resolution, integrity and security
   rules, existing required checks and full-SHA action pins.
   After the fuzz jobs have landed and emitted successful check runs on `main`,
   add the exact contexts `Fuzz (Linux)` and `Fuzz (Windows)` to `main-integrity`'s
   existing required checks, bound to the GitHub Actions integration (15368).
   Until that separately approved settings change, fuzz failures are visible
   but do not themselves block merges. Keep the six existing required contexts;
   do not replace them or require contexts before their jobs exist on `main`.
4. Read back effective rules and inspect the next real PR's review/merge gates.
   Verify fresh Scorecard results. Review-history scoring will improve only as
   genuinely approved changes enter the rolling window. The current alert
   threshold is 10/10, so historical unreviewed changes may keep it open after
   settings improve; do not rewrite history or create artificial changes to
   flush it.

No repository settings, reviewer permissions or bypasses are changed by the
repository fixes. If independent review is unavailable, record the residual
risk for an explicit owner decision rather than silently dismissing findings.

## Time-dependent assessment: alert 12

[Maintained](https://github.com/AviBackToBlack/container-bin/security/code-scanning/12)
scores zero because the repository was created `2026-08-16T23:22:46Z`, less
than 90 days before the baseline scan. It is not archived. The 90-day point is
`2026-11-14T23:22:46Z`; reassess on or after 2026-11-15 UTC using a fresh
Scorecard analysis and genuine commit/issue activity. This is an assessment-age
limitation, not a code defect. Passing after that date is not guaranteed.

Do not manufacture activity, alter creation/history metadata, disable the check,
or claim the alert is fixed early. This document does not schedule a workflow,
create a monitor or authorize dismissal; that remains a separate owner decision.

## Best Practices assessment: alert 13

[CII-Best-Practices](https://github.com/AviBackToBlack/container-bin/security/code-scanning/13)
reports no detected OpenSSF badge participation. Registration requires the
owner's approval and a third-party account; no account or project entry is
created by the repository changes.

Initial evidence available for an owner-led
[passing-badge assessment](https://www.bestpractices.dev/en/criteria/0):

- Purpose, installation and reference material: README and `docs/`.
- Public contribution and bug-reporting processes: CONTRIBUTING and issue
  templates; source history and reviewable PRs are public.
- FLOSS license: Apache-2.0 in LICENSE.
- Versioned releases and human-readable release notes: GitHub releases.
- Private vulnerability reporting and response target: SECURITY and the enabled
  GitHub reporting setting.
- Automated testing and static analysis: cross-platform CI, race tests,
  CodeQL, govulncheck, dependency review, zizmor and the proposed fuzz campaigns.
- Release integrity evidence: full-SHA actions, deterministic builds,
  independent reproduction, checksums and provenance.

These pointers are inputs, not a completed criteria audit or badge claim. The
owner must assess every applicable criterion and provide truthful evidence,
including actual report-response performance, maintainer security expertise,
and any remaining process gaps. Register the exact repository URL, pursue the
passing badge (5/10 meets the current action policy), then verify the badge API
and a fresh Scorecard result.
Registration or an in-progress badge alone does not imply this alert closes.

## Completion boundary

All nine findings remain in scope. Verify each against fresh default-branch
scanner results after approved changes land. Keep unresolved governance and
time-dependent items visible; do not count plans, open PRs, registration, elapsed
time alone or manually dismissed findings as verified remediation. Published
`v2.0.0` tags/assets and release qualification remain untouched.
