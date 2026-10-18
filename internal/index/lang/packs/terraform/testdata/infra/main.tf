terraform {
  required_version = ">= 1.6"
}

module "network" {
  source = "./modules/network"
  cidr   = var.cidr
}

module "eks" {
  source  = "terraform-aws-modules/eks/aws"
  version = "~> 20.0"
}

resource "aws_s3_bucket" "logs" {
  bucket = "app-logs-${var.env}"
}

# resource "aws_instance" "commented_out" {}
