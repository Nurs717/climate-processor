# Infrastructure (Terraform)

The core of the Google Cloud setup is described as code: the VM, its network, the service
accounts and their permissions, the image registry, the secret containers, and the Workload
Identity pool that lets GitHub Actions deploy without keys. The code was written **after** the
resources already existed and brought under Terraform with `import` blocks, so the first plan
reported no changes and nothing was recreated.

## What is (and is not) managed

| File | What it describes |
|---|---|
| `versions.tf` | Terraform and provider versions, the GCS state backend |
| `variables.tf`, `terraform.tfvars` | project ID, region, zone |
| `apis.tf` | enabled Google APIs |
| `network.tf` | the static IP `climate-ip` and the firewall rules (MQTTS on 8883, HTTP/HTTPS on 80 and 443, SSH only from IAP) |
| `compute.tf` | the `climate-vm` instance (e2-micro, Ubuntu 24.04, shielded VM) |
| `iam.tf` | the two service accounts: `climate-vm` (runtime) and `github-deployer` (CD) |
| `iam_bindings.tf` | their permissions, one binding per resource |
| `wif.tf` | Workload Identity pool and provider for GitHub, limited to this repository on `main` |
| `registry.tf` | Artifact Registry repository with a cleanup policy (keeps the 5 newest versions) |
| `secrets.tf` | the four Secret Manager secrets, **without values** |

Not in Terraform, on purpose:

- **Secret values.** They would end up in the state. They are added with `gcloud secrets versions add`,
  see [`deploy/gcp/README.md`](../deploy/gcp/README.md).
- **Software on the VM:** Docker, swap, the CA and the broker certificate, `mosquitto/passwd`. They are
  set up once by hand and afterwards maintained by the deploy pipeline.
- **GitHub repository variables** used by CD, and the billing budget alert.
- **The state bucket itself** (see below): Terraform cannot create the place where it keeps its own state.

## State

The state lives in the GCS bucket `<project-id>-tfstate` (prefix `climate`) with object versioning
enabled, so a bad write can be rolled back, and with locking, so two runs cannot overlap.
It never goes to git (`*.tfstate` is git-ignored).

The bucket was created once with commands like these:

```bash
BUCKET="$(gcloud config get-value project)-tfstate"
gcloud storage buckets create "gs://$BUCKET" --location=us-central1 \
  --uniform-bucket-level-access --public-access-prevention
gcloud storage buckets update "gs://$BUCKET" --versioning
```

The bucket name is written in `versions.tf` (a backend block cannot use variables); change it there
if you fork this repository.

## Everyday workflow

Needs Terraform >= 1.7, `gcloud`, and application default credentials
(`gcloud auth application-default login`).

```bash
cd infra
terraform init                 # once per clone
terraform fmt
terraform plan -out=tfplan     # read it: it must show only what you intended
terraform apply tfplan         # applies exactly the saved plan
```

CI (`terraform-ci.yml`) runs `fmt -check`, `init -backend=false` and `validate` on every change in
`infra/`. It never reads the state or touches GCP, so it needs no credentials. `plan` and `apply`
are run by hand.

`No changes` is the healthy state. A plan that wants to change or replace the VM, the static IP
or the IAM bindings of a working system deserves a second look before it is applied.

## Safety rails

- `prevent_destroy` on the static IP, the VM and the Workload Identity pool and provider. The IP is
  inside the broker certificate and the firmware, the VM's disk holds the certificates and the
  password file, and a deleted pool keeps its ID for 30 days. A plan that would destroy any of them fails.
- `ignore_changes` on the boot disk parameters: a newer Ubuntu image must not force a rebuild of the VM.
- `allow_stopping_for_update` is not set, so changing the machine type or the service account fails
  instead of silently rebooting the VM.
- IAM is managed with `*_iam_member` resources: they only add the listed binding and never remove
  bindings they do not describe.
- `disable_on_destroy = false` on the APIs: removing the code does not switch services off.

## Bringing an existing resource under Terraform

This is how the code was built, and how to add the next resource:

1. Write the `resource` block. Terraform can draft it: put an `import` block in a temporary
   `imports.tf` and run `terraform plan -generate-config-out=generated.tf`. Clean the result up: the
   generator writes every default and sometimes an invalid value (for example `vlan = 0`).
2. `terraform plan -out=tfplan` must show `N to import, 0 to add, 0 to change, 0 to destroy`. If it
   wants to change something, adjust the code to match reality, not the other way round.
3. `terraform apply tfplan`, delete `imports.tf` and `tfplan`, run `terraform plan` once more:
   `No changes`.

## Rebuilding in an empty project

Terraform recreates everything in the table above (create the state bucket first). This path has
not been rehearsed end to end. Expect these manual steps afterwards:

- A new static IP is a different address. It is inside the broker certificate, so issue new
  certificates with `gen-secrets.sh` and rebuild the firmware with the new address and CA.
- Add the secret values and set up the VM software as described in
  [`deploy/gcp/README.md`](../deploy/gcp/README.md), then set the GitHub variables.
- If the first `apply` races the enabling of an API, run it again.
