variable "project_id" {
  type        = string
  description = "Google Cloud project that hosts everything"
}

variable "region" {
  type    = string
  default = "us-central1" # the free tier of e2-micro works only in us-central1, us-east1 and us-west1
}

variable "zone" {
  type    = string
  default = "us-central1-a"
}
