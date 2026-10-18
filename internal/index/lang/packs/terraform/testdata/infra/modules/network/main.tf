variable "cidr" {}

resource "aws_vpc" "this" {
  cidr_block = var.cidr
}

resource "aws_subnet" "private" {
  vpc_id = aws_vpc.this.id
}
