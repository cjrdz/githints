variable "cidr" {
  type = string
}

variable "env" {}

output "bucket" {
  value = aws_s3_bucket.logs.id
}
