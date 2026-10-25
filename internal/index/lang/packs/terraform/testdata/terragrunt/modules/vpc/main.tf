variable "cidr" {}

resource "aws_vpc" "main" {
  cidr_block = var.cidr
}

output "vpc_id" {
  value = aws_vpc.main.id
}
