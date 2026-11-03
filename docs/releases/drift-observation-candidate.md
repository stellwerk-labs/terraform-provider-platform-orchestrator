# Runner capacity and observation protocols, release candidate

This candidate exposes the generic Runner settings used for bounded observation
work. Publication and the coordinated release versions are still pending.

## Changes

- Add optional `max_parallel_jobs` and `supported_protocols` to all five Runner
  resources and data sources.
- Preserve operator-managed Runner configuration when optional settings are
  omitted, including on import. A no-op that matches an existing operator value
  does not establish Terraform ownership; apply a real configured update to
  start managing that value.
- Clear a setting when it is removed after Terraform has actually managed it.
  An explicit empty protocol set declares no protocols.

## Configuration behavior

`max_parallel_jobs` must be positive. It limits concurrent work across all
Environments using that Runner. An unset limit preserves legacy unlimited
behavior. Lowering the limit lets existing jobs drain before new work is admitted.

`supported_protocols` declares what the installed Runner executable supports.
Read-only observations require `environment-observation/v1` and a positive
capacity limit. A declaration does not upgrade or discover the executable.

The generated resource documentation describes both attributes and their
ownership behavior. Review a full Terraform/OpenTofu plan before applying changes
to imported or operator-managed Runners.

## Upgrade

Use the matching qualified server set. Final versions will be added after the
public SDK and release-consumer checks. These are generic Runner settings;
installing this provider does not install, enable or license an observation
plugin, and this release adds no Drift-specific Terraform resources.
