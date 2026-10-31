---
title: 0005 Licence
description: "NebulaCtrl is licensed under AGPL-3.0-only."
section: Internals
order: 5
---

# 0005. License: AGPL-3.0-only

- Status: **accepted**
- Date: 2026-10-27
- In effect: the source is published on GitHub under this licence.

## Context

A licence must be chosen before public release: without a `LICENSE` file the code is "all rights reserved" by default.

Relevant facts:
- NebulaCtrl is self-hosted software: users run the control plane and the agents themselves. Third parties may also offer it as a hosted service.
- `nebulactrl/nebula` (design-system source) is Apache-2.0; the ported web files keep compatible terms under the AGPL (Apache-2.0 is one-way compatible with GPLv3 and AGPLv3).
- The dependency tree was scanned on 2026-10-27: see [Third-party licences](../security/third-party-licences.md).

## Options

| | Apache-2.0 | AGPL-3.0 |
|---|---|---|
| Use, modify, self-host | Unrestricted | Unrestricted |
| Closed-source forks / commercial hosting by others | Allowed | Hosting a modified version as a network service requires publishing the changes |
| Patent grant | Explicit | Included (GPLv3 terms) |
| Adoption friction in companies | Low | Many companies forbid AGPL |
| Contributor/CLA pressure | Low | Needs a CLA if you later want dual licensing |
| Protection against a competitor offering a hosted NebulaCtrl | None | Strong |
| Compatibility with embedding in other products | Easy | Hard |

## Decision

NebulaCtrl is licensed under the **GNU Affero General Public License, version 3 only**, SPDX identifier `AGPL-3.0-only`.

- The canonical, unmodified text from <https://www.gnu.org/licenses/agpl-3.0.txt> is in `LICENSE` at the repository root.
- "Only", not "or later": the project does not hand future relicensing decisions to the Free Software Foundation. Grafana and Mattermost make the same choice.
- `apps/web/package.json` carries `"license": "AGPL-3.0-only"` and stays `"private": true` because it is not published to npm. Every other manifest in the repository carries the same field.
- Dependencies must be compatible with the AGPL (permissive licences, LGPL, MPL-2.0 and similar). Proprietary, SSPL, BUSL, non-commercial and GPL-2.0-only dependencies are not allowed. The scan result and the rule are in [Third-party licences](../security/third-party-licences.md).
- Contributions are accepted under the same licence (inbound = outbound). There is no CLA. Adding one, or any dual licensing, is a later project decision and would need contributors' consent.

## Reasoning

- The goal is to keep hosted derivatives open. The AGPL's network clause (§13) requires anyone who runs a modified NebulaCtrl as a network service to offer their users the corresponding source. Apache-2.0 gives no such protection.
- Self-hosting, modification and internal use stay unrestricted, so the adoption cost falls on the few organisations that ban the AGPL, not on typical self-hosters. Comparable products (Grafana, Mattermost) made the same trade.
- The platform services NebulaCtrl deploys for users include AGPL software (Loki). Grafana is only a user template in the catalog; NebulaCtrl does not run it. Using the AGPL for the platform itself is consistent with that and avoids a licence-mix story in the documentation.
- The platform is not a library. Embedding in other products, the main AGPL drawback, is not a use case.

## Consequences

- The licence does not block public release or external contributions.
- Container images that NebulaCtrl **deploys** (the template catalog, database engines, the built-in instance services SeaweedFS, the registry and Loki, and Headscale) are **separate works** under their own licences. NebulaCtrl runs them unmodified in separate containers and does not link them; the AGPL of NebulaCtrl does not extend to them and theirs does not extend to NebulaCtrl.
- Users who modify NebulaCtrl and offer it to others over a network must publish their modified source.
- New dependencies must pass the licence rule above before they are approved.

## Alternatives considered

Apache-2.0 (maximum adoption; rejected because it does not keep hosted derivatives open), MIT/BSD (no patent grant), MPL-2.0 (file-level copyleft; weaker fit for a single-binary product), AGPL-3.0-or-later (cedes future relicensing to the FSF), a source-available license such as BSL (contradicts the goal of an open-source project).
