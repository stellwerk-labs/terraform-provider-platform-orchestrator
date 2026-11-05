# Runner capacity and protocol configuration

Every Runner resource accepts `max_parallel_jobs` and `supported_protocols` inside
`runner_configuration`. Every corresponding data source reports their current
Core values. These fields work for Kubernetes, Kubernetes Agent, EKS, GKE and ECS.

```hcl
runner_configuration = {
  max_parallel_jobs  = 2
  supported_protocols = ["environment-observation/v1"]
  # Include the Runner family's ordinary cluster/auth/job configuration.
}
```

A positive `max_parallel_jobs` limits concurrent jobs across all Environments using
that Runner. An unset Core value retains legacy unlimited admission. Lowering the
limit drains existing jobs before admitting more work. This setting does not
change the Runner image's execution capacity by itself.

`supported_protocols` declares what the configured Runner image actually supports.
It accepts up to 16 unique names, each 1 to 128 characters. Do not declare
`environment-observation/v1` for an image that cannot execute that protocol. An
empty set declares no protocols; an unset value does not opt into observations.
Observation admission also requires an explicitly configured positive job limit.

Both fields are optional and reflect Core values when omitted. Existing Terraform
configurations and imported resources preserve operator-configured limits and
protocols during unrelated updates. A successful Terraform create or update with
a field configured establishes management of that field. Removing it afterward
sends an explicit null to clear it at Core. Removing the job limit restores
unlimited admission; removing protocols clears the opt-in. Each field is managed
independently.

Terraform does not persist new provider-private ownership on a no-op apply. Adding
an imported/operator value that already equals Core, without any real resource
change, therefore does not establish management. Removing that same-value
assertion later preserves Core's setting. To adopt it, make an intentional real
update while the field is configured (for example, change the Runner description),
then subsequent removal clears it. Alternatively, clear the setting explicitly
through the Core API. Plans never write to Core.

These provider fields configure the generic Runner. They do not enable the Drift
plugin or grant it observation or Deployment authority.
