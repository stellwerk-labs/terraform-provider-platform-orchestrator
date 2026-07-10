# Terraform Provider Platform Orchestrator

This repo holds the source code for the Platform Orchestrator terraform provider. This allows organizations to configure all platform engineering concerns through infrastructure as code.

## License

Licensed under the European Union Public Licence, version 1.2.

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.0
- [Go](https://golang.org/doc/install) >= 1.23

## Building The Provider

1. Clone the repository
1. Enter the repository directory
1. Build the provider using the Go `install` command:

```shell
go install
```

## Adding Dependencies

This provider uses [Go modules](https://github.com/golang/go/wiki/Modules).
Please see the Go documentation for the most up to date information about using Go modules.

To add a new dependency `github.com/author/dependency` to your Terraform provider:

```shell
go get github.com/author/dependency
go mod tidy
```

Then commit the changes to `go.mod` and `go.sum`.

## Using the provider

After publication, the provider will be available as [stellwerk-labs/platform-orchestrator](https://registry.terraform.io/providers/stellwerk-labs/platform-orchestrator/latest) in the Terraform Registry and [stellwerk-labs/platform-orchestrator](https://search.opentofu.org/provider/stellwerk-labs/platform-orchestrator/latest) in the OpenTofu Registry.

```hcl
terraform {
  required_providers {
    platform-orchestrator = {
      source  = "stellwerk-labs/platform-orchestrator"
      version = "~> 1.0"
    }
  }
}

provider "platform-orchestrator" {
  org_id = "organization"
}
```

The provider uses `https://api.stellwerk.localhost` by default. You can override it with the `api_url` provider attribute, the `PO_API_URL` environment variable, or an `octl` config file.

## Registry publication prerequisites

Terraform Registry publication requires a public GitHub repository named `terraform-provider-platform-orchestrator`, Terraform Registry access through a GitHub account with access to the `stellwerk-labs` organization, and an RSA or DSA GPG signing key registered in the Terraform Registry. The release workflow expects the private key and passphrase in `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` repository secrets.

OpenTofu Registry publication is a separate process. Submit the provider and signing key through the OpenTofu Registry GitHub issue forms after the GitHub repository and signed release are available.

## Developing the Provider

If you wish to work on the provider, you'll first need [Go](http://www.golang.org) installed on your machine (see [Requirements](#requirements) above).

To compile the provider, run `go install`. This will build the provider and put the provider binary in the `$GOPATH/bin` directory.

To generate or update documentation, run `make generate`.

In order to run the full suite of Acceptance tests, run `make testacc`.

*Note:* Acceptance tests create real resources, and often cost money to run.

```shell
make testacc
```
